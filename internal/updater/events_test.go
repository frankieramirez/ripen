package updater

import (
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

func checkDurability(t *testing.T, harness *harness, recorded recordedEvent) {
	t.Helper()
	key := state.Key{
		Backend: recorded.subject.Backend,
		Stack:   recorded.subject.Stack,
		Service: recorded.subject.Service,
	}
	switch recorded.name {
	case event.BaselineRecorded, event.ProposalDeployed, event.StackRecovered:
		if got := harness.accepted(key); got != recorded.data.Digest {
			t.Errorf("%s: accepted digest is %q, want %q written before the event",
				recorded.name, got, recorded.data.Digest)
		}
	case event.TransactionSucceeded:
		if got := harness.accepted(key); got != recorded.data.NewDigest {
			t.Errorf("%s: accepted digest is %q, want %q written before the event",
				recorded.name, got, recorded.data.NewDigest)
		}
	case event.CandidateObserved, event.CandidateMatured:
		candidate, err := harness.store.Candidate(key)
		if err != nil {
			t.Fatal(err)
		}
		if candidate == nil || candidate.Digest != recorded.data.Digest {
			t.Errorf("%s: the candidate was not written before the event", recorded.name)
		}
	case event.BreakerOpened, event.TransactionRolledBack, event.TransactionRollbackFailed:
		if !harness.status().BreakerOpen {
			t.Errorf("%s: the breaker was not open yet when the event went out", recorded.name)
		}
	case event.ProposalCreated:
		pending, err := harness.store.PendingProposal(key)
		if err != nil {
			t.Fatal(err)
		}
		if pending == nil || pending.Digest != recorded.data.Digest {
			t.Errorf("%s: the pending proposal was not written before the event", recorded.name)
		}
	case event.BreakerCleared:
		if harness.status().BreakerOpen {
			t.Errorf("%s: the breaker was still open when the event went out", recorded.name)
		}
	}
}

func TestEveryPagingEventFollowsADurableStateChange(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	harness.events.onEmit = func(recorded recordedEvent) { checkDurability(t, harness, recorded) }

	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "web", domain.ResultUpdated)

	for _, want := range []event.Name{
		event.BaselineRecorded, event.CandidateObserved, event.CandidateMatured,
		event.TransactionStarted, event.TransactionSucceeded, event.RunFinished,
	} {
		if !harness.events.saw(want) {
			t.Errorf("no %s event was emitted", want)
		}
	}
}

func TestARollbackAnnouncesItselfOnlyOnceTheBreakerIsWritten(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}
	harness.events.onEmit = func(recorded recordedEvent) { checkDurability(t, harness, recorded) }

	harness.expect(harness.run(domain.ModeApply), "web", domain.ResultRolledBack)

	if !harness.events.saw(event.TransactionRolledBack) || !harness.events.saw(event.BreakerOpened) {
		t.Error("a rollback must announce both the rollback and the breaker")
	}
}

func TestAProposalIsAnnouncedOnlyOnceItIsRecorded(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.events.onEmit = func(recorded recordedEvent) { checkDurability(t, harness, recorded) }

	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	if !harness.events.saw(event.ProposalCreated) {
		t.Error("no proposal.created event was emitted")
	}
}

func TestAServiceComingBackAnnouncesItselfAndIsAudited(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}
	harness.expect(harness.run(domain.ModeApply), "web", domain.ResultRolledBack)
	harness.health.answer = nil
	harness.events.events = nil
	harness.events.onEmit = func(recorded recordedEvent) { checkDurability(t, harness, recorded) }

	harness.expect(harness.run(domain.ModeMonitor), "web", domain.ResultCandidate)

	if !harness.events.saw(event.StackRecovered) {
		t.Fatal("a service that came back must say so")
	}
	attempts, err := harness.store.Attempts(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 || attempts[0].Result != domain.ResultUpToDate {
		t.Errorf("audit = %+v, want the recovery recorded before it was announced", attempts)
	}

	harness.events.events = nil
	harness.run(domain.ModeMonitor)
	if harness.events.saw(event.StackRecovered) {
		t.Error("recovery must be announced on the transition, not on every run afterwards")
	}
}
