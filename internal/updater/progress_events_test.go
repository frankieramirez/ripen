package updater

import (
	"errors"
	"testing"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

func TestTransactionProgressIsDurableBeforeTheSinkReceivesItsPhase(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	h := singleHarness(t, multiStack(), engine)
	ripen(h, engine, newDigest)
	phases := []event.Phase{}
	h.events.onEmit = func(recorded recordedEvent) {
		if recorded.name != event.TransactionProgress {
			return
		}
		marker, err := h.store.ActiveTransaction()
		if err != nil || marker == nil {
			t.Fatalf("progress has no persisted marker: %v", err)
		}
		if marker.Phase != string(recorded.data.Phase) || marker.RunID != recorded.subject.RunID || marker.Key.Service != recorded.subject.Service {
			t.Fatalf("marker=%+v event=%+v", marker, recorded)
		}
		phases = append(phases, recorded.data.Phase)
	}

	h.run(domain.ModeApply)

	if len(phases) != 2 || phases[0] != event.PhaseDeploying || phases[1] != event.PhaseVerifying {
		t.Fatalf("phases=%v", phases)
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
