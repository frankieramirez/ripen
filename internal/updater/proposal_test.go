package updater

import (
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
)

func gitStack() config.StackPolicy {
	stack := singleStack("media", domain.BackendPortainer)
	stack.GitPath = "stacks/media/compose.yaml"
	return stack
}

func gitBackend() *fakeBackend {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.gitBacked = true
	engine.running = map[string]string{"web": baseDigest}
	return engine
}

// proposed is the document a merged Proposal would leave behind.
var proposedCompose = "services:\n  web:\n    image: \"" + webImage + "@" + newDigest + "\"\n"

func TestAGitBackedStackProposesInsteadOfRedeploying(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultProposed)
	if report.UpdatesApplied != 0 {
		t.Errorf("updates applied = %d, want 0: a proposal is not a deployment", report.UpdatesApplied)
	}
	if len(engine.deployments) != 0 {
		t.Fatal("a git-backed stack must never be deployed directly")
	}
	if len(harness.proposals.changes) != 1 {
		t.Fatalf("proposals = %d, want exactly one", len(harness.proposals.changes))
	}
	change := harness.proposals.changes[0]
	if change.RepositoryPath != "stacks/media/compose.yaml" {
		t.Errorf("repository path = %q, want the configured git_path", change.RepositoryPath)
	}
	if change.ExpectedContent != singleCompose {
		t.Errorf("expected content = %q, want the live reviewed document", change.ExpectedContent)
	}
	if change.ProposedContent != proposedCompose {
		t.Errorf("proposed content = %q, want the pinned document", change.ProposedContent)
	}
	if change.Label != "media" {
		t.Errorf("label = %q, want the service identity", change.Label)
	}
	if !harness.events.saw(event.ProposalCreated) {
		t.Error("opening a proposal must reach the event stream")
	}
	pending := harness.status().PendingProposals
	if len(pending) != 1 || pending[0].Digest != newDigest || pending[0].URL != harness.proposals.result.URL {
		t.Errorf("pending proposals = %+v, want the digest and URL recorded", pending)
	}
	if !strings.Contains(result.Detail, harness.proposals.result.URL) {
		t.Errorf("detail = %q, want the proposal URL", result.Detail)
	}
}

func TestApplyRefusesToDetachAGitBackedStackWithNoProposalConfiguration(t *testing.T) {
	engine := gitBackend()
	stack := gitStack()
	stack.GitPath = ""
	harness := singleHarness(t, stack, engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	harness.expect(report, "", domain.ResultIneligible)
	if len(engine.deployments) != 0 {
		t.Error("a git-backed stack must never be deployed directly")
	}
	if harness.status().BreakerOpen {
		t.Error("refusing to act is not a failure; the breaker must stay closed")
	}
}

func TestNoSecondProposalIsOpenedWhileOneIsPendingReview(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultIneligible)
	if !strings.Contains(result.Detail, "already pending review") {
		t.Errorf("detail = %q, want the pending-proposal guard", result.Detail)
	}
	if len(harness.proposals.changes) != 1 {
		t.Errorf("proposals = %d, want no second proposal for an unmerged one", len(harness.proposals.changes))
	}
}

func TestANewerDigestDoesNotSupersedeAProposalUnderReview(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	harness.registry.digests[webImage] = thirdDigest
	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultIneligible)
	if !strings.Contains(result.Detail, "different proposal") {
		t.Errorf("detail = %q, want the different-proposal refusal", result.Detail)
	}
	if len(harness.proposals.changes) != 1 {
		t.Errorf("proposals = %d, want the review left alone", len(harness.proposals.changes))
	}
}

func TestAGitDeploymentIsAcceptedOnlyAfterThePinAndTheRunningDigestMatch(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	// The proposal is merged and the forge deploys it.
	engine.compose = proposedCompose
	engine.running["web"] = newDigest
	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "", domain.ResultUpdated)
	if report.UpdatesApplied != 0 {
		t.Errorf("updates applied = %d, want 0: the forge deployed it, not Ripen", report.UpdatesApplied)
	}
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != newDigest {
		t.Errorf("accepted digest = %q, want %q", got, newDigest)
	}
	if pending := harness.status().PendingProposals; len(pending) != 0 {
		t.Errorf("pending proposals = %+v, want the merged proposal cleared", pending)
	}
	if len(engine.deployments) != 0 {
		t.Error("accepting a git deployment must issue no deployment of its own")
	}
}

func TestAPinnedButUnhealthyGitDeploymentOpensTheBreakerWithoutAccepting(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	engine.compose = proposedCompose
	engine.running["web"] = newDigest
	harness.health.answer = func(config.HealthPolicy, int) (bool, error) { return false, nil }
	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "", domain.ResultError)
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != baseDigest {
		t.Errorf("accepted digest = %q, want the baseline kept", got)
	}
	status := harness.status()
	if !status.BreakerOpen {
		t.Fatal("an unhealthy git deployment must open the breaker")
	}
	if !strings.Contains(status.BreakerReason, "media") {
		t.Errorf("breaker reason = %q, want it to name the stack", status.BreakerReason)
	}
}

func TestAnOperatorCanClearAReviewedStaleProposal(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)

	status, err := harness.updater.ClearProposal("media", "reviewed and rejected")
	if err != nil {
		t.Fatal(err)
	}

	if len(status.PendingProposals) != 0 {
		t.Errorf("pending proposals = %+v, want none after clearing", status.PendingProposals)
	}
	if _, err := harness.updater.ClearProposal("media", "reviewed and rejected"); err == nil {
		t.Error("clearing nothing must be an error, not a silent success")
	}
}

func TestAnOpenBreakerBlocksProposalsAsWellAsApplies(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	if err := harness.store.OpenBreaker("media: rollback failed", harness.clock.now); err != nil {
		t.Fatal(err)
	}

	report := harness.run(domain.ModeApply)
	if len(report.Results) != 1 || report.Results[0].Code != domain.ResultBreakerOpen {
		t.Errorf("run = %+v, want the run halted", report.Results)
	}

	// The propose verb takes the same route to the same refusal.
	result, _, err := harness.updater.Propose("media")
	if err != nil {
		t.Fatal(err)
	}

	if result.Code != domain.ResultBreakerOpen {
		t.Errorf("propose = %s, want breaker_open: the breaker halts every outbound action", result.Code)
	}
	if len(harness.proposals.changes) != 0 {
		t.Error("no proposal may be opened while the breaker is open")
	}
}
