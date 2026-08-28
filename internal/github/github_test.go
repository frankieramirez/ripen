package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/proposal"
)

const (
	liveCompose     = "services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n"
	proposedCompose = "services:\n  web:\n    image: \"ghcr.io/example/web:1.4.0@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111\"\n"
	digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

type call struct {
	method string
	path   string
	body   map[string]any
}

type fakeForge struct {
	calls   []call
	routes  map[string]any
	missing map[string]bool
}

func (f *fakeForge) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorded := call{method: request.Method, path: request.URL.RequestURI()}
		if request.Body != nil {
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err == nil {
				recorded.body = body
			}
		}
		f.calls = append(f.calls, recorded)

		key := request.Method + " " + request.URL.Path
		if request.URL.RawQuery != "" {
			key += "?" + request.URL.RawQuery
		}
		if f.missing[key] {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		payload, ok := f.routes[key]
		if !ok {
			t.Errorf("unrouted request %s", key)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(payload)
	})
}

func (f *fakeForge) called(method string) bool {
	for _, recorded := range f.calls {
		if recorded.method == method {
			return true
		}
	}
	return false
}

func contentsPayload(content, sha string) map[string]any {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	var wrapped strings.Builder
	for index, character := range encoded {
		if index > 0 && index%20 == 0 {
			wrapped.WriteString("\n")
		}
		wrapped.WriteRune(character)
	}
	return map[string]any{"content": wrapped.String(), "sha": sha}
}

