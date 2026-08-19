package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/app"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
)

const policy = `
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

func loadedApp(t *testing.T) *app.App {
	t.Helper()
	directory := t.TempDir()
	composePath := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(composePath,
		[]byte("services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "policy.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(policy,
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

func serve(t *testing.T, loaded *app.App, token string) *httptest.Server {
	t.Helper()
	ui, err := New(Options{App: loaded, Address: "127.0.0.1:0", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ui.Handler())
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, server *httptest.Server, path, token string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	answer, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = answer.Body.Close() })
	body := make([]byte, 64<<10)
	read, _ := answer.Body.Read(body)
	return answer, string(body[:read])
}

func TestAnExposedBindWithoutATokenRefusesToStart(t *testing.T) {
	loaded := loadedApp(t)

	for _, address := range []string{"0.0.0.0:7476", "192.168.1.10:7476", ":7476"} {
		if _, err := New(Options{App: loaded, Address: address}); err == nil {
			t.Errorf("address %q started without a token; there is no insecure escape hatch", address)
		}
	}
	if _, err := New(Options{App: loaded, Address: "0.0.0.0:7476", Token: "hunter2"}); err != nil {
		t.Errorf("error = %v, want an exposed bind accepted with a token", err)
	}
}

func TestALoopbackBindNeedsNoToken(t *testing.T) {
	loaded := loadedApp(t)

	for _, address := range []string{"127.0.0.1:7476", "localhost:7476", "[::1]:7476"} {
		if _, err := New(Options{App: loaded, Address: address}); err != nil {
			t.Errorf("address %q was refused: %v", address, err)
		}
	}
}

// TestHealthzIsUnauthenticatedAndInformationFree keeps the one open
// route open and empty: a container healthcheck needs it, and nothing
// else should learn anything from it.
func TestHealthzIsUnauthenticatedAndInformationFree(t *testing.T) {
	loaded := loadedApp(t)
	if err := loaded.Store.OpenBreaker("media: rollback failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	server := serve(t, loaded, "hunter2")

	answer, body := get(t, server, "/healthz", "")

	if answer.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 without a token", answer.StatusCode)
	}
	if strings.TrimSpace(body) != "ok" {
		t.Errorf("body = %q, want nothing but ok", body)
	}
	for _, leak := range []string{"media", "rollback", "breaker", "docker-compose"} {
		if strings.Contains(body, leak) {
			t.Errorf("healthz leaked %q", leak)
		}
	}
}

func TestEveryOtherRouteNeedsTheToken(t *testing.T) {
	server := serve(t, loadedApp(t), "hunter2")

	for _, path := range []string{"/", "/audit", "/policy", "/static/style.css"} {
		answer, _ := get(t, server, path, "")
		if answer.StatusCode != http.StatusSeeOther {
			t.Errorf("%s without a token = %d, want a redirect to sign in", path, answer.StatusCode)
		}
		if answer, _ = get(t, server, path, "hunter2"); answer.StatusCode != http.StatusOK {
			t.Errorf("%s with the token = %d, want 200", path, answer.StatusCode)
		}
	}

	answer, _ := get(t, server, "/api/status", "")
	if answer.StatusCode != http.StatusUnauthorized {
		t.Errorf("the api without a token = %d, want 401", answer.StatusCode)
	}
	if answer.Header.Get("WWW-Authenticate") == "" {
		t.Error("a 401 must say what it wants")
	}
	if answer, _ = get(t, server, "/api/status", "wrong"); answer.StatusCode != http.StatusUnauthorized {
		t.Errorf("the api with the wrong token = %d, want 401", answer.StatusCode)
	}
}

func TestTheOverviewListsEveryConfiguredService(t *testing.T) {
	loaded := loadedApp(t)
	digest := "sha256:" + strings.Repeat("1", 64)
	if err := loaded.Store.SetAcceptedDigest(
		state.Key{Backend: domain.BackendDockerCompose, Stack: "media"}, digest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	server := serve(t, loaded, "")

	answer, body := get(t, server, "/", "")

	if answer.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", answer.StatusCode)
	}
	for _, want := range []string{"media", "docker-compose", "111111111111", "read-only"} {
		if !strings.Contains(body, want) {
			t.Errorf("the overview does not mention %q", want)
		}
	}
}

// TestTheBreakerBannerOffersTheCommandNotAButton is the design decision
// made visible: an open breaker is cleared deliberately, at a terminal,
// with a reason — never by clicking something on a dashboard.
func TestTheBreakerBannerOffersTheCommandNotAButton(t *testing.T) {
	loaded := loadedApp(t)
	if err := loaded.Store.OpenBreaker("media/web: rollback failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	server := serve(t, loaded, "")

	_, body := get(t, server, "/", "")

	if !strings.Contains(body, "ripen clear-breaker --reason") {
		t.Error("the banner does not show the command to copy")
	}
	if !strings.Contains(body, "media/web: rollback failed") {
		t.Error("the banner does not show why the breaker opened")
	}
	if strings.Contains(body, "<form") || strings.Contains(body, "<button") {
		t.Error("the breaker banner must not offer anything clickable")
	}
}

func TestTheInternalAPIAnswersTheResponseEnvelope(t *testing.T) {
	server := serve(t, loadedApp(t), "")

	answer, body := get(t, server, "/api/status", "")

	if answer.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", answer.StatusCode)
	}
	var envelope response.Envelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("the api did not answer an envelope: %s", body)
	}
	if !envelope.OK || envelope.Command != "status" || envelope.SchemaVersion != response.SchemaVersion {
		t.Errorf("envelope = %+v, want the same envelope every surface answers", envelope)
	}
}

// TestNothingCanBeWritten is the read-only guarantee: no route accepts a
// method that could change anything.
func TestNothingCanBeWritten(t *testing.T) {
	server := serve(t, loadedApp(t), "")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, path := range []string{"/", "/audit", "/policy", "/api/status", "/api/audit", "/healthz"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			request, err := http.NewRequest(method, server.URL+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			answer, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = answer.Body.Close()
			if answer.StatusCode != http.StatusMethodNotAllowed && answer.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want it refused: this interface only reads",
					method, path, answer.StatusCode)
			}
		}
	}
}

func TestSigningInSetsACookieSoABrowserCanNavigate(t *testing.T) {
	server := serve(t, loadedApp(t), "hunter2")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	refused, err := client.PostForm(server.URL+"/sign-in", map[string][]string{"token": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = refused.Body.Close()
	if refused.StatusCode != http.StatusUnauthorized {
		t.Errorf("the wrong token = %d, want 401", refused.StatusCode)
	}

	accepted, err := client.PostForm(server.URL+"/sign-in", map[string][]string{"token": {"hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Body.Close()
	if accepted.StatusCode != http.StatusSeeOther {
		t.Fatalf("the right token = %d, want a redirect to the overview", accepted.StatusCode)
	}
	cookies := accepted.Cookies()
	if len(cookies) != 1 || cookies[0].Name != tokenCookie {
		t.Fatalf("cookies = %+v, want the session cookie", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie = %+v, want it http-only and same-site strict", cookies[0])
	}
}

func TestTheTokenComesFromTheEnvironmentOrAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-token")
	if err := os.WriteFile(path, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	token, err := ReadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if token != "from-the-file" {
		t.Errorf("token = %q, want the file's contents trimmed", token)
	}

	t.Setenv("RIPEN_UI_TOKEN", "from-the-environment")
	if token, err = ReadToken(path); err != nil {
		t.Fatal(err)
	}
	if token != "from-the-environment" {
		t.Errorf("token = %q, want the environment to win", token)
	}
}
