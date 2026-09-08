package updater

import (
	"testing"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/state"
)

func recoveryHarness(t *testing.T) (*harness, *fakeBackend, state.Key) {
	t.Helper()
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.running = map[string]string{"web": baseDigest}
	h := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	key := state.Key{Backend: domain.BackendPortainer, Stack: "media"}
	if err := h.store.SetAcceptedDigest(key, baseDigest, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	token, ok, err := h.store.AcquireLease(h.clock.Now(), 30)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := h.store.BeginTransaction(token, key, "interrupted", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.store.OpenBreaker("interrupted", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.store.ReleaseLease(token); err != nil {
		t.Fatal(err)
	}
	return h, engine, key
}

func TestExplicitReconciliationRecordsHealthyBaselineAndClearsInterruptedMarker(t *testing.T) {
	h, engine, key := recoveryHarness(t)

	status, err := h.updater.ReconcileTransaction("confirmed the backend request finished")

	if err != nil || status.BreakerOpen {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	marker, err := h.store.ActiveTransaction()
	if err != nil || marker != nil {
		t.Fatalf("marker=%+v err=%v", marker, err)
	}
	attempt, err := h.store.LastAttempt(key)
	if err != nil || attempt == nil || attempt.Result != domain.ResultUpToDate {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	if len(engine.deployments) != 0 {
		t.Fatal("reconciliation deployed a change")
	}
}

func TestExplicitReconciliationRefusesAnUnprovenRunningDigest(t *testing.T) {
	h, engine, _ := recoveryHarness(t)
	engine.running = nil
	engine.imageStatus = "updated"

	_, err := h.updater.ReconcileTransaction("backend request finished")

	if err == nil {
		t.Fatal("image status substituted for a proven running digest")
	}
	marker, readErr := h.store.ActiveTransaction()
	if readErr != nil || marker == nil {
		t.Fatal("marker lost", readErr)
	}
}

func TestExplicitReconciliationRefusesAnUnhealthyStack(t *testing.T) {
	h, engine, _ := recoveryHarness(t)
	engine.servicesUp = false

	_, err := h.updater.ReconcileTransaction("backend request finished")

	if err == nil {
		t.Fatal("unhealthy stack reconciled")
	}
	marker, readErr := h.store.ActiveTransaction()
	if readErr != nil || marker == nil {
		t.Fatal("marker lost", readErr)
	}
}

func TestExplicitReconciliationRefusesAnActiveLease(t *testing.T) {
	h, engine, _ := recoveryHarness(t)
	if _, ok, err := h.store.AcquireLease(h.clock.Now(), 30); err != nil || !ok {
		t.Fatal(err)
	}

	_, err := h.updater.ReconcileTransaction("backend request finished")

	if err == nil {
		t.Fatal("active owner bypassed")
	}
	if engine.observations != 0 {
		t.Fatal("busy reconciliation read the backend")
	}
}

func TestExplicitReconciliationCannotClearAnUncertainProposal(t *testing.T) {
	h, engine, _ := recoveryHarness(t)
	marker, err := h.store.ActiveTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetTransactionPhase(marker.OwnerToken, "proposing", h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	_, err = h.updater.ReconcileTransaction("backend request finished")

	if err == nil {
		t.Fatal("uncertain proposal reconciled from baseline health")
	}
	if engine.observations != 0 {
		t.Fatal("proposal reconciliation read the backend")
	}
}
