package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frankieramirez/ripen/internal/app"
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
    expected_services:
      - web
    health:
      target: http://127.0.0.1:9/health
`

const portainerPolicy = `
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
`

func composeApp(t *testing.T) *app.App {
	t.Helper()
	directory := t.TempDir()
	composePath := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(composePath,
		[]byte("services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "policy.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(composePolicy,
		filepath.Join(directory, "state", "ripen.db"), composePath)), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.Close() })
	return loaded
}

func session(t *testing.T, server *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := server.server.Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	connected, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connected.Close() })
	return connected
}

func call(t *testing.T, connected *mcp.ClientSession, name string,
	arguments map[string]any) (*mcp.CallToolResult, response.Envelope) {
	t.Helper()
	result, err := connected.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope response.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("%s did not answer a response envelope: %s", name, encoded)
	}
	return result, envelope
}

func TestReadOnlyRegistersNoWriteToolsAndBuildsNoClients(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "policy.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(portainerPolicy,
		filepath.Join(directory, "state", "ripen.db"),
		filepath.Join(directory, "absent-api-key"),
		strings.Repeat("a", 64))), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close() }()

	readOnly, err := New(Options{App: loaded})
	if err != nil {
		t.Fatalf("a read-only server must not need credentials: %v", err)
	}
	for _, name := range readOnly.Tools() {
		if slices.Contains([]string{"run_monitor_cycle", "create_proposal", "clear_proposal"}, name) {
			t.Errorf("read-only server registered the write tool %q", name)
		}
	}

	if _, err := New(Options{App: loaded, EnableWrites: true, Stream: os.Stderr}); err == nil {
		t.Error("a writes-enabled server must build its clients, and so must fail on a missing key file")
	}
}

func TestApplyModeAndClearBreakerHaveNoTools(t *testing.T) {
	server, err := New(Options{App: composeApp(t), EnableWrites: true, Stream: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}

	tools := server.Tools()

	for _, forbidden := range []string{"clear_breaker", "run_apply_cycle", "apply", "run"} {
		if slices.Contains(tools, forbidden) {
			t.Errorf("tool %q exists; apply mode and clear-breaker are absent by construction", forbidden)
		}
	}
	want := []string{
		"status", "candidates", "audit", "explain",
		"run_monitor_cycle", "create_proposal", "clear_proposal",
	}
	if len(tools) != len(want) {
		t.Errorf("tools = %v, want exactly %v", tools, want)
	}
	for _, name := range want {
		if !slices.Contains(tools, name) {
			t.Errorf("tool %q is missing", name)
		}
	}
}

func TestToolsAreVisibleToAClientAndAnswerTheResponseEnvelope(t *testing.T) {
	server, err := New(Options{App: composeApp(t)})
	if err != nil {
		t.Fatal(err)
	}
	connected := session(t, server)

	listed, err := connected.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 4 {
		t.Errorf("a read-only server listed %d tools, want the four reads", len(listed.Tools))
	}

	result, envelope := call(t, connected, "status", nil)

	if result.IsError {
		t.Errorf("status reported an error: %+v", result.Content)
	}
	if envelope.SchemaVersion != response.SchemaVersion || envelope.Command != "status" || !envelope.OK {
		t.Errorf("envelope = %+v, want the same envelope the CLI prints", envelope)
	}
	if _, err := time.Parse(time.RFC3339, envelope.OccurredAt); err != nil {
		t.Errorf("occurred_at = %q, want RFC 3339", envelope.OccurredAt)
	}
}

func TestAFailureIsAToolErrorCarryingTheEnvelopeNotAProtocolError(t *testing.T) {
	server, err := New(Options{App: composeApp(t)})
	if err != nil {
		t.Fatal(err)
	}
	connected := session(t, server)

	result, envelope := call(t, connected, "explain", map[string]any{"stack": "absent"})

	if !result.IsError {
		t.Error("a failed tool call must set isError so the caller can see and correct it")
	}
	if envelope.OK || envelope.Error == nil {
		t.Fatalf("envelope = %+v, want a failure envelope", envelope)
	}
	if envelope.Error.Code != response.CodeNotFound {
		t.Errorf("code = %q, want not_found", envelope.Error.Code)
	}
}

func TestTheAuditToolTakesTheSameFiltersAsTheCommand(t *testing.T) {
	loaded := composeApp(t)
	key := state.Key{Backend: domain.BackendDockerCompose, Stack: "media"}
	for _, runID := range []string{"run-a", "run-b"} {
		if err := loaded.Store.RecordAttempt(state.Attempt{
			Key: key, RunID: runID, Actor: domain.ActorDaemon, Result: domain.ResultUpdated,
		}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	server, err := New(Options{App: loaded})
	if err != nil {
		t.Fatal(err)
	}
	connected := session(t, server)

	_, envelope := call(t, connected, "audit", map[string]any{"run": "run-a"})

	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	var audit response.Audit
	if err := json.Unmarshal(encoded, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Attempts) != 1 || audit.Attempts[0].RunID != "run-a" {
		t.Errorf("attempts = %+v, want only run-a", audit.Attempts)
	}
}

func TestAWriteThroughMCPIsRecordedAsTheMCPActor(t *testing.T) {
	loaded := composeApp(t)
	server, err := New(Options{App: loaded, EnableWrites: true, Stream: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	connected := session(t, server)

	result, envelope := call(t, connected, "run_monitor_cycle", nil)

	if result.IsError {
		t.Fatalf("the monitor cycle failed: %+v", envelope.Error)
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	var run response.Run
	if err := json.Unmarshal(encoded, &run); err != nil {
		t.Fatal(err)
	}
	if run.Mode != string(domain.ModeMonitor) {
		t.Errorf("mode = %q, want monitor: apply mode is not reachable from here", run.Mode)
	}
	if run.Actor != string(domain.ActorMCP) {
		t.Errorf("actor = %q, want the surface to decide it, not the caller", run.Actor)
	}
}
