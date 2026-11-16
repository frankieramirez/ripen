package updater

import (
	"errors"
	"slices"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

func TestTransactionProgressIsDurableBeforeTheSinkReceivesItsPhase(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	h := singleHarness(t, multiStack(), engine)
	ripen(h, engine, newDigest)
	phases := []domain.Phase{}
	h.events.onEmit = func(recorded recordedEvent) {
		if recorded.name != event.TransactionProgress {
			return
		}
		marker, err := h.store.ActiveTransaction()
		if err != nil || marker == nil {
			t.Fatalf("progress has no persisted marker: %v", err)
		}
		if marker.Phase != recorded.data.Phase || marker.RunID != recorded.subject.RunID || marker.Key.Service != recorded.subject.Service {
			t.Fatalf("marker=%+v event=%+v", marker, recorded)
		}
		phases = append(phases, recorded.data.Phase)
	}

	h.run(domain.ModeApply)

	if len(phases) != 2 || phases[0] != domain.PhaseDeploying || phases[1] != domain.PhaseVerifying {
		t.Fatalf("phases=%v", phases)
	}
}

func announcedPhases(t *testing.T, h *harness) *[]domain.Phase {
	t.Helper()
	phases := &[]domain.Phase{}
	h.events.onEmit = func(recorded recordedEvent) {
		if recorded.name != event.TransactionProgress {
			return
		}
		marker, err := h.store.ActiveTransaction()
		if err != nil || marker == nil || marker.Phase != recorded.data.Phase {
			t.Fatalf("marker=%+v err=%v event=%+v", marker, err, recorded)
		}
		*phases = append(*phases, recorded.data.Phase)
	}
	return phases
}

func TestAProposalAnnouncesOnlyItsProposingPhaseFromADurableMarker(t *testing.T) {
	engine := gitBackend()
	h := singleHarness(t, gitStack(), engine)
	ripen(h, engine, newDigest)
	phases := announcedPhases(t, h)

	h.expect(h.run(domain.ModeApply), "", domain.ResultProposed)

	if !slices.Equal(*phases, []domain.Phase{domain.PhaseProposing}) {
		t.Fatalf("phases = %v, want only proposing", *phases)
	}
}

func TestAFailedVerificationAnnouncesEveryPhaseItPassedThroughInOrder(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	h := singleHarness(t, multiStack(), engine)
	ripen(h, engine, newDigest)
	h.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}
	phases := announcedPhases(t, h)

	h.expect(h.run(domain.ModeApply), "web", domain.ResultRolledBack)

	want := []domain.Phase{domain.PhaseDeploying, domain.PhaseVerifying, domain.PhaseRollingBack}
	if !slices.Equal(*phases, want) {
		t.Fatalf("phases = %v, want %v", *phases, want)
	}
}

func TestAFailedDeployAnnouncesItsRollbackWithoutEverVerifying(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	h := singleHarness(t, multiStack(), engine)
	ripen(h, engine, newDigest)
	engine.deployErr = func(attempt int) error {
		if attempt == 1 {
			return errors.New("the engine refused the request")
		}
		return nil
	}
	phases := announcedPhases(t, h)

	h.expect(h.run(domain.ModeApply), "web", domain.ResultRolledBack)

	want := []domain.Phase{domain.PhaseDeploying, domain.PhaseRollingBack}
	if !slices.Equal(*phases, want) {
		t.Fatalf("phases = %v, want %v", *phases, want)
	}
}

func TestRunTerminalEventsDistinguishSkippedStacksAndPreserveBreakerReason(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	h := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	if err := h.store.OpenBreaker("health timeout", h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	var received recordedEvent
	h.events.onEmit = func(recorded recordedEvent) { received = recorded }
	report := Report{RunID: "monitor", Mode: domain.ModeMonitor, Results: []Result{
		{Key: state.Key{Backend: domain.BackendPortainer, Stack: "media", Service: "web"}, Code: domain.ResultBusy},
		{Key: state.Key{Backend: domain.BackendPortainer, Stack: "media", Service: "sidecar"}, Code: domain.ResultBusy},
	}}

	h.updater.finishReport(report, nil)

	if received.name != event.RunFinished || received.data.SkippedStacks != 1 || !received.data.BreakerOpen || received.data.Reason != "health timeout" {
		t.Fatalf("event=%+v", received)
	}
	h.updater.finishReport(report, errors.New("observation failed"))
	if received.name != event.RunFailed || received.data.SkippedStacks != 1 || received.data.Detail != "observation failed" {
		t.Fatalf("failure=%+v", received)
	}
}
