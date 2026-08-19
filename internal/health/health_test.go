package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/config"
)

func serving(t *testing.T, status int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestAnAcceptedStatusIsHealthy(t *testing.T) {
	policy := config.HealthPolicy{Type: "http", Target: serving(t, http.StatusFound),
		AcceptedStatus: []int{200, 302}, TimeoutSeconds: 2}

	healthy, err := New().Check(policy)
	if err != nil {
		t.Fatal(err)
	}

	if !healthy {
		t.Error("a 302 must be healthy when the policy accepts it")
	}
}

func TestAStatusOutsideThePolicyIsUnhealthy(t *testing.T) {
	policy := config.HealthPolicy{Type: "http", Target: serving(t, http.StatusInternalServerError),
		AcceptedStatus: []int{200}, TimeoutSeconds: 2}

	healthy, err := New().Check(policy)
	if err != nil {
		t.Fatal(err)
	}

	if healthy {
		t.Error("a 500 must be unhealthy")
	}
}

func TestAnUnreachableTargetIsUnhealthyRatherThanAnError(t *testing.T) {
	// A port nothing listens on: the connection is refused immediately.
	policy := config.HealthPolicy{Type: "http", Target: "http://127.0.0.1:1",
		AcceptedStatus: []int{200}, TimeoutSeconds: 2}

	healthy, err := New().Check(policy)

	if err != nil {
		t.Fatalf("error = %v, want an unreachable service to answer the question, not raise", err)
	}
	if healthy {
		t.Error("an unreachable service must be unhealthy")
	}
}

func TestASlowTargetTimesOutAsUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	policy := config.HealthPolicy{Type: "http", Target: server.URL,
		AcceptedStatus: []int{200}, TimeoutSeconds: 0.01}

	healthy, err := New().Check(policy)
	if err != nil {
		t.Fatal(err)
	}

	if healthy {
		t.Error("a check that ran out of time must be unhealthy")
	}
}

func TestAnUnsupportedCheckTypeIsAnError(t *testing.T) {
	policy := config.HealthPolicy{Type: "tcp", Target: "127.0.0.1:9000", TimeoutSeconds: 1}

	_, err := New().Check(policy)

	if err == nil || !strings.Contains(err.Error(), "unsupported health check type") {
		t.Errorf("error = %v, want the unsupported-type error", err)
	}
}

func TestANonHTTPTargetIsAnError(t *testing.T) {
	policy := config.HealthPolicy{Type: "http", Target: "file:///etc/passwd", TimeoutSeconds: 1}

	_, err := New().Check(policy)

	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Errorf("error = %v, want the scheme error", err)
	}
}
