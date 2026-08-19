package updater

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/domain"
)

func TestMonitorBaselinesTheProvenRunningDigestWithoutRedeploying(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, singleCompose)
	engine.running = map[string]string{"web": baseDigest}
	harness := singleHarness(t, singleStack("media", domain.BackendDockerCompose), engine)

	report := harness.run(domain.ModeMonitor)

	result := harness.expect(report, "", domain.ResultBaselined)
	if result.Digest != baseDigest {
		t.Errorf("baseline = %q, want the running digest %q", result.Digest, baseDigest)
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "")); got != baseDigest {
		t.Errorf("accepted digest = %q, want %q", got, baseDigest)
	}
	if len(engine.deployments) != 0 {
		t.Errorf("deployments = %d, want none: monitor never redeploys", len(engine.deployments))
	}
}

func TestMonitorBaselinesTheRegistryDigestWhenTheBackendReportsUpToDate(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)

	report := harness.run(domain.ModeMonitor)

	result := harness.expect(report, "", domain.ResultBaselined)
	if result.Digest != baseDigest {
		t.Errorf("baseline = %q, want the registry digest %q", result.Digest, baseDigest)
	}
	if len(engine.deployments) != 0 {
		t.Errorf("deployments = %d, want none", len(engine.deployments))
	}
}

func TestMonitorBaselinesEachServiceOfAMultiServiceStackIndependently(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)

	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "web", domain.ResultBaselined)
	harness.expect(report, "sidecar", domain.ResultBaselined)
	if got := harness.accepted(key(domain.BackendDockerCompose, "web")); got != baseDigest {
		t.Errorf("web baseline = %q, want %q", got, baseDigest)
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "sidecar")); got != sidecarDigest {
		t.Errorf("sidecar baseline = %q, want %q", got, sidecarDigest)
	}
	if len(engine.deployments) != 0 {
		t.Errorf("deployments = %d, want none", len(engine.deployments))
	}
}

func TestAHealthOnlyServiceIsNeverResolvedOrBaselined(t *testing.T) {
	stack := multiStack()
	stack.Services[1].Enabled = false
	stack.Services[1].AutoApply = false
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, stack, engine)

	report := harness.run(domain.ModeMonitor)

	if len(report.Results) != 1 {
		t.Fatalf("results = %+v, want only the managed service", report.Results)
	}
	harness.expect(report, "web", domain.ResultBaselined)
	if got := harness.accepted(key(domain.BackendDockerCompose, "sidecar")); got != "" {
		t.Errorf("sidecar baseline = %q, want a health-only service to stay unbaselined", got)
	}
	if slices.Contains(harness.registry.lookups, sidecarImage) {
		t.Error("a health-only service must never be resolved against the registry")
	}
}

func TestMonitorRefusesToBaselineWhenAnUpdateIsAlreadyPending(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "outdated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)

	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "", domain.ResultBaselineBlocked)
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != "" {
		t.Errorf("accepted digest = %q, want nothing baselined from an unprovable state", got)
	}
}

func TestMonitorReportsANewRegistryDigestAsACandidateWithoutRedeploying(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	harness.run(domain.ModeMonitor)

	harness.registry.digests[webImage] = newDigest
	engine.imageStatus = "outdated"
	report := harness.run(domain.ModeMonitor)

	result := harness.expect(report, "", domain.ResultCandidate)
	if result.Digest != newDigest {
		t.Errorf("candidate digest = %q, want %q", result.Digest, newDigest)
	}
	if got := harness.accepted(key(domain.BackendPortainer, "")); got != baseDigest {
		t.Errorf("accepted digest = %q, want the baseline untouched", got)
	}
	if len(engine.deployments) != 0 {
		t.Errorf("deployments = %d, want none", len(engine.deployments))
	}
}

func TestAStackTheBackendCannotSeeIsReportedNotVisible(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.observeErr = backend.NotVisible("media")
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)

	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "", domain.ResultNotVisible)
}

