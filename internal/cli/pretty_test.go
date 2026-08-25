package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/response"
)

func TestWritePrettyExplainIsAViewOfThePayload(t *testing.T) {
	baseline := "sha256:" + strings.Repeat("a", 64)
	envelope := response.Succeed("explain", at, response.Explain{
		Backend:          "docker-compose",
		Stack:            "media",
		Enabled:          true,
		Mode:             "monitor",
		ExpectedServices: []string{"web"},
		Services: []response.ExplainService{{
			Identity:  response.Identity{Backend: "docker-compose", Stack: "media"},
			Enabled:   true,
			AutoApply: true,
			Health:    response.Health{Type: "http", Target: "http://127.0.0.1:9/health", AcceptedStatus: []int{200}},
			Baseline:  &baseline,
			Blockers:  []string{"the configured mode is monitor"},
		}},
	})

	got := prettyText(t, envelope)

	want := "" +
		"stack: media\n" +
		"backend: docker-compose\n" +
		"enabled: true\n" +
		"excluded: false\n" +
		"mode: monitor\n" +
		"circuit breaker: closed\n" +
		"git path: none\n" +
		"expected services: web\n" +
		"services:\n" +
		"  docker-compose/media:\n" +
		"    enabled: true\n" +
		"    auto apply: true\n" +
		"    health: http\n" +
		"      target: http://127.0.0.1:9/health\n" +
		"      accepted status: 200\n" +
		"    baseline: " + baseline + "\n" +
		"    candidate: none\n" +
		"    pending proposal: none\n" +
		"    blockers:\n" +
		"      - the configured mode is monitor\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWritePrettyStatusIsAViewOfThePayload(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	reason := "media: rollback failed"
	envelope := response.Succeed("status", at, response.Status{
		Breaker: response.Breaker{Open: true, Reason: &reason},
		Lease:   response.Lease{Active: true},
		Notifier: response.NotifierHealth{
			ConsecutiveFailures: 2,
			DroppedSinceStart:   1,
		},
		Services: []response.Service{{
			Identity:  response.Identity{Backend: "docker-compose", Stack: "media"},
			Enabled:   true,
			AutoApply: true,
			Baseline:  &digest,
		}},
		Versions: response.Versions{
			Ripen: "1.0.0", Commit: "abc", BuiltAt: "2026-08-19T09:14:22Z",
			ResponseSchema: 1, EventSchema: 1, StateSchema: 1,
		},
		EffectivePolicy: response.EffectivePolicy{
			Mode:                       "monitor",
			MaxUpdatesPerRun:           1,
			CandidateMinAgeSeconds:     86400,
			VerificationTimeoutSeconds: 300,
			LeaseTTLSeconds:            1800,
			CheckIntervalSeconds:       86400,
			StateFile:                  "/var/lib/ripen/ripen.db",
			Backends:                   []string{"docker-compose"},
			StackCount:                 1,
		},
	})

	got := prettyText(t, envelope)

	want := "" +
		"mode: monitor\n" +
		"circuit breaker: open\n" +
		"  reason: media: rollback failed\n" +
		"lease: active\n" +
		"notifier:\n" +
		"  last success: none\n" +
		"  consecutive failures: 2\n" +
		"  dropped since start: 1\n" +
		"services:\n" +
		"  docker-compose/media:\n" +
		"    enabled: true\n" +
		"    auto apply: true\n" +
		"    baseline: " + digest + "\n" +
		"    candidate: none\n" +
		"    pending proposal: none\n" +
		"    last result: none\n" +
		"effective policy:\n" +
		"  max updates per run: 1\n" +
		"  candidate min age: 86400s\n" +
		"  verification timeout: 300s\n" +
		"  lease ttl: 1800s\n" +
		"  check interval: 86400s\n" +
		"  state file: /var/lib/ripen/ripen.db\n" +
		"  backends: docker-compose\n" +
		"  stacks: 1\n" +
		"  proposals: false\n" +
		"  notifier: false\n" +
		"versions:\n" +
		"  ripen: 1.0.0\n" +
		"  commit: abc\n" +
		"  built: 2026-08-19T09:14:22Z\n" +
		"  response schema: 1\n" +
		"  event schema: 1\n" +
		"  state schema: 1\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWritePrettyCandidatesNoneIsNone(t *testing.T) {
	envelope := response.Succeed("candidates", at, response.Candidates{
		Candidates: []response.Candidate{},
	})

	got := prettyText(t, envelope)

	if got != "candidates: none\n" {
		t.Errorf("got %q", got)
	}
}

func TestWritePrettyNamesAPerServiceIdentity(t *testing.T) {
	service := "web"
	envelope := response.Succeed("candidates", at, response.Candidates{
		Candidates: []response.Candidate{{
			Identity: response.Identity{
				Backend: "docker-compose", Stack: "media", Service: &service,
			},
			Observation: response.Observation{
				Digest: "sha256:abc", FirstSeen: "t0", LastSeen: "t1",
				Observations: 1, MatureAt: "t2",
			},
		}},
	})

	got := prettyText(t, envelope)

	if !strings.Contains(got, "docker-compose/media/web: sha256:abc") {
		t.Errorf("pretty omitted the service name:\n%s", got)
	}
}

func TestWritePrettyAuditIncludesTheCursor(t *testing.T) {
	cursor := "12"
	old := "sha256:" + strings.Repeat("c", 64)
	next := "sha256:" + strings.Repeat("d", 64)
	envelope := response.Succeed("audit", at, response.Audit{
		Attempts: []response.Attempt{{
			Identity:    response.Identity{Backend: "docker-compose", Stack: "media"},
			RunID:       "run-1",
			Actor:       "cli",
			Result:      "updated",
			Detail:      "pinned web",
			OldDigest:   &old,
			NewDigest:   &next,
			AttemptedAt: "2026-08-19T09:14:22Z",
		}},
		NextCursor: &cursor,
	})

	got := prettyText(t, envelope)

	want := "" +
		"attempts:\n" +
		"  docker-compose/media: updated\n" +
		"    attempted at: 2026-08-19T09:14:22Z\n" +
		"    actor: cli\n" +
		"    run id: run-1\n" +
		"    detail: pinned web\n" +
		"    old digest: " + old + "\n" +
		"    new digest: " + next + "\n" +
		"next cursor: 12\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWritePrettyFailureRendersTheError(t *testing.T) {
	envelope := response.Fail("status", at, response.CodeConfigInvalid, "policy.yaml is not readable")

	got := prettyText(t, envelope)

	want := "" +
		"ok: false\n" +
		"command: status\n" +
		"error: config_invalid\n" +
		"  message: policy.yaml is not readable\n" +
		"  retryable: false\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWritePrettyFallsBackToTheEnvelopeForAnUnknownPayload(t *testing.T) {
	envelope := response.Succeed("version", at, response.Version{
		Versions: response.Versions{Ripen: "1.0.0"},
	})

	got := prettyText(t, envelope)

	if !json.Valid([]byte(strings.TrimSpace(got))) {
		t.Fatalf("unknown payload must fall back to the envelope, got %q", got)
	}
	if !strings.Contains(got, `"command":"version"`) {
		t.Errorf("fallback = %s, want the envelope", got)
	}
}

var at = time.Date(2026, 8, 19, 9, 14, 22, 0, time.UTC)

func prettyText(t *testing.T, envelope response.Envelope) string {
	t.Helper()
	var stdout bytes.Buffer
	if err := writePretty(&stdout, envelope); err != nil {
		t.Fatal(err)
	}
	return stdout.String()
}
