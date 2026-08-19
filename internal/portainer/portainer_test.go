package portainer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	digest1 = "sha256:" + strings.Repeat("1", 64)
	digest2 = "sha256:" + strings.Repeat("2", 64)
)

type call struct {
	method  string
	path    string
	headers map[string]string
	body    any
	timeout time.Duration
}

// fakeRequester answers requests from a routing function and records
// every call.
type fakeRequester struct {
	calls  []call
	answer func(method, path string) (int, any)
}

func (f *fakeRequester) request(method, path string, headers map[string]string, body any, timeout time.Duration) (int, any, error) {
	f.calls = append(f.calls, call{method: method, path: path, headers: headers, body: body, timeout: timeout})
	if f.answer == nil {
		return 200, nil, nil
	}
	status, payload := f.answer(method, path)
	return status, payload, nil
}

func writeKey(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func adapterWith(t *testing.T, requester *fakeRequester) *Adapter {
	t.Helper()
	adapter, err := New(Options{
		BaseURL:           "https://portainer:9443",
		APIKeyFile:        writeKey(t, "ptr_key"),
		FingerprintSHA256: strings.Repeat("a", 64),
		UpdateTimeout:     600 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.client = requester
	return adapter
}

func TestMalformedStackEntryIsAnAdapterError(t *testing.T) {
	requester := &fakeRequester{answer: func(string, string) (int, any) {
		return 200, []any{map[string]any{"Id": "not-an-integer"}}
	}}

	_, err := adapterWith(t, requester).ListStacks()

	if err == nil || !strings.Contains(err.Error(), "unexpected Portainer stack entry") {
		t.Errorf("error = %v, want 'unexpected Portainer stack entry'", err)
	}
}

func TestAPIKeyFileToleratesTrailingNewline(t *testing.T) {
	adapter, err := New(Options{
		BaseURL:           "https://portainer:9443",
		APIKeyFile:        writeKey(t, "ptr_key\n"),
		FingerprintSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	requester := &fakeRequester{answer: func(string, string) (int, any) {
		return 200, map[string]any{"Username": "ripen"}
	}}
	adapter.client = requester

	if _, err := adapter.CurrentUsername(); err != nil {
		t.Fatal(err)
	}
	if got := requester.calls[0].headers["X-API-Key"]; got != "ptr_key" {
		t.Errorf("X-API-Key = %q, want the newline stripped", got)
	}
}

func TestAPIKeyFileRejectsInteriorWhitespace(t *testing.T) {
	_, err := New(Options{
		BaseURL:           "https://portainer:9443",
		APIKeyFile:        writeKey(t, "ptr key"),
		FingerprintSHA256: strings.Repeat("a", 64),
	})

	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error = %v, want 'whitespace'", err)
	}
}

func TestStackUpdateUsesTheExtendedDeploymentTimeout(t *testing.T) {
	requester := &fakeRequester{}

	err := adapterWith(t, requester).UpdateStack(
		Stack{ID: 147, EndpointID: 2, Name: "example-app", Status: 1},
		"services: {}", nil, true)
	if err != nil {
		t.Fatal(err)
	}

	if len(requester.calls) != 1 || requester.calls[0].timeout != 600*time.Second {
		t.Errorf("calls = %+v, want one PUT with the 600s update timeout", requester.calls)
	}
	body, ok := requester.calls[0].body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map", requester.calls[0].body)
	}
	if body["RepullImageAndRedeploy"] != true || body["Prune"] != false ||
		body["StackFileContent"] != "services: {}" {
		t.Errorf("body = %+v", body)
	}
}

func TestStackListRecordsGitBacking(t *testing.T) {
	requester := &fakeRequester{answer: func(string, string) (int, any) {
		return 200, []any{map[string]any{
			"Id": 211.0, "EndpointId": 2.0, "Name": "arr", "Status": 1.0,
			"Env":       []any{},
			"GitConfig": map[string]any{"URL": "https://github.com/example/nas.git"},
		}}
	}}

	stacks, err := adapterWith(t, requester).ListStacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || !stacks[0].GitBacked {
		t.Errorf("stacks = %+v, want one git-backed stack", stacks)
	}
}

func TestStackUpdateRefusesGitBackedStack(t *testing.T) {
	requester := &fakeRequester{}

	err := adapterWith(t, requester).UpdateStack(
		Stack{ID: 211, EndpointID: 2, Name: "arr", Status: 1, GitBacked: true},
		"services: {}", nil, false)

	if err == nil || !strings.Contains(err.Error(), "Git-backed stack") {
		t.Errorf("error = %v, want 'Git-backed stack'", err)
	}
	if len(requester.calls) != 0 {
		t.Errorf("calls = %+v, want no HTTP call", requester.calls)
	}
}

func TestRunningServiceDigestsAreScopedToTheAuthorizedComposeProject(t *testing.T) {
	requester := &fakeRequester{answer: func(_, path string) (int, any) {
		switch {
		case strings.Contains(path, "/containers/json?"):
			return 200, []any{
				map[string]any{
					"ImageID": "sha256:radarr-image",
					"Labels": map[string]any{
						"com.docker.compose.project": "arr",
						"com.docker.compose.service": "radarr",
					},
				},
				map[string]any{
					"ImageID": "sha256:sonarr-image",
					"Labels": map[string]any{
						"com.docker.compose.project": "arr",
						"com.docker.compose.service": "sonarr",
					},
				},
			}
		case strings.HasSuffix(path, "/images/sha256%3Aradarr-image/json"):
			return 200, map[string]any{"RepoDigests": []any{"lscr.io/linuxserver/radarr@" + digest1}}
		case strings.HasSuffix(path, "/images/sha256%3Asonarr-image/json"):
			return 200, map[string]any{"RepoDigests": []any{"lscr.io/linuxserver/sonarr@" + digest2}}
		}
		return 404, map[string]any{"message": "unexpected request"}
	}}

	digests, err := adapterWith(t, requester).ServiceImageDigests(Stack{ID: 211, EndpointID: 2, Name: "arr", Status: 1})
	if err != nil {
		t.Fatal(err)
	}

	if digests["radarr"] != digest1 || digests["sonarr"] != digest2 || len(digests) != 2 {
		t.Errorf("digests = %v", digests)
	}
	containerPath := requester.calls[0].path
	if strings.Contains(containerPath, "all=1") {
		t.Error("container listing queried all containers, want running only")
	}
	parsed, err := url.Parse(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	var filters map[string][]string
	if err := json.Unmarshal([]byte(parsed.Query().Get("filters")), &filters); err != nil {
		t.Fatalf("filters do not parse: %v", err)
	}
	if len(filters["label"]) != 1 || filters["label"][0] != "com.docker.compose.project=arr" {
		t.Errorf("label filter = %v", filters["label"])
	}
	if len(filters["status"]) != 1 || filters["status"][0] != "running" {
		t.Errorf("status filter = %v", filters["status"])
	}
}

func TestRunningServiceDigestsRejectAnEmptyProjectResult(t *testing.T) {
	requester := &fakeRequester{answer: func(string, string) (int, any) {
		return 200, []any{}
	}}

	_, err := adapterWith(t, requester).ServiceImageDigests(Stack{ID: 211, EndpointID: 2, Name: "arr", Status: 1})

	if err == nil || !strings.Contains(err.Error(), "no running containers") {
		t.Errorf("error = %v, want 'no running containers'", err)
	}
}

func TestRunningServiceDigestPrefersTheContainersExactDigestPin(t *testing.T) {
	requester := &fakeRequester{answer: func(_, path string) (int, any) {
		switch {
		case strings.Contains(path, "/containers/json?"):
			return 200, []any{map[string]any{
				"Image":   "lscr.io/linuxserver/bazarr:latest@" + digest2,
				"ImageID": "sha256:bazarr-image",
				"Labels": map[string]any{
					"com.docker.compose.project": "arr",
					"com.docker.compose.service": "bazarr",
				},
			}}
		case strings.HasSuffix(path, "/images/sha256%3Abazarr-image/json"):
			return 200, map[string]any{"RepoDigests": []any{
				"lscr.io/linuxserver/bazarr@" + digest1,
				"lscr.io/linuxserver/bazarr@" + digest2,
			}}
		}
		return 404, map[string]any{"message": "unexpected request"}
	}}

	digests, err := adapterWith(t, requester).ServiceImageDigests(Stack{ID: 211, EndpointID: 2, Name: "arr", Status: 1})
	if err != nil {
		t.Fatal(err)
	}

	if len(digests) != 1 || digests["bazarr"] != digest2 {
		t.Errorf("digests = %v, want the container's exact pin %s", digests, digest2)
	}
}

// --- TLS pinning against a real TLS server ---

func tlsTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "ptr_key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Username": "ripen"})
	}))
	t.Cleanup(server.Close)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw))
	return server, fingerprint
}

func TestTLSFingerprintPinningAcceptsThePinnedCertificate(t *testing.T) {
	server, fingerprint := tlsTestServer(t)

	adapter, err := New(Options{
		BaseURL:           server.URL,
		APIKeyFile:        writeKey(t, "ptr_key"),
		FingerprintSHA256: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}

	username, err := adapter.CurrentUsername()
	if err != nil {
		t.Fatalf("CurrentUsername error: %v", err)
	}
	if username != "ripen" {
		t.Errorf("username = %q, want ripen", username)
	}
}

func TestTLSFingerprintMismatchRefusesTheConnection(t *testing.T) {
	server, _ := tlsTestServer(t)

	adapter, err := New(Options{
		BaseURL:           server.URL,
		APIKeyFile:        writeKey(t, "ptr_key"),
		FingerprintSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = adapter.CurrentUsername()

	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("error = %v, want 'fingerprint mismatch'", err)
	}
}
