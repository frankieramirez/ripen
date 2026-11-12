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
	"github.com/frankieramirez/ripen/internal/state"
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

	deployCompose, err := t.pin(fresh, observed, observed.remoteDigest)
	if err != nil {
		return t.failure(observed.key, err), false
	}
	repull := observed.runningDigest == ""
	if !repull {
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
		if !t.healthyOnce(fresh) {
			return Result{
				Key:    observed.key,
				Code:   domain.ResultIneligible,
				Detail: "the stack failed health preflight before apply",
			}, false
		}
	}

	if err := t.updater.checkOwnership(); err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.updater.state.BeginTransaction(t.updater.token, observed.key, t.runID, t.updater.clock.Now()); err != nil {
		return t.failure(observed.key, err), false
	}
	t.updater = t.updater.withContext(t.updater.leaseCtx)
	if err := t.phase("deploying"); err != nil {
		return t.failure(observed.key, err), true
	}
	t.updater.emit(event.TransactionStarted, t.subject(observed.key),
		event.Data{OldDigest: accepted, NewDigest: observed.remoteDigest})

	var failure string
	switch err := t.port().Deploy(fresh, deployCompose, repull); {
	case err == nil:
		if err := t.phase("verifying"); err != nil {
			return t.failure(observed.key, err), true
		}
		failure = t.unverified(fresh, observed, observed.remoteDigest)
		if failure == "" {
			return t.succeed(observed, accepted, "updated and passed functional health verification")
		}
	case isTimeout(err):
		if err := t.phase("verifying"); err != nil {
			return t.failure(observed.key, err), true
		}
		unproven := t.awaitConfirmation(observed, fresh)
		if unproven == "" {
			return t.succeed(observed, accepted,
				"the deploy response timed out, but image status and health proved success")
		}
		failure = fmt.Sprintf("the deploy response timed out and could not be proven: %v; at the last check %s",
			err, unproven)
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
	if err := t.complete(observed, accepted, domain.ResultUpdated, detail, observed.remoteDigest, "", true, now); err != nil {
		return t.failure(observed.key, err), true
	}
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
	if err := t.phase("rolling_back"); err != nil {
		return t.failure(observed.key, err), true
	}
	reason := fmt.Sprintf("%s: %s", label(observed.key), failure)
	rollbackCompose := observed.stack.Compose
	if pinned, err := t.pin(observed.stack, observed, accepted); err == nil {
		rollbackCompose = pinned
	} else {
		reason = fmt.Sprintf("%s; the baseline digest could not be pinned back: %v", reason, err)
	}

	healthy := false
	unrestored := "rollback health verification failed"
	if err := t.port().Deploy(observed.stack, rollbackCompose, observed.runningDigest == ""); err != nil {
		reason = fmt.Sprintf("%s; the rollback request failed: %v", reason, err)
	} else if unverified := t.unverified(observed.stack, observed, accepted); unverified != "" {
		unrestored = fmt.Sprintf("%s: %s", unrestored, unverified)
	} else {
		healthy = true
	}

	now := t.updater.clock.Now()
	code := domain.ResultRolledBack
	detail := failure + "; restored the accepted baseline and opened the breaker"
	if !healthy {
		code = domain.ResultRollbackFailed
		detail = fmt.Sprintf("%s; %s; the breaker is open", failure, unrestored)
	}
	if err := t.complete(observed, accepted, code, detail, "", reason, healthy, now); err != nil {
		return t.failure(observed.key, err), true
	}
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

	if err := t.updater.checkOwnership(); err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.updater.state.BeginTransaction(t.updater.token, observed.key, t.runID, t.updater.clock.Now()); err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.phase("proposing"); err != nil {
		return t.failure(observed.key, err), false
	}
	result, err := t.updater.proposals.Propose(proposal.Change{
		Label:           label(observed.key),
		RepositoryPath:  t.stack.GitPath,
		ExpectedContent: fresh.Compose,
		ProposedContent: proposed,
		Digest:          observed.remoteDigest,
	})
	if errors.Is(err, proposal.ErrNothingWritten) {
		if finishErr := t.updater.state.FinishTransaction(t.updater.token); finishErr != nil {
			return t.failure(observed.key, finishErr), false
		}
		return Result{Key: observed.key, Code: domain.ResultIneligible, Detail: err.Error()}, false
	}
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.updater.state.SetPendingProposal(observed.key, observed.remoteDigest,
		result.URL, t.updater.clock.Now()); err != nil {
		return t.failure(observed.key, err), false
	}
	if err := t.updater.state.FinishTransaction(t.updater.token); err != nil {
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

type healthCheck struct {
	service string
	policy  config.HealthPolicy
}

func (c healthCheck) target() string {
	if c.service == "" {
		return "the health target"
	}
	return fmt.Sprintf("the health target for service %q", c.service)
}

func (t *transaction) healthChecks() []healthCheck {
	if len(t.stack.Services) > 0 {
		checks := make([]healthCheck, 0, len(t.stack.Services))
		for _, service := range t.stack.Services {
			checks = append(checks, healthCheck{service: service.Name, policy: service.Health})
		}
		return checks
	}
	if t.stack.Health == nil {
		return nil
	}
	return []healthCheck{{policy: *t.stack.Health}}
}

func (t *transaction) healthyOnce(stackState backend.StackState) bool {
	return t.unhealthy(stackState) == ""
}

func (t *transaction) unhealthy(stackState backend.StackState) string {
	running, stopped, err := t.port().ServicesRunning(stackState)
	switch {
	case err != nil:
		return fmt.Sprintf("the backend could not report its services: %v", err)
	case !running && stopped == "":
		return "a service was not running"
	case !running:
		return stopped
	}
	for _, check := range t.healthChecks() {
		answer, err := t.updater.health.Check(check.policy)
		switch {
		case err != nil:
			return check.target() + " could not be checked"
		case answer.Healthy:
		case answer.Status == 0:
			return check.target() + " gave no answer"
		default:
			return fmt.Sprintf("%s answered %d, which is not an accepted status", check.target(), answer.Status)
		}
	}
	return ""
}

func (t *transaction) unverified(stackState backend.StackState, observed observation, digest string) string {
	if unhealthy := t.awaitHealth(stackState); unhealthy != "" {
		return "the functional health check timed out: at the last check " + unhealthy
	}
	if wrong := t.wrongDigest(observed, digest); wrong != "" {
		return "the stack passed its health checks, but " + wrong
	}
	return ""
}

func (t *transaction) awaitHealth(stackState backend.StackState) string {
	return t.waitUntil(func() string { return t.unhealthy(stackState) })
}

func (t *transaction) awaitConfirmation(observed observation, fresh backend.StackState) string {
	return t.waitUntil(func() string {
		if unhealthy := t.unhealthy(fresh); unhealthy != "" {
			return unhealthy
		}
		if observed.runningDigest != "" {
			return t.wrongDigest(observed, observed.remoteDigest)
		}
		current, err := t.port().Observe(t.stack)
		switch {
		case err != nil:
			return fmt.Sprintf("the stack could not be observed: %v", err)
		case current.ImageStatus != "updated":
			return fmt.Sprintf("the backend reported image status %q", current.ImageStatus)
		}
		return ""
	})
}

func (t *transaction) waitUntil(unmet func() string) string {
	deadline := t.updater.clock.Now().
		Add(time.Duration(t.updater.policy.VerificationTimeoutSeconds) * time.Second)
	for {
		if t.updater.checkOwnership() != nil {
			return "this run no longer owned the transaction"
		}
		found := unmet()
		if found == "" {
			return ""
		}
		remaining := deadline.Sub(t.updater.clock.Now())
		if remaining <= 0 {
			return found
		}
		t.updater.clock.Sleep(min(maximumPollInterval, remaining))
	}
}

func (t *transaction) wrongDigest(observed observation, expected string) string {
	if observed.runningDigest == "" {
		return ""
	}
	running, err := t.port().RunningDigests(observed.stack)
	switch {
	case err != nil:
		return fmt.Sprintf("the running digest could not be read: %v", err)
	case running[observed.service] == "":
		return "the backend reported no running digest for the service"
	case running[observed.service] != expected:
		return fmt.Sprintf("the running digest was %s, not %s", running[observed.service], expected)
	}
	return ""
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func (t *transaction) phase(phase string) error {
	if err := t.updater.checkOwnership(); err != nil {
		return err
	}
	if err := t.updater.state.SetTransactionPhase(t.updater.token, phase, t.updater.clock.Now()); err != nil {
		return err
	}
	marker, err := t.updater.state.ActiveTransaction()
	if err != nil {
		return err
	}
	if marker == nil {
		return errors.New("transaction progress lost its durable marker")
	}
	t.updater.emit(event.TransactionProgress, t.subject(marker.Key), event.Data{Phase: event.Phase(phase)})
	return nil
}

func (t *transaction) complete(observed observation, accepted string, code domain.ResultCode, detail, digest, reason string, clearMarker bool, now time.Time) error {
	return t.updater.state.CompleteTransaction(t.updater.token, state.Attempt{Key: observed.key, RunID: t.runID, Actor: t.updater.actor, OldDigest: accepted, NewDigest: observed.remoteDigest, Result: code, Detail: detail}, digest, reason, clearMarker, now)
}
