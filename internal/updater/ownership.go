package updater

import (
	"context"
	"errors"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/health"
	"github.com/frankieramirez/ripen/internal/registry"
)

func (u *Updater) withContext(ctx context.Context) *Updater {
	scoped := *u
	scoped.ctx = ctx
	scoped.backends = make(map[domain.Backend]backend.Port, len(u.backends))
	for name, port := range u.backends {
		scoped.backends[name] = backend.WithContext(ctx, port)
	}
	if client, ok := u.registry.(interface {
		WithContext(context.Context) *registry.Client
	}); ok {
		scoped.registry = client.WithContext(ctx)
	}
	if checker, ok := u.health.(interface {
		WithContext(context.Context) *health.Checker
	}); ok {
		scoped.health = checker.WithContext(ctx)
	}
	return &scoped
}

func (u *Updater) owned(ctx context.Context, token string) (*Updater, func()) {
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	reads, cancelReads := context.WithCancel(ctx)
	stopReadCancel := context.AfterFunc(leaseCtx, cancelReads)
	scoped := u.withContext(reads)
	scoped.stopReads = cancelReads
	scoped.token = token
	scoped.state = u.state.WithLease(token, u.clock.Now)
	scoped.leaseCtx = leaseCtx
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := time.Duration(u.policy.LeaseTTLSeconds) * time.Second / 3
		ticks, stop := u.ticker(interval)
		defer stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticks:
				if err := u.state.RenewLease(token, u.clock.Now(), u.policy.LeaseTTLSeconds); err != nil {
					cancelLease()
					return
				}
			}
		}
	}()
	return scoped, func() { cancelLease(); cancelReads(); stopReadCancel(); <-done; _ = u.state.ReleaseLease(token) }
}

func (u *Updater) checkOwnership() error {
	if u.ctx != nil && u.ctx.Err() != nil {
		return u.ctx.Err()
	}
	if u.token == "" {
		return errors.New("no run owns the state lease")
	}
	return u.state.CheckLease(u.token, u.clock.Now())
}

func (u *Updater) ticker(interval time.Duration) (<-chan time.Time, func()) {
	if clock, ok := u.clock.(interface {
		Ticker(time.Duration) (<-chan time.Time, func())
	}); ok {
		return clock.Ticker(interval)
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}
