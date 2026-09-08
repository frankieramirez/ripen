package updater

import (
	"context"
	"fmt"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

func TestCancellationDuringProposalHealthDoesNotAcceptOrOpenTheBreaker(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		t.Run(fmt.Sprintf("healthy=%v", healthy), func(t *testing.T) {
			engine := gitBackend()
			h := singleHarness(t, gitStack(), engine)
			ripen(h, engine, newDigest)
			h.expect(h.run(domain.ModeApply), "", domain.ResultProposed)
			engine.compose = proposedCompose
			engine.running["web"] = newDigest
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.health.answer = func(config.HealthPolicy, int) (bool, error) { cancel(); return healthy, nil }

			_, _ = h.updater.RunContext(ctx, domain.ModeMonitor)

			if h.status().BreakerOpen {
				t.Fatal("canceled health check opened the breaker")
			}
			if h.accepted(key(domain.BackendPortainer, "")) != baseDigest {
				t.Fatal("canceled health check accepted the proposal")
			}
			if len(h.status().PendingProposals) != 1 {
				t.Fatal("canceled health check cleared the proposal")
			}
			if h.events.saw(event.ProposalDeployed) || h.events.saw(event.BreakerOpened) {
				t.Fatal("canceled health check emitted a deployment outcome")
			}
		})
	}
}

func TestCancellationDuringRecoveryHealthPreservesFailureAndCandidateHistory(t *testing.T) {
	for _, healthy := range []bool{false, true} {
		t.Run(fmt.Sprintf("healthy=%v", healthy), func(t *testing.T) {
			engine := gitBackend()
			h := singleHarness(t, gitStack(), engine)
			ripen(h, engine, newDigest)
			k := key(domain.BackendPortainer, "")
			if err := h.store.RecordAttempt(state.Attempt{Key: k, RunID: "previous", Actor: domain.ActorDaemon, Result: domain.ResultError, Detail: "previous failure"}, h.clock.Now()); err != nil {
				t.Fatal(err)
			}
			before, err := h.store.Candidate(k)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.health.answer = func(config.HealthPolicy, int) (bool, error) { cancel(); return healthy, nil }

			_, _ = h.updater.RunContext(ctx, domain.ModeMonitor)

			last, err := h.store.LastAttempt(k)
			if err != nil {
				t.Fatal(err)
			}
			if last == nil || last.Result != domain.ResultError || last.RunID != "previous" {
				t.Fatalf("failure replaced: %+v", last)
			}
			after, err := h.store.Candidate(k)
			if err != nil {
				t.Fatal(err)
			}
			if before == nil || after == nil || before.Count != after.Count {
				t.Fatal("canceled reconciliation advanced candidate")
			}
			if h.events.saw(event.StackRecovered) {
				t.Fatal("canceled health check emitted recovery")
			}
		})
	}
}
