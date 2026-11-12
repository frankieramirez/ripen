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
	ctx    context.Context
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
	checker := &Checker{ctx: context.Background(), client: &http.Client{}}
	for _, option := range options {
		option(checker)
	}
	return checker
}

// Answer is what one check saw. Status is the HTTP status the target
// answered with, and zero when it gave no answer at all.
type Answer struct {
	Healthy bool
	Status  int
}

// Check reports whether the target answers with an accepted status. A
// connection failure or timeout is an unhealthy answer, not an error:
// the check did its job. Only a policy Ripen cannot execute at all —
// an unsupported type, an unusable target — is an error.
func (c *Checker) Check(policy config.HealthPolicy) (Answer, error) {
	if policy.Type != "" && policy.Type != "http" {
		return Answer{}, fmt.Errorf("unsupported health check type %q", policy.Type)
	}
	target, err := url.Parse(policy.Target)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return Answer{}, fmt.Errorf("health target %q must be an http or https URL", policy.Target)
	}

	timeout := time.Duration(policy.TimeoutSeconds * float64(time.Second))
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Answer{}, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		if c.ctx.Err() != nil {
			return Answer{}, c.ctx.Err()
		}
		return Answer{}, nil
	}
	defer func() { _ = response.Body.Close() }()

	accepted := policy.AcceptedStatus
	if len(accepted) == 0 {
		accepted = []int{http.StatusOK}
	}
	return Answer{Healthy: slices.Contains(accepted, response.StatusCode), Status: response.StatusCode}, nil
}

// WithContext returns a copy bound to the caller lifetime.
func (c *Checker) WithContext(ctx context.Context) *Checker {
	clone := *c
	clone.ctx = ctx
	return &clone
}
