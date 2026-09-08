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

type coordinatedEngine struct {
	schedule func(context.Context, domain.Mode, time.Duration) error
	finite   func(context.Context, domain.Mode) (updater.Report, error)
}

func (e *coordinatedEngine) Schedule(ctx context.Context, mode domain.Mode, interval time.Duration) error {
	return e.schedule(ctx, mode, interval)
}
func (e *coordinatedEngine) RunContext(ctx context.Context, mode domain.Mode) (updater.Report, error) {
	return e.finite(ctx, mode)
}

func TestTheDaemonDispatchesScheduledWorkToTheCoordinator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduled := 0
	engine := &coordinatedEngine{
		schedule: func(received context.Context, mode domain.Mode, interval time.Duration) error {
			scheduled++
			if received != ctx || mode != domain.ModeApply || interval != 5*time.Minute {
				t.Fatal("scheduler received different options")
			}
			return nil
		},
		finite: func(context.Context, domain.Mode) (updater.Report, error) {
			t.Fatal("scheduled run used a finite cycle")
			return updater.Report{}, nil
		},
	}

	err := Run(ctx, Options{Updater: engine, Mode: domain.ModeApply, Interval: 5 * time.Minute})

	if err != nil || scheduled != 1 {
		t.Fatalf("err=%v scheduled=%d", err, scheduled)
	}
}

func TestOnceRoutesApplyAndBreakerObservationThroughTheCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var modes []domain.Mode
	engine := &coordinatedEngine{
		schedule: func(context.Context, domain.Mode, time.Duration) error {
			t.Fatal("once started the scheduler")
			return nil
		},
		finite: func(received context.Context, mode domain.Mode) (updater.Report, error) {
			if received != ctx {
				t.Fatal("finite run lost caller context")
			}
			modes = append(modes, mode)
			return updater.Report{BreakerOpen: true}, nil
		},
	}

	err := Run(ctx, Options{Updater: engine, Mode: domain.ModeApply, Once: true})

	if err != nil || !slices.Equal(modes, []domain.Mode{domain.ModeApply, domain.ModeMonitor}) {
		t.Fatalf("err=%v modes=%v", err, modes)
	}
}

func TestScheduledShutdownWaitsForCoordinatorDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	drain := make(chan struct{})
	done := make(chan error, 1)
	engine := &coordinatedEngine{schedule: func(ctx context.Context, _ domain.Mode, _ time.Duration) error {
		close(started)
		<-ctx.Done()
		<-drain
		return nil
	}}
	go func() { done <- Run(ctx, Options{Updater: engine, Mode: domain.ModeApply}) }()
	<-started

	cancel()
	select {
	case <-done:
		t.Fatal("daemon returned before coordinator drained")
	default:
	}
	close(drain)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish draining")
	}
}

func TestScheduledOwnershipFailureRemainsAnError(t *testing.T) {
	failure := errors.New("the run lease is no longer owned")
	engine := &coordinatedEngine{schedule: func(context.Context, domain.Mode, time.Duration) error { return failure }}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeApply})

	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
}

func TestOnceReturnsTheFiniteRunError(t *testing.T) {
	failure := errors.New("backend unavailable")
	engine := &coordinatedEngine{finite: func(context.Context, domain.Mode) (updater.Report, error) { return updater.Report{}, failure }}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeApply, Once: true})

	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
}

func TestOnceReturnsTheMonitorErrorAfterTheBreakerBlocksApply(t *testing.T) {
	failure := errors.New("observation failed")
	engine := &coordinatedEngine{finite: func(_ context.Context, mode domain.Mode) (updater.Report, error) {
		if mode == domain.ModeMonitor {
			return updater.Report{}, failure
		}
		return updater.Report{BreakerOpen: true}, nil
	}}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeApply, Once: true})

	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
}

func TestOnceMonitorDoesNotRepeatWhenTheBreakerIsOpen(t *testing.T) {
	calls := 0
	engine := &coordinatedEngine{finite: func(context.Context, domain.Mode) (updater.Report, error) {
		calls++
		return updater.Report{BreakerOpen: true}, nil
	}}

	err := Run(context.Background(), Options{Updater: engine, Mode: domain.ModeMonitor, Once: true})

	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
