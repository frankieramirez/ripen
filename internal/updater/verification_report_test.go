package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

func unhealthyAfterTheFirstDeploy(engine *fakeBackend) func(config.HealthPolicy, int) (bool, error) {
	return func(config.HealthPolicy, int) (bool, error) {
		return len(engine.deployments) != 1, nil
	}
}

func TestAVerificationTimeoutSaysTheHealthTargetGaveNoAnswer(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = unhealthyAfterTheFirstDeploy(engine)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultRolledBack)
	want := "the functional health check timed out: at the last check the health target gave no answer"
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
	if reason := harness.status().BreakerReason; reason != "media: "+want {
		t.Errorf("breaker reason = %q, want %q", reason, "media: "+want)
	}
}

func TestAVerificationTimeoutNamesTheServiceAndTheStatusItsHealthTargetAnswered(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.unhealthyStatus = 503
	harness.health.answer = func(policy config.HealthPolicy, _ int) (bool, error) {
		return policy.Target != sidecarHealthTarget || len(engine.deployments) != 1, nil
	}

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "web", domain.ResultRolledBack)
	want := `at the last check the health target for service "sidecar" answered 503, which is not an accepted status`
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
}

func TestAVerificationTimeoutRepeatsWhatTheBackendFoundNotRunning(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	engine.deployErr = func(attempt int) error {
		engine.servicesUp = attempt != 1
		return nil
	}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "web", domain.ResultRolledBack)
	want := "at the last check a service is not running"
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
}

func TestAHealthyDeployOnTheWrongDigestIsNotCalledATimeout(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(config.HealthPolicy, int) (bool, error) {
		if len(engine.deployments) == 1 {
			engine.running["web"] = thirdDigest
		}
		return true, nil
	}

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "web", domain.ResultRolledBack)
	want := fmt.Sprintf("the stack passed its health checks, but the running digest was %s, not %s",
		thirdDigest, newDigest)
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
	if strings.Contains(result.Detail, "timed out") {
		t.Errorf("detail = %q, want no timeout claimed when health passed", result.Detail)
	}
}

func TestAFailedRollbackSaysWhatItsOwnLastCheckSaw(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	ripen(harness, engine, newDigest)
	harness.health.unhealthyStatus = 500
	harness.health.answer = func(config.HealthPolicy, int) (bool, error) {
		return len(engine.deployments) == 0, nil
	}

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "web", domain.ResultRollbackFailed)
	want := `rollback health verification failed: the functional health check timed out: ` +
		`at the last check the health target for service "web" answered 500, which is not an accepted status; ` +
		`the breaker is open`
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
}

func TestADeployTimeoutThatCannotBeProvenSaysWhatTheLastCheckSaw(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	engine.deployErr = func(attempt int) error {
		if attempt == 1 {
			return fmt.Errorf("portainer request failed: %w", context.DeadlineExceeded)
		}
		return nil
	}
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	ripen(harness, engine, newDigest)

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultRolledBack)
	want := `could not be proven: portainer request failed: context deadline exceeded; ` +
		`at the last check the backend reported image status "outdated"`
	if !strings.Contains(result.Detail, want) {
		t.Errorf("detail = %q, want it to say %q", result.Detail, want)
	}
}

func TestAnUnhealthyGitDeploymentSaysWhatTheLastCheckSaw(t *testing.T) {
	engine := gitBackend()
	harness := singleHarness(t, gitStack(), engine)
	ripen(harness, engine, newDigest)
	harness.expect(harness.run(domain.ModeApply), "", domain.ResultProposed)
	engine.compose = proposedCompose
	engine.running["web"] = newDigest
	harness.health.answer = func(config.HealthPolicy, int) (bool, error) { return false, nil }

	harness.run(domain.ModeMonitor)

	want := "media: the deployed proposal failed functional health verification: " +
		"at the last check the health target gave no answer"
	if reason := harness.status().BreakerReason; reason != want {
		t.Errorf("breaker reason = %q, want %q", reason, want)
	}
}

func TestAVerificationFailureNeverRepeatsTheHealthTargetOrItsError(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	stack := singleStack("media", domain.BackendPortainer)
	stack.Health.Target = "http://operator:hunter2@media:8080/health?token=s3cret"
	harness := singleHarness(t, stack, engine)
	ripen(harness, engine, newDigest)
	harness.health.answer = func(policy config.HealthPolicy, _ int) (bool, error) {
		if len(engine.deployments) == 1 {
			return false, fmt.Errorf("get %q: %w", policy.Target, errors.New("connection refused"))
		}
		return true, nil
	}

	report := harness.run(domain.ModeApply)

	result := harness.expect(report, "", domain.ResultRolledBack)
	if !strings.Contains(result.Detail, "at the last check the health target could not be checked") {
		t.Errorf("detail = %q, want it to say the check could not be run", result.Detail)
	}
	published := []string{result.Detail, harness.status().BreakerReason}
	for _, recorded := range harness.events.events {
		published = append(published, recorded.data.Detail, recorded.data.Reason)
	}
	for _, text := range published {
		if strings.Contains(text, "hunter2") || strings.Contains(text, "s3cret") {
			t.Errorf("published %q, want the health target and its error kept out", text)
		}
	}
}
