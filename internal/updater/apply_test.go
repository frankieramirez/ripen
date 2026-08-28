package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
)

const sidecarHealthTarget = "http://media:9090/health"

func ripen(harness *harness, engine *fakeBackend, digest string) {
	harness.t.Helper()
	harness.run(domain.ModeMonitor)
	harness.registry.digests[webImage] = digest
	if engine.running == nil {
		engine.imageStatus = "outdated"
	}
	harness.run(domain.ModeMonitor)
	harness.mature()
}

func TestApplyWaitsForTheCandidateMaturityWindow(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	harness.run(domain.ModeMonitor)
	harness.registry.digests[webImage] = newDigest
	engine.imageStatus = "outdated"

	first := harness.run(domain.ModeApply)

	harness.expect(first, "", domain.ResultCandidate)
	if len(engine.deployments) != 0 {
		t.Fatalf("deployments = %d, want none before the window elapses", len(engine.deployments))
	}

	harness.mature()
	second := harness.run(domain.ModeApply)

	harness.expect(second, "", domain.ResultUpdated)
}

func TestApplyRedeploysASingleServiceStackWithOneRepull(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	harness.expect(report, "", domain.ResultUpdated)
	if report.UpdatesApplied != 1 {
		t.Errorf("updates applied = %d, want 1", report.UpdatesApplied)
	}
	if len(engine.deployments) != 1 {
		t.Fatalf("deployments = %d, want exactly one", len(engine.deployments))
	}
	if !engine.lastDeployment().repull {
		t.Error("a stack observed only through image status must be redeployed with a repull")
	}
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != newDigest {
		t.Errorf("accepted digest = %q, want %q", got, newDigest)
	}
	if harness.status().BreakerOpen {
		t.Error("a successful transaction must leave the breaker closed")
	}
}

func TestApplyPinsOnlyTheChangedServiceOfAMultiServiceStack(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultUpdated)
	harness.expect(report, "sidecar", domain.ResultUpToDate)
	deployed := engine.lastDeployment()
	if deployed.repull {
		t.Error("a digest-pinned deploy must not ask for a repull")
	}
	if !strings.Contains(deployed.compose, `image: "`+webImage+"@"+newDigest+`"`) {
		t.Errorf("deployed compose does not pin web:\n%s", deployed.compose)
	}
	if !strings.Contains(deployed.compose, "image: "+sidecarImage+"\n") {
		t.Errorf("deployed compose changed the sibling service:\n%s", deployed.compose)
	}

	follow := harness.run(domain.ModeMonitor)
	harness.expect(follow, "web", domain.ResultUpToDate)
	harness.expect(follow, "sidecar", domain.ResultUpToDate)
}

func TestAtMostOneServiceIsUpdatedPerRun(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	harness.run(domain.ModeMonitor)
	harness.registry.digests[webImage] = newDigest
	harness.registry.digests[sidecarImage] = thirdDigest
	harness.run(domain.ModeMonitor)
	harness.mature()

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultUpdated)
	harness.expect(report, "sidecar", domain.ResultCandidate)
	if report.UpdatesApplied != 1 {
		t.Errorf("updates applied = %d, want 1", report.UpdatesApplied)
	}
	if len(engine.deployments) != 1 {
		t.Errorf("deployments = %d, want exactly one", len(engine.deployments))
	}
}

func TestApplyCancelsWhenTheComposeDriftsAfterPlanning(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	observations := 0
	engine.onObserve = func(f *fakeBackend, _ int) {
		observations++
		if observations == 2 {
			f.compose += "# an operator edited this between planning and applying\n"
		}
	}

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "web", domain.ResultDrifted)
	if !strings.Contains(result.Detail, "between planning and applying") {
		t.Errorf("detail = %q, want it to name the drift", result.Detail)
	}
	if len(engine.deployments) != 0 {
		t.Error("a drifted stack must not be deployed")
	}
}

func TestAnInterpolatedImageLineCannotBePinnedAndIsIneligible(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, "services:\n  web:\n    image: ghcr.io/example/web:${TAG}\n")
	engine.running = map[string]string{"web": baseDigest}
	engine.resolved = map[string]string{"web": webImage}
	harness := singleHarness(t, singleStack("media", domain.BackendDockerCompose), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultIneligible)
	if !strings.Contains(result.Detail, "variable-interpolated") {
		t.Errorf("detail = %q, want it to name the interpolated image line", result.Detail)
	}
	if len(engine.deployments) != 0 {
		t.Error("an unpinnable stack must not be deployed")
	}
}

