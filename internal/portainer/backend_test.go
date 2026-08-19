package portainer

import (
	"errors"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

const stackCompose = "services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n"

func stackEntry(status int, gitConfig any) map[string]any {
	entry := map[string]any{
		"Id": 150.0, "EndpointId": 2.0, "Name": "media", "Status": float64(status),
		"Env": []any{map[string]any{"name": "TZ", "value": "UTC"}},
	}
	if gitConfig != nil {
		entry["GitConfig"] = gitConfig
	}
	return entry
}

func backendWith(t *testing.T, answer func(method, path string) (int, any)) (*Backend, *fakeRequester) {
	t.Helper()
	requester := &fakeRequester{answer: answer}
	return NewBackend(adapterWith(t, requester), "ripen"), requester
}

func singleServicePolicy() config.StackPolicy {
	health := config.HealthPolicy{Type: "http", Target: "http://media:8080/health"}
	return config.StackPolicy{
		Name: "media", Backend: domain.BackendPortainer, Enabled: true,
		ExpectedServices: []string{"web"}, Health: &health,
	}
}

func TestPreflightRefusesAMismatchedPortainerIdentity(t *testing.T) {
	port, requester := backendWith(t, func(_, path string) (int, any) {
		if strings.HasSuffix(path, "/users/me") {
			return 200, map[string]any{"Username": "admin"}
		}
		return 200, nil
	})

	err := port.Preflight()

	if err == nil || !strings.Contains(err.Error(), `belongs to "admin"`) {
		t.Fatalf("error = %v, want the identity refusal", err)
	}
	if len(requester.calls) != 1 {
		t.Errorf("calls = %d, want the run stopped at the identity check", len(requester.calls))
	}
}

func TestPreflightAcceptsTheExpectedIdentity(t *testing.T) {
	port, _ := backendWith(t, func(string, string) (int, any) {
		return 200, map[string]any{"Username": "ripen"}
	})

	if err := port.Preflight(); err != nil {
		t.Errorf("error = %v, want the configured identity accepted", err)
	}
}

func TestObserveReadsTheStackThroughItsImageStatusForASingleServicePolicy(t *testing.T) {
	port, requester := backendWith(t, func(_, path string) (int, any) {
		switch {
		case strings.HasSuffix(path, "/api/stacks"):
			return 200, []any{stackEntry(1, nil)}
		case strings.HasSuffix(path, "/file"):
			return 200, map[string]any{"StackFileContent": stackCompose}
		case strings.HasSuffix(path, "/images_status"):
			return 200, map[string]any{"Status": "Outdated"}
		}
		return 404, nil
	})

	state, err := port.Observe(singleServicePolicy())
	if err != nil {
		t.Fatal(err)
	}

	if state.ImageStatus != "outdated" {
		t.Errorf("image status = %q, want outdated", state.ImageStatus)
	}
	if state.RunningDigests != nil {
		t.Errorf("running digests = %v, want none for a single-service policy", state.RunningDigests)
	}
	if state.ServiceImages["web"] != "ghcr.io/example/web:1.4.0" {
		t.Errorf("service images = %v, want the image read out of the deployed document", state.ServiceImages)
	}
	for _, call := range requester.calls {
		if strings.Contains(call.path, "/containers/") {
			t.Error("a single-service policy must not pay for container discovery")
		}
	}
}

func TestObserveReadsRunningDigestsForAGitBackedStack(t *testing.T) {
	port, _ := backendWith(t, func(_, path string) (int, any) {
		switch {
		case strings.HasSuffix(path, "/api/stacks"):
			return 200, []any{stackEntry(1, map[string]any{"URL": "https://github.com/x/y"})}
		case strings.HasSuffix(path, "/file"):
			return 200, map[string]any{"StackFileContent": stackCompose}
		case strings.Contains(path, "/containers/json"):
			return 200, []any{map[string]any{
				"ImageID": "sha256:abc",
				"Image":   "ghcr.io/example/web:1.4.0@" + digest1,
				"Labels": map[string]any{
					"com.docker.compose.project": "media",
					"com.docker.compose.service": "web",
				},
			}}
		}
		return 404, nil
	})

	state, err := port.Observe(singleServicePolicy())
	if err != nil {
		t.Fatal(err)
	}

	if !state.GitBacked {
		t.Error("git backing must survive into the observation")
	}
	if state.RunningDigests["web"] != digest1 {
		t.Errorf("running digests = %v, want the digest a git deployment can be confirmed against",
			state.RunningDigests)
	}
}

func TestAStackTheAPIDoesNotListIsNotVisible(t *testing.T) {
	port, _ := backendWith(t, func(_, path string) (int, any) {
		if strings.HasSuffix(path, "/api/stacks") {
			return 200, []any{}
		}
		return 404, nil
	})

	_, err := port.Observe(singleServicePolicy())

	var notVisible *backend.NotVisibleError
	if !errors.As(err, &notVisible) {
		t.Fatalf("error = %v, want a NotVisibleError", err)
	}
}

func TestAnInactiveStackIsIneligible(t *testing.T) {
	port, _ := backendWith(t, func(_, path string) (int, any) {
		if strings.HasSuffix(path, "/api/stacks") {
			return 200, []any{stackEntry(2, nil)}
		}
		return 404, nil
	})

	_, err := port.Observe(singleServicePolicy())

	var ineligible *backend.IneligibleError
	if !errors.As(err, &ineligible) {
		t.Fatalf("error = %v, want an IneligibleError", err)
	}
}

func TestTheFingerprintIsIndependentOfEnvironmentOrder(t *testing.T) {
	forward := []EnvVar{{Name: "TZ", Value: "UTC"}, {Name: "PUID", Value: "1000"}}
	reversed := []EnvVar{{Name: "PUID", Value: "1000"}, {Name: "TZ", Value: "UTC"}}

	if fingerprint(stackCompose, forward) != fingerprint(stackCompose, reversed) {
		t.Error("reordering the environment is not drift")
	}
	changed := []EnvVar{{Name: "TZ", Value: "Europe/Madrid"}, {Name: "PUID", Value: "1000"}}
	if fingerprint(stackCompose, forward) == fingerprint(stackCompose, changed) {
		t.Error("changing an environment value is drift")
	}
	if fingerprint(stackCompose, forward) == fingerprint(stackCompose+"# edited\n", forward) {
		t.Error("changing the compose document is drift")
	}
}
