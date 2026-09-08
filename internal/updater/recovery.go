package updater

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

// ReconcileTransaction verifies the Baseline after the operator attests that the interrupted backend request has finished.
func (u *Updater) ReconcileTransaction(reason string) (state.Status, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return state.Status{}, errors.New("a reconciliation reason confirming the backend request has finished is required")
	}
	token, acquired, err := u.state.AcquireLease(u.clock.Now(), u.policy.LeaseTTLSeconds)
	if err != nil {
		return state.Status{}, err
	}
	if !acquired {
		return state.Status{}, errors.New("cannot reconcile while another run owns the lease")
	}
	owner, release := u.owned(context.Background(), token)
	defer release()
	marker, err := owner.state.ActiveTransaction()
	if err != nil {
		return state.Status{}, err
	}
	if marker == nil {
		return state.Status{}, errors.New("no interrupted transaction requires reconciliation")
	}
	if marker.Phase == "proposing" {
		return state.Status{}, errors.New("cannot reconcile an interrupted proposal without proving the external proposal request has settled")
	}
	var stack *config.StackPolicy
	for i := range u.policy.Stacks {
		candidate := &u.policy.Stacks[i]
		if candidate.Backend == marker.Key.Backend && candidate.Name == marker.Key.Stack {
			stack = candidate
			break
		}
	}
	if stack == nil {
		return state.Status{}, errors.New("the interrupted transaction stack is absent from the policy")
	}
	port := owner.backends[stack.Backend]
	if port == nil {
		return state.Status{}, errors.New("the interrupted transaction backend is not configured")
	}
	if err := port.Preflight(); err != nil {
		return state.Status{}, err
	}
	fresh, err := port.Observe(*stack)
	if err != nil {
		return state.Status{}, err
	}
	expected := slices.Sorted(slices.Values(stack.ExpectedServices))
	if !slices.Equal(fresh.Services, expected) {
		return state.Status{}, errors.New("cannot reconcile: deployed services differ from the reviewed policy")
	}
	service := marker.Key.Service
	if service == "" {
		if len(expected) != 1 {
			return state.Status{}, errors.New("cannot reconcile: the affected service cannot be identified")
		}
		service = expected[0]
	}
	if !slices.Contains(expected, service) {
		return state.Status{}, errors.New("cannot reconcile: the affected service is absent from the reviewed policy")
	}
	accepted, found, err := owner.state.AcceptedDigest(marker.Key)
	if err != nil {
		return state.Status{}, err
	}
	if !found || accepted == "" {
		return state.Status{}, errors.New("cannot reconcile without an accepted baseline")
	}
	t := &transaction{updater: owner, stack: *stack, runID: marker.RunID, mode: domain.ModeMonitor}
	if !t.healthyOnce(fresh) {
		return state.Status{}, errors.New("cannot reconcile: the stack or a sibling failed health verification")
	}
	running, err := port.RunningDigests(fresh)
	if err != nil {
		return state.Status{}, err
	}
	if !slices.Equal(slices.Sorted(maps.Keys(running)), expected) || running[service] != accepted {
		return state.Status{}, errors.New("cannot reconcile: running digests do not prove the accepted baseline")
	}
	if err := owner.checkOwnership(); err != nil {
		return state.Status{}, err
	}
	attempt := state.Attempt{Key: marker.Key, RunID: marker.RunID, Actor: u.actor, OldDigest: accepted, NewDigest: accepted, Result: domain.ResultUpToDate, Detail: "explicitly reconciled the healthy accepted baseline after the backend request finished: " + reason}
	if err := owner.state.ReconcileTransaction(token, attempt, reason, u.clock.Now()); err != nil {
		return state.Status{}, err
	}
	u.emit(event.BreakerCleared, event.Subject{}, event.Data{Reason: reason})
	return u.Status()
}
