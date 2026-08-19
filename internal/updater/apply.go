package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/composefile"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/proposal"
)

// maximumPollInterval bounds how long verification waits between checks.
const maximumPollInterval = 10 * time.Second

// apply is the mutating half of the Transaction. Everything before the
// deploy is a chance to refuse; everything after it is verification, and
// verification that fails rolls back and opens the breaker.
func (t *transaction) apply(observed observation, accepted string) (Result, bool) {
	fresh, err := t.port().Observe(t.stack)
	if err != nil {
		var notVisible *backend.NotVisibleError
		if errors.As(err, &notVisible) {
			return Result{
				Key:    observed.key,
				Code:   domain.ResultDrifted,
				Detail: "the stack disappeared before apply",
			}, false
		}
		return t.failure(observed.key, err), false
	}
	if fresh.Fingerprint != observed.stack.Fingerprint || fresh.GitBacked != observed.stack.GitBacked {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultDrifted,
			Detail: "the stack changed between planning and applying",
		}, false
	}
	if fresh.GitBacked {
		return t.propose(observed, fresh, accepted)
	}

	deployCompose := fresh.Compose
	repull := true
	if observed.runningDigest != "" {
		running, err := t.port().RunningDigests(fresh)
		if err != nil {
			return t.failure(observed.key, err), false
		}
		if running[observed.service] != accepted {
			return Result{
				Key:    observed.key,
				Code:   domain.ResultDrifted,
				Detail: "the running service digest changed before apply",
			}, false
		}
		if deployCompose, err = t.pin(fresh, observed, observed.remoteDigest); err != nil {
			return t.failure(observed.key, err), false
		}
		// A stack is only ever mutated from a healthy state: every
		// sibling, including health-only ones, has to pass first.
		repull = false
		if !t.healthyOnce(fresh) {
			return Result{
				Key:    observed.key,
				Code:   domain.ResultIneligible,
				Detail: "the stack failed health preflight before apply",
			}, false
		}
	}

	t.updater.emit("transaction.started", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "old_digest": accepted, "new_digest": observed.remoteDigest})

	var failure string
	switch err := t.port().Deploy(fresh, deployCompose, repull); {
	case err == nil:
		if t.waitForHealth(fresh) && t.runningDigestIs(observed, observed.remoteDigest) {
			return t.succeed(observed, accepted, "updated and passed functional health verification")
		}
		failure = "the functional health check timed out"
	case isTimeout(err):
		// An ambiguous deploy is not a failed deploy: the request timed
		// out, the deployment may well have landed. Re-check rather than
		// roll back something that is running correctly.
		if t.waitForConfirmation(observed, fresh) {
			return t.succeed(observed, accepted,
				"the deploy response timed out, but image status and health proved success")
		}
		failure = fmt.Sprintf("the deploy response timed out and could not be proven: %v", err)
	default:
		failure = fmt.Sprintf("the deploy failed: %v", err)
	}
	return t.rollback(observed, accepted, failure)
}

// pin rewrites the target Service's image to an exact digest. An image
// line assembled from variables cannot be pinned in place, so a stack
// that uses one is ineligible for apply rather than rewritten wrongly.
func (t *transaction) pin(stackState backend.StackState, observed observation, digest string) (string, error) {
	declared := stackState.DeclaredImages[observed.service]
	if declared == "" {
		return "", backend.Ineligible("service %q has no literal image line to pin", observed.service)
	}
	if declared != stackState.ServiceImages[observed.service] {
		return "", backend.Ineligible(
			"service %q has a variable-interpolated image line, which cannot be pinned", observed.service)
	}
	pinned, err := composefile.ReplaceServiceImage(stackState.Compose, observed.service,
		declared, observed.image.Pinned(digest))
	if err != nil {
		return "", backend.Ineligible("%v", err)
	}
	return pinned, nil
}

func (t *transaction) succeed(observed observation, accepted, detail string) (Result, bool) {
	now := t.updater.clock.Now()
	if err := t.updater.state.SetAcceptedDigest(observed.key, observed.remoteDigest, now); err != nil {
		return t.failure(observed.key, err), false
	}
	t.recordAttempt(observed, accepted, observed.remoteDigest, domain.ResultUpdated, detail, now)
	t.updater.emit("transaction.succeeded", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "old_digest": accepted, "new_digest": observed.remoteDigest})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultUpdated,
		Detail: detail,
		Digest: observed.remoteDigest,
	}, true
}

// rollback restores the Service to its accepted Baseline and opens the
// breaker either way: a Transaction that needed a rollback is a
// Transaction nobody should repeat unattended.
//
// The document deployed is the pre-apply document with the one image
// scalar pinned to the Baseline digest. In steady state that is byte for
// byte what was there before, since every apply pins; the pin matters
// for the first apply over a mutable tag, where restoring the tag alone
// would re-deploy the new image the engine has now cached.
func (t *transaction) rollback(observed observation, accepted, failure string) (Result, bool) {
	reason := fmt.Sprintf("%s: %s", label(observed.key), failure)
	rollbackCompose := observed.stack.Compose
	if pinned, err := t.pin(observed.stack, observed, accepted); err == nil {
		rollbackCompose = pinned
	} else {
		reason = fmt.Sprintf("%s; the baseline digest could not be pinned back: %v", reason, err)
	}

	healthy := false
	if err := t.port().Deploy(observed.stack, rollbackCompose, observed.runningDigest == ""); err != nil {
		reason = fmt.Sprintf("%s; the rollback request failed: %v", reason, err)
	} else {
		healthy = t.waitForHealth(observed.stack) && t.runningDigestIs(observed, accepted)
	}

	now := t.updater.clock.Now()
	if err := t.updater.state.OpenBreaker(reason, now); err != nil {
		return t.failure(observed.key, err), true
	}
	code := domain.ResultRolledBack
	detail := failure + "; restored the accepted baseline and opened the breaker"
	if !healthy {
		code = domain.ResultRollbackFailed
		detail = failure + "; rollback health verification failed; the breaker is open"
	}
	t.recordAttempt(observed, accepted, observed.remoteDigest, code, detail, now)
	t.updater.emit("breaker.opened", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "reason": reason})
	t.updater.emit("rollback.finished", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "result": string(code)})
	return Result{Key: observed.key, Code: code, Detail: detail, Digest: accepted}, true
}

