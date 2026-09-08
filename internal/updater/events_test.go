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

func TestABreakerBlockedRunEmitsItsCompletionAndReason(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, singleCompose)
	harness := singleHarness(t, singleStack("media", domain.BackendDockerCompose), engine)
	if err := harness.store.OpenBreaker("media: verification failed", harness.clock.now); err != nil {
		t.Fatal(err)
	}

	report := harness.run(domain.ModeApply)

	if len(harness.events.events) != 1 {
		t.Fatalf("events = %v, want one completion", harness.events.events)
	}
	got := harness.events.events[0]
	if got.name != event.RunFinished || got.subject.RunID != report.RunID ||
		!got.data.BreakerOpen || got.data.Mode != "apply" || got.data.ResultCount != 1 ||
		got.data.Reason != "media: verification failed" {
		t.Fatalf("completion = %+v", got)
	}
}

func TestARollbackFinishesTheRunAndMonitorRefreshesUnrelatedCandidates(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	observer := newBackend(domain.BackendPortainer, singleCompose)
	observer.imageStatus = "updated"
	stack := singleStack("observer", domain.BackendPortainer)
	stack.AutoApply = false
	harness := newHarness(t, policyFor(multiStack(), stack), map[domain.Backend]*fakeBackend{
		domain.BackendDockerCompose: engine, domain.BackendPortainer: observer,
	})
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}
	harness.events.events = nil

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultRolledBack)
	if !harness.events.saw(event.RunFinished) || !report.BreakerOpen {
		t.Fatal("the rollback run must finish with an open breaker")
	}
	observerKey := state.Key{Backend: domain.BackendPortainer, Stack: "observer"}
	before, err := harness.store.Candidate(observerKey)
	if err != nil || before == nil {
		t.Fatalf("candidate = %v, error = %v", before, err)
	}
	harness.mature()
	harness.run(domain.ModeApply)
	harness.run(domain.ModeMonitor)

	after, err := harness.store.Candidate(observerKey)
	if err != nil || after == nil || after.Count != before.Count+1 || !after.LastSeen.After(before.LastSeen) {
		t.Fatalf("candidate before = %+v, after = %+v, error = %v", before, after, err)
	}
	if len(engine.deployments) != 2 || len(observer.deployments) != 0 || !harness.status().BreakerOpen {
		t.Fatal("monitor must preserve the breaker and deploy nothing after rollback")
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
