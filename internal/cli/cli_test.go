package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
)

const composePolicy = `
mode: monitor
state_file: %s
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: %s
    auto_apply: true
    expected_services:
      - web
    health:
      target: http://127.0.0.1:9/health
`

type invocation struct {
	code     int
	stdout   string
	stderr   string
	envelope response.Envelope
	data     map[string]any
}

// invoke runs one command against a temporary policy and state store.
func invoke(t *testing.T, configPath string, args ...string) invocation {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{args[0], "--config", configPath}, args[1:]...)
	code := Run(full, &stdout, &stderr)

	result := invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
	if strings.TrimSpace(result.stdout) != "" {
		if err := json.Unmarshal(stdout.Bytes(), &result.envelope); err != nil {
			t.Fatalf("stdout is not one JSON envelope (%v): %s", err, result.stdout)
		}
		var raw struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &raw); err == nil {
			result.data = raw.Data
		}
	}
	return result
}

// policyFile writes a compose-backend policy and returns its path plus
// the state database path it names.
func policyFile(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state", "ripen.db")
	composePath := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(composePath,
		[]byte("services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "policy.yaml")
	if err := os.WriteFile(configPath,
		[]byte(fmt.Sprintf(composePolicy, statePath, composePath)), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, statePath
}

func openStore(t *testing.T, path string) *state.Store {
	t.Helper()
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mediaKey() state.Key {
	return state.Key{Backend: domain.BackendDockerCompose, Stack: "media"}
}

func TestEveryAnswerIsOneResponseEnvelopeOnStdout(t *testing.T) {
	configPath, _ := policyFile(t)

	for _, command := range []string{"status", "candidates", "audit", "version", "schema"} {
		result := invoke(t, configPath, command)

		if result.code != ExitOK {
			t.Errorf("%s exited %d (%s)", command, result.code, result.stderr)
		}
		if result.envelope.Command != command {
			t.Errorf("%s answered for command %q", command, result.envelope.Command)
		}
		if result.envelope.SchemaVersion != response.SchemaVersion || !result.envelope.OK {
			t.Errorf("%s envelope = %+v, want a versioned success", command, result.envelope)
		}
		if _, err := time.Parse(time.RFC3339, result.envelope.OccurredAt); err != nil {
			t.Errorf("%s occurred_at = %q, want RFC 3339", command, result.envelope.OccurredAt)
		}
		if strings.Count(strings.TrimSpace(result.stdout), "\n") != 0 {
			t.Errorf("%s wrote more than one line to stdout: %s", command, result.stdout)
		}
		if result.stderr != "" {
			t.Errorf("%s wrote to stderr on success: %s", command, result.stderr)
		}
	}
}

func TestStatusListsAConfiguredServiceBeforeItHasEverBeenObserved(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "status")

	services, ok := result.data["services"].([]any)
	if !ok || len(services) != 1 {
		t.Fatalf("services = %v, want the one configured service", result.data["services"])
	}
	service := services[0].(map[string]any)
	if service["backend"] != "docker-compose" || service["stack"] != "media" {
		t.Errorf("identity = %v, want backend and stack on every read", service)
	}
	if service["service"] != nil {
		t.Errorf("service = %v, want null for a stack-level policy", service["service"])
	}
	if service["baseline"] != nil {
		t.Errorf("baseline = %v, want null before anything is observed", service["baseline"])
	}
	if _, present := service["state_key"]; present {
		t.Error("state_key must never appear on the wire")
	}
	policy, ok := result.data["effective_policy"].(map[string]any)
	if !ok || policy["mode"] != "monitor" {
		t.Errorf("effective_policy = %v, want the mode Ripen is actually running", result.data["effective_policy"])
	}
	if _, ok := result.data["versions"].(map[string]any); !ok {
		t.Error("status must carry the versions block")
	}
}

func TestStatusShowsTheBaselineAndBreakerFromTheStateStore(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	digest := "sha256:" + strings.Repeat("1", 64)
	if err := store.SetAcceptedDigest(mediaKey(), digest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenBreaker("media: rollback failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	result := invoke(t, configPath, "status")

	service := result.data["services"].([]any)[0].(map[string]any)
	if service["baseline"] != digest {
		t.Errorf("baseline = %v, want %q", service["baseline"], digest)
	}
	breaker := result.data["breaker"].(map[string]any)
	if breaker["open"] != true || breaker["reason"] != "media: rollback failed" {
		t.Errorf("breaker = %v, want the open breaker and its reason", breaker)
	}
}

func TestCandidatesReportWhetherTheyHaveMatured(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	digest := "sha256:" + strings.Repeat("2", 64)
	old := time.Now().UTC().Add(-72 * time.Hour)
	if _, err := store.ObserveCandidate(mediaKey(), digest, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveCandidate(mediaKey(), digest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	result := invoke(t, configPath, "candidates")

	candidates := result.data["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v, want one", candidates)
	}
	candidate := candidates[0].(map[string]any)
	if candidate["digest"] != digest || candidate["observations"] != 2.0 {
		t.Errorf("candidate = %v, want two observations of %q", candidate, digest)
	}
	if candidate["mature"] != true {
		t.Errorf("mature = %v, want a twice-seen day-old candidate to be mature", candidate["mature"])
	}
}

func TestAuditPagesNewestFirstWithACursor(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	for index := range 3 {
		if err := store.RecordAttempt(state.Attempt{
			Key: mediaKey(), RunID: fmt.Sprintf("run-%d", index), Actor: domain.ActorCLI,
			Result: domain.ResultUpdated, Detail: fmt.Sprintf("attempt %d", index),
		}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	first := invoke(t, configPath, "audit", "--limit", "2")

	attempts := first.data["attempts"].([]any)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want the page size", len(attempts))
	}
	if attempts[0].(map[string]any)["run_id"] != "run-2" {
		t.Errorf("first attempt = %v, want the newest", attempts[0])
	}
	cursor, ok := first.data["next_cursor"].(string)
	if !ok {
		t.Fatalf("next_cursor = %v, want a cursor while a page remains", first.data["next_cursor"])
	}

	second := invoke(t, configPath, "audit", "--limit", "2", "--cursor", cursor)

	rest := second.data["attempts"].([]any)
	if len(rest) != 1 || rest[0].(map[string]any)["run_id"] != "run-0" {
		t.Errorf("second page = %v, want the last attempt", rest)
	}
	if second.data["next_cursor"] != nil {
		t.Errorf("next_cursor = %v, want null on the last page", second.data["next_cursor"])
	}
}

func TestAuditFiltersByRun(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	for _, runID := range []string{"run-a", "run-b"} {
		if err := store.RecordAttempt(state.Attempt{
			Key: mediaKey(), RunID: runID, Actor: domain.ActorDaemon, Result: domain.ResultUpdated,
		}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	result := invoke(t, configPath, "audit", "--run", "run-a")

	attempts := result.data["attempts"].([]any)
	if len(attempts) != 1 || attempts[0].(map[string]any)["run_id"] != "run-a" {
		t.Errorf("attempts = %v, want only run-a", attempts)
	}
}

func TestExplainNamesEverythingBlockingAnApply(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "explain", "media")

	if result.code != ExitOK {
		t.Fatalf("exit = %d (%s)", result.code, result.stderr)
	}
	services := result.data["services"].([]any)
	blockers := services[0].(map[string]any)["blockers"].([]any)
	var joined []string
	for _, blocker := range blockers {
		joined = append(joined, blocker.(string))
	}
	text := strings.Join(joined, "; ")
	for _, want := range []string{"the configured mode is monitor", "no baseline has been recorded yet"} {
		if !strings.Contains(text, want) {
			t.Errorf("blockers = %q, want it to name %q", text, want)
		}
	}
}

func TestExplainOnAnUnknownStackIsNotFound(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "explain", "nope")

	if result.code != ExitOperation {
		t.Errorf("exit = %d, want %d", result.code, ExitOperation)
	}
	if result.envelope.OK || result.envelope.Error.Code != response.CodeNotFound {
		t.Errorf("envelope = %+v, want a not_found failure", result.envelope.Error)
	}
}

func TestExplainWithoutAStackNameIsAUsageError(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "explain")

	if result.code != ExitUsage {
		t.Errorf("exit = %d, want %d", result.code, ExitUsage)
	}
	if result.envelope.Error.Code != response.CodeUsage || result.envelope.Error.Retryable {
		t.Errorf("error = %+v, want a non-retryable usage error", result.envelope.Error)
	}
}

func TestAnUnknownCommandIsAUsageErrorInTheEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run([]string{"frobnicate"}, &stdout, &stderr)

	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	var envelope response.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not an envelope: %s", stdout.String())
	}
	if envelope.OK || !strings.Contains(envelope.Error.Message, "frobnicate") {
		t.Errorf("envelope = %+v, want it to name the unknown command", envelope.Error)
	}
	if !strings.Contains(stderr.String(), "frobnicate") {
		t.Errorf("stderr = %q, want a human-readable line too", stderr.String())
	}
}

func TestAMissingPolicyFileIsAConfigurationError(t *testing.T) {
	result := invoke(t, filepath.Join(t.TempDir(), "absent.yaml"), "status")

	if result.code != ExitUsage {
		t.Errorf("exit = %d, want %d for a configuration problem", result.code, ExitUsage)
	}
	if result.envelope.Error.Code != response.CodeConfigInvalid {
		t.Errorf("error = %+v, want config_invalid", result.envelope.Error)
	}
}

func TestClearBreakerRequiresAReasonAndThenClosesIt(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	if err := store.OpenBreaker("media: rollback failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	refused := invoke(t, configPath, "clear-breaker")
	if refused.code != ExitUsage || refused.envelope.Error.Code != response.CodeUsage {
		t.Fatalf("clearing without a reason = %d %+v, want a usage error", refused.code, refused.envelope.Error)
	}

	cleared := invoke(t, configPath, "clear-breaker", "--reason", "restored the failed service by hand")
	if cleared.code != ExitOK {
		t.Fatalf("exit = %d (%s)", cleared.code, cleared.stderr)
	}
	breaker := cleared.data["breaker"].(map[string]any)
	if breaker["open"] != false {
		t.Errorf("breaker = %v, want it closed", breaker)
	}

	status := invoke(t, configPath, "status")
	if status.data["breaker"].(map[string]any)["open"] != false {
		t.Error("the breaker must stay closed after the process exits")
	}
}

func TestAnOpenBreakerMakesAnApplyRunAskForAHuman(t *testing.T) {
	configPath, statePath := policyFile(t)
	store := openStore(t, statePath)
	if err := store.OpenBreaker("media: rollback failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	result := invoke(t, configPath, "run", "--mode", "apply")

	if result.code != ExitAttention {
		t.Errorf("exit = %d, want %d — an open breaker needs a person", result.code, ExitAttention)
	}
	if !result.envelope.OK {
		t.Errorf("envelope = %+v, want a successful run report carrying the breaker result", result.envelope)
	}
	results := result.data["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["result"] != "breaker_open" {
		t.Errorf("results = %v, want a single breaker_open result", results)
	}
}

func TestAnInvalidModeIsAUsageError(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "run", "--mode", "sideways")

	if result.code != ExitUsage || result.envelope.Error.Code != response.CodeUsage {
		t.Errorf("result = %d %+v, want a usage error", result.code, result.envelope.Error)
	}
}

func TestRunReportsAnOperationalErrorWithoutATraceback(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "api-key")
	if err := os.WriteFile(keyPath, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "policy.yaml")
	policy := fmt.Sprintf(`
mode: monitor
state_file: %s
portainer:
  base_url: https://127.0.0.1:9443
  api_key_file: %s
  expected_username: ripen
  tls_fingerprint_sha256: %s
stacks:
  media:
    enabled: true
    expected_services:
      - web
    health:
      target: http://127.0.0.1:9/health
`, filepath.Join(directory, "state", "ripen.db"), keyPath, strings.Repeat("a", 64))
	if err := os.WriteFile(configPath, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}

	result := invoke(t, configPath, "run")

	if result.code != ExitOperation {
		t.Fatalf("exit = %d, want %d (stderr: %s)", result.code, ExitOperation, result.stderr)
	}
	if result.envelope.OK || result.envelope.Error == nil {
		t.Fatalf("envelope = %+v, want an ok:false failure envelope on stdout", result.envelope)
	}
	if strings.TrimSpace(result.stderr) == "" {
		t.Error("a failure must also say something readable on stderr")
	}
	for _, trace := range []string{"goroutine", "panic:", ".go:"} {
		if strings.Contains(result.stderr, trace) {
			t.Errorf("stderr = %q, want no traceback", result.stderr)
		}
	}
}

func TestProposingAStackWithoutAGitPathIsRefused(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "propose", "media")

	if result.code != ExitOperation {
		t.Errorf("exit = %d, want %d", result.code, ExitOperation)
	}
	if result.envelope.Error.Code != response.CodePreconditionFailed {
		t.Errorf("error = %+v, want precondition_failed", result.envelope.Error)
	}
}

func TestClearProposalRequiresAStackAndAReason(t *testing.T) {
	configPath, _ := policyFile(t)

	missingReason := invoke(t, configPath, "clear-proposal", "media")
	if missingReason.code != ExitUsage {
		t.Errorf("exit = %d, want a usage error without a reason", missingReason.code)
	}

	nothingPending := invoke(t, configPath, "clear-proposal", "media", "--reason", "reviewed and rejected")
	if nothingPending.code != ExitOperation ||
		nothingPending.envelope.Error.Code != response.CodeNotFound {
		t.Errorf("result = %d %+v, want not_found when nothing is pending",
			nothingPending.code, nothingPending.envelope.Error)
	}
}

func TestTheSchemaVerbPublishesOneSchemaPerCommand(t *testing.T) {
	configPath, _ := policyFile(t)

	result := invoke(t, configPath, "schema")

	schemas, ok := result.data["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v, want a schemas map", result.data)
	}
	for _, command := range response.Commands {
		if _, present := schemas[command]; !present {
			t.Errorf("no schema published for %q", command)
		}
	}
}
