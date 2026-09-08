package compose

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
)

type probeRunner struct{ calls atomic.Int32 }

func (r *probeRunner) Run(ctx context.Context, _ string, _, _ []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.calls.Add(1) == 1 {
		return nil, errors.New("unavailable")
	}
	return []byte("{}"), nil
}

func TestConcurrentContextClonesShareSuccessfulProbeAndRetryFailures(t *testing.T) {
	runner := &probeRunner{}
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))
	if adapter.Preflight() == nil {
		t.Fatal("first probe must fail")
	}
	var group sync.WaitGroup

	for range 20 {
		group.Go(func() {
			if err := adapter.WithContext(context.Background()).Preflight(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()

	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("probes=%d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.WithContext(ctx).Preflight(); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
