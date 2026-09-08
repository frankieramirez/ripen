package updater

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/state"
)

func TestCanceledRunDoesNotObserveOrEstablishABaseline(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	h := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.updater.RunContext(ctx, domain.ModeMonitor)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if engine.observations != 0 || len(engine.deployments) != 0 {
		t.Fatal("canceled run touched the backend")
	}
	if got := h.accepted(state.Key{Backend: domain.BackendPortainer, Stack: "media"}); got != "" {
		t.Fatalf("baseline=%q", got)
	}
}

func TestAnUnfinishedRollbackRefusesBreakerClearingAndAnotherDeployment(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	h := singleHarness(t, multiStack(), engine)
	ripen(h, engine, newDigest)
	h.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) { return len(engine.deployments) == 0, nil }
	h.expect(h.run(domain.ModeApply), "web", domain.ResultRollbackFailed)
	active, err := h.store.ActiveTransaction()
	if err != nil || active == nil {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	before := len(engine.deployments)
	if err := h.store.ClearBreaker("operator reviewed failure", h.clock.Now()); err == nil {
		t.Fatal("clearing the breaker accepted an unfinished rollback")
	}

	h.run(domain.ModeApply)

	if len(engine.deployments) != before {
		t.Fatal("unfinished rollback allowed another deployment")
	}
	active, err = h.store.ActiveTransaction()
	if err != nil || active == nil {
		t.Fatalf("unfinished transaction lost: active=%+v err=%v", active, err)
	}
}

func TestAnExpiredLeaseDoesNotAuthorizeDeploymentPastAnInterruptedTransaction(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	h := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(h, engine, newDigest)
	token, acquired, err := h.store.AcquireLease(h.clock.Now(), 1)
	if err != nil || !acquired {
		t.Fatalf("lease acquired=%v err=%v", acquired, err)
	}
	if err := h.store.BeginTransaction(token, state.Key{Backend: domain.BackendPortainer, Stack: "interrupted"}, "old-run", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	h.clock.Sleep(2 * time.Second)

	h.run(domain.ModeApply)

	if len(engine.deployments) != 0 {
		t.Fatal("replacement owner deployed past an interrupted transaction")
	}
	active, err := h.store.ActiveTransaction()
	if err != nil || active == nil || active.RunID != "old-run" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}

type shutdownBackend struct {
	*fakeBackend
	cancel                   context.CancelFunc
	canceledDuringDeployment bool
}

func (b *shutdownBackend) WithContext(ctx context.Context) backend.Port {
	return &shutdownContextBackend{shutdownBackend: b, context: ctx}
}

type shutdownContextBackend struct {
	*shutdownBackend
	context context.Context
}

func (b *shutdownContextBackend) Deploy(stack backend.StackState, document string, repull bool) error {
	b.cancel()
	b.canceledDuringDeployment = b.context.Err() != nil
	return b.fakeBackend.Deploy(stack, document, repull)
}

func TestNormalShutdownAllowsTheActiveDeploymentToFinish(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	h := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(h, engine, newDigest)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := &shutdownBackend{fakeBackend: engine, cancel: cancel}
	h.updater.backends[domain.BackendPortainer] = port

	report, err := h.updater.RunContext(ctx, domain.ModeApply)

	if err != nil {
		t.Fatal(err)
	}
	if port.canceledDuringDeployment {
		t.Fatal("normal shutdown canceled the active deployment")
	}
	if ctx.Err() == nil {
		t.Fatal("test never requested shutdown")
	}
	h.expect(report, "", domain.ResultUpdated)
	active, err := h.store.ActiveTransaction()
	if err != nil || active != nil {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}