func TestAnUnusableEngineTakesOnlyItsOwnStacksOutOfTheRun(t *testing.T) {
	portainerEngine := newBackend(domain.BackendPortainer, singleCompose)
	portainerEngine.imageStatus = "updated"
	composeEngine := newBackend(domain.BackendDockerCompose, singleCompose)
	composeEngine.preflightErr = backend.EngineUnavailable("docker-compose", errors.New("no such binary"))
	policy := policyFor(
		singleStack("media", domain.BackendPortainer),
		singleStack("arr", domain.BackendDockerCompose),
	)
	harness := newHarness(t, policy, map[domain.Backend]*fakeBackend{
		domain.BackendPortainer:     portainerEngine,
		domain.BackendDockerCompose: composeEngine,
	})

	report := harness.run(domain.ModeMonitor)

	if len(report.Results) != 2 {
		t.Fatalf("results = %+v, want one per stack", report.Results)
	}
	for _, result := range report.Results {
		switch result.Key.Stack {
		case "media":
			if result.Code != domain.ResultBaselined {
				t.Errorf("media = %s, want a healthy backend to carry on", result.Code)
			}
		case "arr":
			if result.Code != domain.ResultEngineUnavailable {
				t.Errorf("arr = %s, want engine_unavailable", result.Code)
			}
		}
	}
}

func TestAWrongBackendIdentityFailsTheRunBeforeAnyInventoryWork(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.preflightErr = errors.New("the Portainer API key belongs to \"admin\", expected \"ripen\"")
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)

	_, err := harness.updater.Run(domain.ModeMonitor)

	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("error = %v, want the identity refusal", err)
	}
	if engine.observations != 0 {
		t.Errorf("observations = %d, want no inventory work under the wrong identity", engine.observations)
	}
}

func TestASecondRunIsBusyWhileTheLeaseIsHeld(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	if _, acquired, err := harness.store.AcquireLease(harness.clock.now, 1800); err != nil || !acquired {
		t.Fatalf("could not hold the lease: acquired=%v err=%v", acquired, err)
	}

	report := harness.run(domain.ModeMonitor)

	if len(report.Results) != 1 || report.Results[0].Code != domain.ResultBusy {
		t.Errorf("results = %+v, want a single busy result", report.Results)
	}
	if engine.observations != 0 {
		t.Error("a busy run must not observe anything")
	}
}

func TestAFailingEventSinkNeverChangesARunResult(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, singleCompose)
	engine.running = map[string]string{"web": baseDigest}
	harness := singleHarness(t, singleStack("media", domain.BackendDockerCompose), engine)
	harness.events.panics = true

	report := harness.run(domain.ModeMonitor)

	harness.expect(report, "", domain.ResultBaselined)
	if got := harness.accepted(key(domain.BackendDockerCompose, "")); got != baseDigest {
		t.Errorf("accepted digest = %q, want the baseline written despite the sink", got)
	}
}

func TestAnOpenBreakerBlocksApplyButNotMonitor(t *testing.T) {
	engine := newBackend(domain.BackendDockerCompose, singleCompose)
	engine.running = map[string]string{"web": baseDigest}
	harness := singleHarness(t, singleStack("media", domain.BackendDockerCompose), engine)
	if err := harness.store.OpenBreaker("a rollback failed on media/web", harness.clock.now); err != nil {
		t.Fatal(err)
	}

	applied := harness.run(domain.ModeApply)
	if len(applied.Results) != 1 || applied.Results[0].Code != domain.ResultBreakerOpen {
		t.Fatalf("apply results = %+v, want a single breaker_open result", applied.Results)
	}
	if !strings.Contains(applied.Results[0].Detail, "rollback failed") {
		t.Errorf("detail = %q, want the recorded breaker reason", applied.Results[0].Detail)
	}
	if engine.observations != 0 {
		t.Error("a blocked apply must not observe anything")
	}

	monitored := harness.run(domain.ModeMonitor)
	harness.expect(monitored, "", domain.ResultBaselined)
}