func writeToken(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("ghp_token\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func adapterFor(t *testing.T, forge *fakeForge) *Adapter {
	t.Helper()
	server := httptest.NewServer(forge.handler(t))
	t.Cleanup(server.Close)
	adapter, err := New(Options{
		Repository: "frankieramirez/nas-infrastructure",
		BaseBranch: "main",
		TokenFile:  writeToken(t, 0o600),
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func change() proposal.Change {
	return proposal.Change{
		Label:           "media/web",
		RepositoryPath:  "stacks/media/compose.yaml",
		ExpectedContent: liveCompose,
		ProposedContent: proposedCompose,
		Digest:          digest,
	}
}

const (
	repo         = "/repos/frankieramirez/nas-infrastructure"
	contentsPath = repo + "/contents/stacks/media/compose.yaml"
	branchRef    = repo + "/git/ref/heads/ripen/media-web-111111111111"
)

func TestProposingCreatesABranchAndADigestPinPullRequest(t *testing.T) {
	forge := &fakeForge{
		routes: map[string]any{
			"GET " + contentsPath + "?ref=main":   contentsPayload(liveCompose, "blob-sha"),
			"GET " + repo + "/git/ref/heads/main": map[string]any{"object": map[string]any{"sha": "base-sha"}},
			"POST " + repo + "/git/refs":          map[string]any{"ref": "refs/heads/ripen/media-web-111111111111"},
			"PUT " + contentsPath:                 map[string]any{"commit": map[string]any{"sha": "commit-sha"}},
			"GET " + repo + "/pulls?base=main&head=frankieramirez%3Aripen%2Fmedia-web-111111111111&state=open": []any{},
			"POST " + repo + "/pulls": map[string]any{
				"html_url": "https://github.com/frankieramirez/nas-infrastructure/pull/7"},
		},
		missing: map[string]bool{"GET " + branchRef: true},
	}

	result, err := adapterFor(t, forge).Propose(change())
	if err != nil {
		t.Fatal(err)
	}

	if !result.Created || result.URL != "https://github.com/frankieramirez/nas-infrastructure/pull/7" {
		t.Errorf("result = %+v, want a created pull request URL", result)
	}
	var put *call
	for index, recorded := range forge.calls {
		if recorded.method == http.MethodPut {
			put = &forge.calls[index]
		}
	}
	if put == nil {
		t.Fatal("the proposal never wrote the file")
	}
	if put.body["sha"] != "blob-sha" {
		t.Errorf("PUT sha = %v, want the base file's blob sha", put.body["sha"])
	}
	written, err := base64.StdEncoding.DecodeString(put.body["content"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != proposedCompose {
		t.Errorf("written content = %q, want the pinned document", written)
	}
	if put.body["branch"] != "ripen/media-web-111111111111" {
		t.Errorf("PUT branch = %v, want the deterministic proposal branch", put.body["branch"])
	}
}

func TestProposingIsIdempotentWhenTheBranchAndPullRequestExist(t *testing.T) {
	forge := &fakeForge{
		routes: map[string]any{
			"GET " + contentsPath + "?ref=main":                           contentsPayload(liveCompose, "blob-sha"),
			"GET " + branchRef:                                            map[string]any{"object": map[string]any{"sha": "branch-sha"}},
			"GET " + contentsPath + "?ref=ripen%2Fmedia-web-111111111111": contentsPayload(proposedCompose, "branch-blob"),
			"GET " + repo + "/pulls?base=main&head=frankieramirez%3Aripen%2Fmedia-web-111111111111&state=open": []any{
				map[string]any{"html_url": "https://github.com/frankieramirez/nas-infrastructure/pull/7"},
			},
		},
	}

	result, err := adapterFor(t, forge).Propose(change())
	if err != nil {
		t.Fatal(err)
	}

	if result.Created {
		t.Error("created = true, want false for an existing proposal")
	}
	if result.URL != "https://github.com/frankieramirez/nas-infrastructure/pull/7" {
		t.Errorf("url = %q, want the existing pull request", result.URL)
	}
	if forge.called(http.MethodPost) || forge.called(http.MethodPut) {
		t.Error("an existing proposal must not be written or opened again")
	}
}

func TestProposingRefusesWhenTheRepositorySourceHasDrifted(t *testing.T) {
	forge := &fakeForge{
		routes: map[string]any{
			"GET " + contentsPath + "?ref=main": contentsPayload(
				"services:\n  web:\n    image: ghcr.io/example/web:9.9.9\n", "blob-sha"),
		},
	}

	_, err := adapterFor(t, forge).Propose(change())

	if err == nil || !strings.Contains(err.Error(), "differs from the live") {
		t.Errorf("error = %v, want the source-drift refusal", err)
	}
	if forge.called(http.MethodPut) || forge.called(http.MethodPost) {
		t.Error("a drifted source must not be written to")
	}
}

func TestATokenFileReadableByOthersIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}

	_, err := New(Options{
		Repository: "frankieramirez/nas-infrastructure",
		BaseBranch: "main",
		TokenFile:  writeToken(t, 0o644),
	})

	if err == nil || !strings.Contains(err.Error(), "group or others") {
		t.Errorf("error = %v, want the broad-permissions refusal", err)
	}
}

func TestAnUnexpectedChangeOnTheProposalBranchIsRefused(t *testing.T) {
	forge := &fakeForge{
		routes: map[string]any{
			"GET " + contentsPath + "?ref=main": contentsPayload(liveCompose, "blob-sha"),
			"GET " + branchRef:                  map[string]any{"object": map[string]any{"sha": "branch-sha"}},
			"GET " + contentsPath + "?ref=ripen%2Fmedia-web-111111111111": contentsPayload(
				"services:\n  web:\n    image: someone-elses-edit\n", "branch-blob"),
		},
	}

	_, err := adapterFor(t, forge).Propose(change())

	if err == nil || !strings.Contains(err.Error(), "unexpected change") {
		t.Errorf("error = %v, want the unexpected-branch-content refusal", err)
	}
}

func TestARepositoryPathOutsideTheRepositoryIsRefused(t *testing.T) {
	forge := &fakeForge{routes: map[string]any{}}
	adapter := adapterFor(t, forge)

	for _, path := range []string{"/etc/compose.yaml", "../compose.yaml", "stacks/media/compose.json"} {
		proposed := change()
		proposed.RepositoryPath = path
		if _, err := adapter.Propose(proposed); err == nil {
			t.Errorf("path %q was accepted, want a relative YAML path refusal", path)
		}
	}
	if len(forge.calls) != 0 {
		t.Error("a refused path must not reach the forge")
	}
}
