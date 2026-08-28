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
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/proposal"
)

const maximumPollInterval = 10 * time.Second

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
	if t.proposalMode(fresh) {
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
		repull = false
		if !t.healthyOnce(fresh) {
			return Result{
				Key:    observed.key,
				Code:   domain.ResultIneligible,
				Detail: "the stack failed health preflight before apply",
			}, false
		}
	}

	t.updater.emit(event.TransactionStarted, t.subject(observed.key),
		event.Data{OldDigest: accepted, NewDigest: observed.remoteDigest})

	var failure string
	switch err := t.port().Deploy(fresh, deployCompose, repull); {
	case err == nil:
		if t.waitForHealth(fresh) && t.runningDigestIs(observed, observed.remoteDigest) {
			return t.succeed(observed, accepted, "updated and passed functional health verification")
		}
		failure = "the functional health check timed out"
	case isTimeout(err):
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
	t.updater.emit(event.TransactionSucceeded, t.subject(observed.key),
		event.Data{OldDigest: accepted, NewDigest: observed.remoteDigest, Detail: detail})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultUpdated,
		Detail: detail,
		Digest: observed.remoteDigest,
	}, true
}

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
	t.updater.emit(event.BreakerOpened, t.subject(observed.key), event.Data{Reason: reason})
	rollbackEvent := event.TransactionRolledBack
	if code == domain.ResultRollbackFailed {
		rollbackEvent = event.TransactionRollbackFailed
	}
	t.updater.emit(rollbackEvent, t.subject(observed.key), event.Data{
		Result: string(code), OldDigest: accepted, NewDigest: observed.remoteDigest, Detail: detail})
	return Result{Key: observed.key, Code: code, Detail: detail, Digest: accepted}, true
}

func (t *transaction) proposalMode(stackState backend.StackState) bool {
	return stackState.GitBacked || t.stack.GitPath != ""
}

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
	opened := result
	t.updater.emit(event.ProposalCreated, t.subject(observed.key), event.Data{
		Digest: observed.remoteDigest, ProposalURL: result.URL, Created: result.Created, Detail: detail})
	return Result{
		Key:      observed.key,
		Code:     domain.ResultProposed,
		Detail:   fmt.Sprintf("%s: %s", detail, result.URL),
		Digest:   observed.remoteDigest,
		Proposal: &opened,
	}, false
}

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

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