// propose opens a digest-pin Proposal instead of deploying: a Git-backed
// stack is deployed by its forge, and Ripen never detaches it by writing
// straight to the backend.
func (t *transaction) propose(observed observation, fresh backend.StackState, accepted string) (Result, bool) {
	if t.updater.proposals == nil || t.stack.GitPath == "" {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultIneligible,
			Detail: "the git-backed stack has no reviewed proposal configuration",
		}, false
	}
	status, err := t.updater.state.Status(t.updater.clock.Now())
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if status.BreakerOpen {
		// The breaker halts every outbound action, and a Proposal is one.
		return Result{
			Key:    observed.key,
			Code:   domain.ResultBreakerOpen,
			Detail: breakerDetail(status),
		}, false
	}
	pending, err := t.updater.state.PendingProposal(observed.key)
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if pending != nil {
		// No second proposal while the first sits unmerged, whatever
		// digest it names: a reviewer is looking at one change, not two.
		return Result{
			Key:    observed.key,
			Code:   domain.ResultIneligible,
			Detail: "a proposal is already pending review",
			Digest: pending.Digest,
		}, false
	}

	running, err := t.port().RunningDigests(fresh)
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if running[observed.service] != accepted {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultDrifted,
			Detail: "the running service digest changed before the proposal",
		}, false
	}
	if !t.healthyOnce(fresh) {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultIneligible,
			Detail: "the stack failed health preflight before the proposal",
		}, false
	}
	proposed, err := t.pin(fresh, observed, observed.remoteDigest)
	if err != nil {
		return t.failure(observed.key, err), false
	}

	result, err := t.updater.proposals.Propose(proposal.Change{
		Label:           label(observed.key),
		RepositoryPath:  t.stack.GitPath,
		ExpectedContent: fresh.Compose,
		ProposedContent: proposed,
		Digest:          observed.remoteDigest,
	})
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.updater.state.SetPendingProposal(observed.key, observed.remoteDigest,
		result.URL, t.updater.clock.Now()); err != nil {
		return t.failure(observed.key, err), false
	}
	detail := "opened a digest-pin proposal"
	if !result.Created {
		detail = "the digest-pin proposal is already open"
	}
	t.updater.emit("proposal.created", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "digest": observed.remoteDigest,
		"proposal_url": result.URL, "created": result.Created})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultProposed,
		Detail: fmt.Sprintf("%s: %s", detail, result.URL),
		Digest: observed.remoteDigest,
	}, false
}

// --- verification ---

func (t *transaction) healthPolicies() []config.HealthPolicy {
	if len(t.stack.Services) > 0 {
		policies := make([]config.HealthPolicy, 0, len(t.stack.Services))
		for _, service := range t.stack.Services {
			policies = append(policies, service.Health)
		}
		return policies
	}
	if t.stack.Health == nil {
		return nil
	}
	return []config.HealthPolicy{*t.stack.Health}
}

// healthyOnce is the conjunctive verification: the engine has every
// configured Service running, and every configured functional health
// check passes. A health check that errors counts as unhealthy — an
// unreachable service is exactly what the check exists to catch.
func (t *transaction) healthyOnce(stackState backend.StackState) bool {
	running, _, err := t.port().ServicesRunning(stackState)
	if err != nil || !running {
		return false
	}
	for _, policy := range t.healthPolicies() {
		healthy, err := t.updater.health.Check(policy)
		if err != nil || !healthy {
			return false
		}
	}
	return true
}

func (t *transaction) waitForHealth(stackState backend.StackState) bool {
	return t.waitUntil(func() bool { return t.healthyOnce(stackState) })
}

// waitForConfirmation resolves an ambiguous deploy: the stack has to
// look healthy *and* show the new image before the update is accepted.
func (t *transaction) waitForConfirmation(observed observation, fresh backend.StackState) bool {
	return t.waitUntil(func() bool {
		if !t.healthyOnce(fresh) {
			return false
		}
		if observed.runningDigest != "" {
			return t.runningDigestIs(observed, observed.remoteDigest)
		}
		current, err := t.port().Observe(t.stack)
		return err == nil && current.ImageStatus == "updated"
	})
}

func (t *transaction) waitUntil(condition func() bool) bool {
	deadline := t.updater.clock.Now().
		Add(time.Duration(t.updater.policy.VerificationTimeoutSeconds) * time.Second)
	for {
		if condition() {
			return true
		}
		remaining := deadline.Sub(t.updater.clock.Now())
		if remaining <= 0 {
			return false
		}
		t.updater.clock.Sleep(min(maximumPollInterval, remaining))
	}
}

func (t *transaction) runningDigestIs(observed observation, expected string) bool {
	if observed.runningDigest == "" {
		return true
	}
	running, err := t.port().RunningDigests(observed.stack)
	if err != nil {
		return false
	}
	return running[observed.service] == expected
}

// isTimeout reports whether a backend call failed by running out of
// time, which is ambiguous, rather than by being refused, which is not.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
