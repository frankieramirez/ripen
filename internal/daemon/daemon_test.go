package daemon

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/updater"
)

type fakeEngine struct {
	cycles      int
	err         error
	modes       []domain.Mode
	breakerOpen bool
	monitorErr  error
}

func (f *fakeEngine) Run(mode domain.Mode) (updater.Report, error) {
	f.cycles++
	f.modes = append(f.modes, mode)
	if mode == domain.ModeMonitor && f.monitorErr != nil {
		return updater.Report{}, f.monitorErr
	}
	return updater.Report{Mode: mode, BreakerOpen: f.breakerOpen}, f.err
}

func TestOnceReportsAFailedMonitorAfterTheBreakerBlocksApply(t *testing.T) {
	engine := &fakeEngine{breakerOpen: true, monitorErr: errors.New("observation failed")}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeApply, Once: true})

	if !errors.Is(err, engine.monitorErr) {
		t.Fatalf("error = %v, want the monitor error", err)
	}
	if !slices.Equal(engine.modes, []domain.Mode{domain.ModeApply, domain.ModeMonitor}) {
		t.Fatalf("modes = %v, want apply followed by monitor", engine.modes)
	}
}

func TestAnOpenBreakerInMonitorModeDoesNotRunAnotherMonitor(t *testing.T) {
	engine := &fakeEngine{breakerOpen: true}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeMonitor, Once: true})

	if err != nil || engine.cycles != 1 {
		t.Fatalf("error = %v, cycles = %d, want one successful monitor", err, engine.cycles)
	}
}

func TestAnOpenBreakerKeepsScheduledObservationRunningAndClearingItResumesApply(t *testing.T) {
	engine := &fakeEngine{breakerOpen: true}
	cycles := 0

	err := Run(context.Background(), Options{
		Updater: engine,
		Mode:    domain.ModeApply,
		Sleep: func(context.Context, time.Duration) bool {
			cycles++
			if cycles == 2 {
				engine.breakerOpen = false
			}
			return cycles < 3
		},
	})

	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Mode{domain.ModeApply, domain.ModeMonitor, domain.ModeApply, domain.ModeMonitor, domain.ModeApply}
	if !slices.Equal(engine.modes, want) {
		t.Fatalf("modes = %v, want %v", engine.modes, want)
	}
}

func TestOnceRunsASingleCycleAndNeverSleeps(t *testing.T) {
	engine := &fakeEngine{}
	slept := 0

	err := Run(context.Background(), Options{
		Updater:  engine,
		Mode:     domain.ModeMonitor,
		Interval: time.Hour,
		Once:     true,
		Sleep:    func(context.Context, time.Duration) bool { slept++; return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	if engine.cycles != 1 {
		t.Errorf("cycles = %d, want 1", engine.cycles)
	}
	if slept != 0 {
		t.Errorf("slept %d times, want none: --once means once", slept)
	}
}

func TestOnceReturnsATransientRunErrorSoTheProcessCanExitNonZero(t *testing.T) {
	engine := &fakeEngine{err: errors.New("the backend refused the connection")}
	slept := 0

	err := Run(context.Background(), Options{
		Updater: engine,
		Mode:    domain.ModeMonitor,
		Once:    true,
		Sleep:   func(context.Context, time.Duration) bool { slept++; return true },
	})

	if err == nil || !errors.Is(err, engine.err) {
		t.Errorf("error = %v, want the run's own error", err)
	}
	if slept != 0 {
		t.Error("a failed --once run must not sleep before exiting")
	}
}

func TestTheLoopKeepsGoingAfterAFailedCycle(t *testing.T) {
	engine := &fakeEngine{err: errors.New("the registry timed out")}
	cycles := 0

	err := Run(context.Background(), Options{
		Updater:  engine,
		Mode:     domain.ModeApply,
		Interval: time.Hour,
		Sleep: func(context.Context, time.Duration) bool {
			cycles++
			return cycles < 3
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if engine.cycles != 3 {
		t.Errorf("cycles = %d, want the loop to survive failures", engine.cycles)
	}
	for _, mode := range engine.modes {
		if mode != domain.ModeApply {
			t.Errorf("mode = %q, want every cycle to run the configured mode", mode)
		}
	}
}

func TestACancelledDaemonStopsCleanly(t *testing.T) {
	engine := &fakeEngine{}
	ctx, cancel := context.WithCancel(context.Background())

	err := Run(ctx, Options{
		Updater:  engine,
		Mode:     domain.ModeMonitor,
		Interval: time.Hour,
		Sleep: func(context.Context, time.Duration) bool {
			cancel()
			return false
		},
	})

	if err != nil {
		t.Errorf("error = %v, want a cancelled daemon to be a clean exit", err)
	}
	if engine.cycles != 1 {
		t.Errorf("cycles = %d, want the one cycle it started", engine.cycles)
	}
}

func TestTheDefaultWaitStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if wait(ctx, time.Hour) {
		t.Error("wait must report false when the context ended first")
	}
}