func TestAnUnhealthySiblingBlocksTheUpdateEntirely(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(policy config.HealthPolicy, _ int) (bool, error) {
		return policy.Target != sidecarHealthTarget, nil
	}

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultIneligible)
	if len(engine.deployments) != 0 {
		t.Error("an unhealthy sibling must block the deploy entirely")
	}
}

func TestAHealthOnlyServiceStillGatesItsSiblings(t *testing.T) {
	stack := multiStack()
	stack.Services[1].Enabled = false
	stack.Services[1].AutoApply = false
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, stack, engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(policy config.HealthPolicy, _ int) (bool, error) {
		return policy.Target != sidecarHealthTarget, nil
	}

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultIneligible)
	if len(engine.deployments) != 0 {
		t.Error("an unhealthy health-only service must block its siblings")
	}
}

func TestVerificationChecksEveryServiceBeforeAndAfterTheUpdate(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.checks = nil

	harness.expect(harness.run(domain.ModeApply), "web", domain.ResultUpdated)

	for _, target := range []string{"http://media:8080/health", sidecarHealthTarget} {
		if got := harness.health.checksFor(target); got < 2 {
			t.Errorf("checks for %s = %d, want at least one before and one after", target, got)
		}
	}
}

func TestFailedPostUpdateHealthRollsBackAndOpensTheBreaker(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultRolledBack)
	if !harness.events.saw(event.BreakerOpened) {
		t.Error("opening the breaker must reach the event stream")
	}
	status := harness.status()
	if !status.BreakerOpen {
		t.Fatal("a rollback must open the breaker")
	}
	if !strings.Contains(status.BreakerReason, "media/web") {
		t.Errorf("breaker reason = %q, want it to name the stack and service", status.BreakerReason)
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "web")); got != baseDigest {
		t.Errorf("accepted digest = %q, want the baseline kept", got)
	}
	rolled := engine.lastDeployment()
	if !strings.Contains(rolled.compose, `image: "`+webImage+"@"+baseDigest+`"`) {
		t.Errorf("rollback did not re-pin the baseline digest:\n%s", rolled.compose)
	}
	if !strings.Contains(rolled.compose, "image: "+sidecarImage+"\n") {
		t.Errorf("rollback touched the sibling service:\n%s", rolled.compose)
	}
}

func TestAFailedRollbackIsReportedAndBlocksEveryFutureApply(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		return len(engine.deployments) == 0, nil
	}

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultRollbackFailed)
	if !harness.status().BreakerOpen {
		t.Fatal("a failed rollback must open the breaker")
	}

	next := harness.run(domain.ModeApply)
	if len(next.Results) != 1 || next.Results[0].Code != domain.ResultBreakerOpen {
		t.Errorf("next apply = %+v, want a single breaker_open result", next.Results)
	}
}

func TestAHealthCheckThatErrorsCountsAsUnhealthy(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(_ config.HealthPolicy, _ int) (bool, error) {
		if len(engine.deployments) == 1 {
			return false, errors.New("dial tcp: connection refused")
		}
		return true, nil
	}

	report := harness.run(domain.ModeApply)

	harness.expect(report, "web", domain.ResultRolledBack)
	if !harness.status().BreakerOpen {
		t.Error("a check that blew up must be treated as unhealthy, not ignored")
	}
}

func TestATimedOutDeployIsAcceptedWhenImageStatusAndHealthProveSuccess(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	engine.deployLands = true
	engine.deployErr = func(attempt int) error {
		if attempt == 1 {
			return fmt.Errorf("portainer request failed: %w", context.DeadlineExceeded)
		}
		return nil
	}
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultUpdated)
	if !strings.Contains(result.Detail, "timed out") {
		t.Errorf("detail = %q, want it to name the ambiguity that was resolved", result.Detail)
	}
	if len(engine.deployments) != 1 {
		t.Errorf("deployments = %d, want no second deploy for an ambiguous success", len(engine.deployments))
	}
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != newDigest {
		t.Errorf("accepted digest = %q, want %q", got, newDigest)
	}
	if harness.status().BreakerOpen {
		t.Error("a proven deploy must leave the breaker closed")
	}
}

func TestEveryWriteRecordsTheRunAndTheSurfaceThatMadeIt(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	attempts, err := harness.store.Attempts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Fatal("an apply must leave an audit trail")
	}
	if attempts[0].Actor != domain.ActorCLI {
		t.Errorf("actor = %q, want the surface that ran the transaction", attempts[0].Actor)
	}
	if attempts[0].RunID != report.RunID {
		t.Errorf("run id = %q, want the report's %q", attempts[0].RunID, report.RunID)
	}
	if attempts[0].Key.Service != "web" {
		t.Errorf("audit key = %+v, want the updated service", attempts[0].Key)
	}
}
