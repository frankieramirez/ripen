package updater

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

// Rebaseline records a service's running digest as its accepted Baseline
// after an operator decides a change made outside Ripen should stand. It
// refuses unless the backend proves the digest every expected service is
// running and every health check passes, and it records the reason.
func (u *Updater) Rebaseline(stackName, service, reason string) (Result, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Result{}, errors.New("a rebaseline reason is required: say why the running digest should stand")
	}
	index := slices.IndexFunc(u.policy.Stacks, func(stack config.StackPolicy) bool {
		return stack.Name == stackName
	})
	if index < 0 {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownStack, stackName)
	}
	stack := u.policy.Stacks[index]
	expected := slices.Sorted(slices.Values(stack.ExpectedServices))
	running, err := rebaselineTarget(stack, service, expected)
	if err != nil {
		return Result{}, err
	}
	key := state.Key{Backend: stack.Backend, Stack: stack.Name, Service: service}

	token, acquired, err := u.state.AcquireLease(u.clock.Now(), u.policy.LeaseTTLSeconds)
	if err != nil {
		return Result{}, err
	}
	if !acquired {
		return Result{}, fmt.Errorf("%w: another run owns the lease", ErrRebaselineRefused)
	}
	owner, release := u.owned(context.Background(), token)
	defer release()
	if marker, err := owner.state.ActiveTransaction(); err != nil {
		return Result{}, err
	} else if marker != nil {
		return Result{}, fmt.Errorf("%w: an interrupted transaction must be reconciled first", ErrRebaselineRefused)
	}
	if pending, err := owner.state.PendingProposal(key); err != nil {
		return Result{}, err
	} else if pending != nil {
		return Result{}, fmt.Errorf("%w: a proposal is pending review; let it deploy or clear it first", ErrRebaselineRefused)
	}

	port := owner.backends[stack.Backend]
	if port == nil {
		return Result{}, fmt.Errorf("the %s backend is not configured", stack.Backend)
	}
	if err := port.Preflight(); err != nil {
		return Result{}, err
	}
	fresh, err := port.Observe(stack)
	if err != nil {
		return Result{}, err
	}
	if !slices.Equal(fresh.Services, expected) {
		return Result{}, fmt.Errorf("%w: deployed services differ from the reviewed policy", ErrRebaselineRefused)
	}
	digests, err := port.RunningDigests(fresh)
	if err != nil {
		return Result{}, err
	}
	digest := digests[running]
	if digest == "" || !slices.Equal(slices.Sorted(maps.Keys(digests)), expected) {
		return Result{}, fmt.Errorf("%w: the running digest cannot be proven for every expected service", ErrRebaselineRefused)
	}
	t := &transaction{updater: owner, stack: stack, runID: newRunID(), mode: domain.ModeMonitor}
	if !t.healthyOnce(fresh) {
		return Result{}, fmt.Errorf("%w: the stack or a sibling failed health verification", ErrRebaselineRefused)
	}

	accepted, _, err := owner.state.AcceptedDigest(key)
	if err != nil {
		return Result{}, err
	}
	if accepted == digest {
		return Result{Key: key, Code: domain.ResultUpToDate,
			Detail: "the running digest is already the accepted baseline", Digest: digest}, nil
	}
	if err := owner.checkOwnership(); err != nil {
		return Result{}, err
	}
	now := u.clock.Now()
	if err := owner.state.SetAcceptedDigest(key, digest, now); err != nil {
		return Result{}, err
	}
	detail := "rebaselined the proven running digest: " + reason
	if err := owner.state.RecordAttempt(state.Attempt{Key: key, RunID: t.runID, Actor: u.actor,
		OldDigest: accepted, NewDigest: digest, Result: domain.ResultBaselined, Detail: detail}, now); err != nil {
		return Result{}, err
	}
	u.emit(event.BaselineRecorded, t.subject(key), event.Data{Digest: digest, Reason: reason})
	return Result{Key: key, Code: domain.ResultBaselined, Detail: detail, Digest: digest}, nil
}

func rebaselineTarget(stack config.StackPolicy, service string, expected []string) (string, error) {
	if !stack.Enabled {
		return "", fmt.Errorf("%w: stack %q is disabled", ErrRebaselineRefused, stack.Name)
	}
	if len(stack.Services) == 0 {
		if service != "" || len(expected) != 1 {
			return "", fmt.Errorf("%w: stack %q is single-service: rebaseline it without --service", ErrRebaselineRefused, stack.Name)
		}
		return expected[0], nil
	}
	if service == "" {
		return "", fmt.Errorf("%w: stack %q has several services: name one with --service", ErrRebaselineRefused, stack.Name)
	}
	index := slices.IndexFunc(stack.Services, func(policy config.ServicePolicy) bool { return policy.Name == service })
	if index < 0 {
		return "", fmt.Errorf("%w: service %q is not in the policy for stack %q", ErrRebaselineRefused, service, stack.Name)
	}
	if !stack.Services[index].Enabled {
		return "", fmt.Errorf("%w: service %q is health-only and has no baseline to rebaseline", ErrRebaselineRefused, service)
	}
	return service, nil
}
