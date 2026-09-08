// Package daemon runs Ripen on a schedule. It owns its process and
// writes nothing to stdout: the Event stream on stderr is its entire
// output, which is what lets a container's logs be parsed as a stream of
// Events and nothing else.
package daemon

import (
	"context"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/updater"
)

// Engine is the one thing a daemon does: run a Transaction.
type Engine interface {
	Run(mode domain.Mode) (updater.Report, error)
}

// Options configures a daemon loop.
type Options struct {
	Updater  Engine
	Mode     domain.Mode
	Interval time.Duration
	// Once runs a single cycle and returns its error. Nothing sleeps.
	Once bool
	// Sleep waits between cycles, reporting false when the context ended
	// first. Tests replace it to prove a --once run never waits.
	Sleep func(ctx context.Context, duration time.Duration) bool
}

// Run drives the loop until the context ends, or once when Once is set.
// A failed cycle is not a failed daemon: the engine has already put a
// run.failed Event on the stream, and the next cycle tries again.
func Run(ctx context.Context, options Options) error {
	if options.Sleep == nil {
		options.Sleep = wait
	}
	if options.Interval <= 0 {
		options.Interval = time.Hour
	}
	if !options.Once {
		if scheduled, ok := options.Updater.(interface {
			Schedule(context.Context, domain.Mode, time.Duration) error
		}); ok {
			return scheduled.Schedule(ctx, options.Mode, options.Interval)
		}
	}
	run := options.Updater.Run
	if contextual, ok := options.Updater.(interface {
		RunContext(context.Context, domain.Mode) (updater.Report, error)
	}); ok {
		run = func(mode domain.Mode) (updater.Report, error) { return contextual.RunContext(ctx, mode) }
	}
	for {
		report, err := run(options.Mode)
		if err == nil && options.Mode == domain.ModeApply && report.BreakerOpen && ctx.Err() == nil {
			_, err = run(domain.ModeMonitor)
		}
		if options.Once {
			return err
		}
		if ctx.Err() != nil {
			return nil //nolint:nilerr // a cancelled daemon exited cleanly, whatever the last cycle did
		}
		if !options.Sleep(ctx, options.Interval) {
			return nil
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
