// Package health runs the functional health checks a Transaction
// verifies against. A check answers one question — does the service
// still serve? — and an unreachable service answers it: no.
package health

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/frankieramirez/ripen/internal/config"
)

// Checker runs HTTP health checks.
type Checker struct {
	client *http.Client
}

// Option adjusts a Checker.
type Option func(*Checker)

// WithHTTPClient replaces the underlying HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Checker) { c.client = client }
}

// New builds a Checker.
func New(options ...Option) *Checker {
	checker := &Checker{client: &http.Client{}}
	for _, option := range options {
		option(checker)
	}
	return checker
}

// Check reports whether the target answers with an accepted status. A
// connection failure or timeout is an unhealthy answer, not an error:
// the check did its job. Only a policy Ripen cannot execute at all —
// an unsupported type, an unusable target — is an error.
func (c *Checker) Check(policy config.HealthPolicy) (bool, error) {
	if policy.Type != "" && policy.Type != "http" {
		return false, fmt.Errorf("unsupported health check type %q", policy.Type)
	}
	target, err := url.Parse(policy.Target)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return false, fmt.Errorf("health target %q must be an http or https URL", policy.Target)
	}

	timeout := time.Duration(policy.TimeoutSeconds * float64(time.Second))
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return false, nil
	}
	defer func() { _ = response.Body.Close() }()

	accepted := policy.AcceptedStatus
	if len(accepted) == 0 {
		accepted = []int{http.StatusOK}
	}
	return slices.Contains(accepted, response.StatusCode), nil
}
