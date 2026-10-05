package updater

import (
	"errors"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
)

func driftedMultiServiceHarness(t *testing.T) (*harness, *fakeBackend) {
	t.Helper()
	engine := newBackend(domain.BackendDockerCompose, multiCompose)
	engine.running = map[string]string{"web": baseDigest, "sidecar": sidecarDigest}
	harness := singleHarness(t, multiStack(), engine)
	harness.run(domain.ModeMonitor)
	engine.running["web"] = newDigest
	harness.registry.digests[webImage] = newDigest
	harness.expect(harness.run(domain.ModeMonitor), "web", domain.ResultDrifted)
	return harness, engine
}

func TestRebaselineAcceptsTheProvenRunningDigestOfADriftedService(t *testing.T) {
	harness, engine := driftedMultiServiceHarness(t)

	result, err := harness.updater.Rebaseline("media", "web", "pinned by hand in a reviewed pull request")

	if err != nil {
		t.Fatal(err)
	}
	if result.Code != domain.ResultBaselined || result.Digest != newDigest {
		t.Fatalf("result = %s %q, want baselined %q", result.Code, result.Digest, newDigest)
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "web")); got != newDigest {
		t.Errorf("accepted digest = %q, want the running digest %q", got, newDigest)
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "sidecar")); got != sidecarDigest {
		t.Errorf("sibling baseline = %q, want it untouched", got)
	}
	if len(engine.deployments) != 0 {
		t.Errorf("deployments = %d, want none: rebaselining never redeploys", len(engine.deployments))
	}
	attempts, err := harness.store.Attempts(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].OldDigest != baseDigest || attempts[0].NewDigest != newDigest ||
		attempts[0].Actor != domain.ActorCLI || !strings.Contains(attempts[0].Detail, "pinned by hand") {
		t.Errorf("audit = %+v, want the transition, the actor, and the reason recorded", attempts)
	}
	if !harness.events.saw(event.BaselineRecorded) {
		t.Error("a rebaseline must reach the event stream")
	}
	harness.expect(harness.run(domain.ModeMonitor), "web", domain.ResultUpToDate)
}

func TestRebaselineRefusesAnUnhealthyStackAndKeepsTheBaseline(t *testing.T) {
	harness, _ := driftedMultiServiceHarness(t)
	harness.health.answer = func(config.HealthPolicy, int) (bool, error) { return false, nil }

	_, err := harness.updater.Rebaseline("media", "web", "pinned by hand")

	if err == nil {
		t.Fatal("rebaselining an unhealthy stack must be refused")
	}
	if got := harness.accepted(key(domain.BackendDockerCompose, "web")); got != baseDigest {
		t.Errorf("accepted digest = %q, want the baseline kept", got)
	}
}

func TestRebaselineRefusesWhatItCannotProveOrWasNotAskedToDecide(t *testing.T) {
	cases := []struct {
		name    string
		stack   string
		service string
		reason  string
		arrange func(*harness, *fakeBackend)
	}{
		{name: "blank reason", stack: "media", service: "web", reason: " "},
		{name: "service missing on a multi-service stack", stack: "media", reason: "why"},
		{name: "service not in the policy", stack: "media", service: "worker", reason: "why"},
		{name: "health-only service", stack: "media", service: "sidecar", reason: "why",
			arrange: func(h *harness, _ *fakeBackend) { h.policy.Stacks[0].Services[1].Enabled = false }},
		{name: "a sibling is not running", stack: "media", service: "web", reason: "why",
			arrange: func(_ *harness, f *fakeBackend) { delete(f.running, "sidecar") }},
		{name: "a proposal is pending review", stack: "media", service: "web", reason: "why",
			arrange: func(h *harness, _ *fakeBackend) {
				if err := h.store.SetPendingProposal(key(domain.BackendDockerCompose, "web"), newDigest,
					"https://github.com/x/y/pull/1", h.clock.now); err != nil {
					h.t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness, engine := driftedMultiServiceHarness(t)
			if tc.arrange != nil {
				tc.arrange(harness, engine)
			}

			_, err := harness.updater.Rebaseline(tc.stack, tc.service, tc.reason)

			if err == nil {
				t.Fatal("want a refusal")
			}
			if got := harness.accepted(key(domain.BackendDockerCompose, "web")); got != baseDigest {
				t.Errorf("accepted digest = %q, want the baseline kept", got)
			}
		})
	}
}

func TestRebaselineRefusesADigestTheBackendCannotProve(t *testing.T) {
	engine := newBackend(domain.BackendPortainer, singleCompose)
	engine.imageStatus = "updated"
	harness := singleHarness(t, singleStack("media", domain.BackendPortainer), engine)
	harness.run(domain.ModeMonitor)

	_, err := harness.updater.Rebaseline("media", "", "why")

	if err == nil || !strings.Contains(err.Error(), "cannot be proven") {
		t.Fatalf("err = %v, want a refusal naming the unprovable digest", err)
	}
}

func TestRebaselineOfAnUnknownStackIsReportedAsUnknown(t *testing.T) {
	harness, _ := driftedMultiServiceHarness(t)

	_, err := harness.updater.Rebaseline("nope", "", "why")

	if !errors.Is(err, ErrUnknownStack) {
		t.Errorf("err = %v, want ErrUnknownStack", err)
	}
}
