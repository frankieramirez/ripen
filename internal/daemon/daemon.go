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

// Engine coordinates scheduled and finite runs.
type Engine interface {
	Schedule(context.Context, domain.Mode, time.Duration) error
	RunContext(context.Context, domain.Mode) (updater.Report, error)
}

// Options configures a daemon loop.
type Options struct {
	Updater  Engine
	Mode     domain.Mode
	Interval time.Duration
	// Once runs a single cycle and returns its error. Nothing sleeps.
	Once bool
}

// Run drives the loop until the context ends, or once when Once is set.
// A failed cycle is not a failed daemon: the engine has already put a
// run.failed Event on the stream, and the next cycle tries again.
func Run(ctx context.Context, options Options) error {
	if options.Interval <= 0 {
		options.Interval = time.Hour
	}
	if !options.Once {
		return options.Updater.Schedule(ctx, options.Mode, options.Interval)
	}
	report, err := options.Updater.RunContext(ctx, options.Mode)
	if err == nil && options.Mode == domain.ModeApply && report.BreakerOpen && ctx.Err() == nil {
		_, err = options.Updater.RunContext(ctx, domain.ModeMonitor)
	}
	return err
}
