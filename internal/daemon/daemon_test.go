package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/updater"
)

// fakeEngine counts cycles and answers however the test says.
type fakeEngine struct {
	cycles int
	err    error
	modes  []domain.Mode
}

func (f *fakeEngine) Run(mode domain.Mode) (updater.Report, error) {
	f.cycles++
	f.modes = append(f.modes, mode)
	return updater.Report{Mode: mode}, f.err
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
			// Stop after the loop has demonstrated it keeps going.
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
