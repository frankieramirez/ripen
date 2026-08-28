package updater

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/state"
)

var runtimePlatform = registry.Platform{OS: "linux", Architecture: "amd64"}

type transaction struct {
	updater *Updater
	stack   config.StackPolicy
	runID   string
	mode    domain.Mode
}

type observation struct {
	stack         backend.StackState
	key           state.Key
	service       string
	image         domain.ImageReference
	declared      string
	remoteDigest  string
	runningDigest string
	imageStatus   string
	health        config.HealthPolicy
	autoApply     bool
}

func (t *transaction) port() backend.Port {
	return t.updater.backends[t.stack.Backend]
}

func (t *transaction) run(slots int) ([]Result, int) {
	stackState, err := t.port().Observe(t.stack)
	if err != nil {
		return []Result{t.failure(state.Key{Backend: t.stack.Backend, Stack: t.stack.Name}, err)}, 0
	}
	observations, err := t.observe(stackState)
	if err != nil {
		return []Result{t.failure(state.Key{Backend: t.stack.Backend, Stack: t.stack.Name}, err)}, 0
	}
	results := make([]Result, 0, len(observations))
	applied := 0
	for _, observed := range observations {
		result, changed := t.evaluate(observed, slots-applied > 0)
		if changed {
			applied++
		}
		results = append(results, result)
	}
	return results, applied
}

func (t *transaction) failure(key state.Key, err error) Result {
	var notVisible *backend.NotVisibleError
	var ineligible *backend.IneligibleError
	var engine *backend.EngineUnavailableError
	switch {
	case errors.As(err, &notVisible):
		return Result{Key: key, Code: domain.ResultNotVisible, Detail: err.Error()}
	case errors.As(err, &ineligible):
		return Result{Key: key, Code: domain.ResultIneligible, Detail: err.Error()}
	case errors.As(err, &engine):
		return Result{Key: key, Code: domain.ResultEngineUnavailable, Detail: err.Error()}
	}
	t.updater.emit(event.StackError, t.subject(key), event.Data{Detail: err.Error()})
	return Result{Key: key, Code: domain.ResultError, Detail: err.Error()}
}

func (t *transaction) observe(stackState backend.StackState) ([]observation, error) {
	expected := slices.Sorted(slices.Values(t.stack.ExpectedServices))
	if !slices.Equal(stackState.Services, expected) {
		return nil, backend.Ineligible("services changed: expected %v, found %v",
			expected, stackState.Services)
	}

	if len(t.stack.Services) > 0 {
		if stackState.RunningDigests == nil {
			return nil, backend.Ineligible("running services cannot be proven for a multi-service stack")
		}
		if !slices.Equal(slices.Sorted(maps.Keys(stackState.RunningDigests)), expected) {
			return nil, backend.Ineligible("running services do not match the reviewed policy")
		}
		observations := make([]observation, 0, len(t.stack.Services))
		for _, service := range t.stack.Services {
			if !service.Enabled {
				continue
			}
			observed, err := t.observeService(stackState, service.Name,
				state.Key{Backend: t.stack.Backend, Stack: t.stack.Name, Service: service.Name},
				service.Health, service.AutoApply)
			if err != nil {
				return nil, err
			}
			observations = append(observations, observed)
		}
		return observations, nil
	}

	if len(stackState.Services) != 1 || t.stack.Health == nil {
		return nil, backend.Ineligible("the single-service policy does not match the deployed stack")
	}
	observed, err := t.observeService(stackState, stackState.Services[0],
		state.Key{Backend: t.stack.Backend, Stack: t.stack.Name},
		*t.stack.Health, t.stack.AutoApply)
	if err != nil {
		return nil, err
	}
	return []observation{observed}, nil
}

func (t *transaction) observeService(stackState backend.StackState, service string,
	key state.Key, health config.HealthPolicy, autoApply bool) (observation, error) {
	reference, ok := stackState.ServiceImages[service]
	if !ok || reference == "" {
		return observation{}, backend.Ineligible("service %q must have a literal image reference", service)
	}
	image, err := domain.ParseImageReference(reference)
	if err != nil {
		return observation{}, backend.Ineligible("service %q: %v", service, err)
	}
	observed := observation{
		stack:     stackState,
		key:       key,
		service:   service,
		image:     image,
		declared:  stackState.DeclaredImages[service],
		health:    health,
		autoApply: autoApply,
	}

	if stackState.RunningDigests != nil {
		running, ok := stackState.RunningDigests[service]
		if !ok {
			return observation{}, backend.Ineligible("service %q is not running", service)
		}
		if observed.remoteDigest, err = t.updater.registry.ResolvePlatformDigest(image, runtimePlatform); err != nil {
			return observation{}, err
		}
		observed.runningDigest = running
		observed.imageStatus = "outdated"
		if running == observed.remoteDigest {
			observed.imageStatus = "updated"
		}
		return observed, nil
	}

	if stackState.ImageStatus != "updated" && stackState.ImageStatus != "outdated" {
		return observation{}, backend.Ineligible("the backend image status is %q", stackState.ImageStatus)
	}
	observed.imageStatus = stackState.ImageStatus
	if observed.remoteDigest, err = t.updater.registry.ResolveDigest(image); err != nil {
		return observation{}, err
	}
	return observed, nil
}

func (t *transaction) evaluate(observed observation, slotAvailable bool) (Result, bool) {
	now := t.updater.clock.Now()
	accepted, found, err := t.updater.state.AcceptedDigest(observed.key)
	if err != nil {
		return t.failure(observed.key, err), false
	}
	if !found {
		return t.baseline(observed, now)
	}

	pending, err := t.updater.state.PendingProposal(observed.key)
	if err != nil {
		return t.failure(observed.key, err), false
	}

	if observed.runningDigest != "" && observed.runningDigest != accepted {
		if t.proposalMode(observed.stack) && pending != nil &&
			pending.Digest == observed.runningDigest &&
			observed.image.PinnedDigest == observed.runningDigest {
			return t.acceptGitDeployment(observed, accepted, pending.URL, now)
		}
		return Result{
			Key:    observed.key,
			Code:   domain.ResultDrifted,
			Detail: "the running service digest changed outside Ripen",
			Digest: observed.runningDigest,
		}, false
	}
	t.recovered(observed, accepted, now)

	if pending != nil && pending.Digest != observed.remoteDigest {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultIneligible,
			Detail: "a different proposal is still pending review",
			Digest: pending.Digest,
		}, false
	}
	if observed.imageStatus == "updated" && observed.remoteDigest == accepted {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultUpToDate,
			Detail: "the running image matches the accepted baseline",
			Digest: accepted,
		}, false
	}
	if observed.remoteDigest == accepted {
		return Result{
			Key:    observed.key,
			Code:   domain.ResultIneligible,
			Detail: "the backend reports outdated but the registry digest has not changed",
			Digest: accepted,
		}, false
	}

	candidate, err := t.updater.state.ObserveCandidate(observed.key, observed.remoteDigest, now)
	if err != nil {
		return t.failure(observed.key, err), false
	}
	t.updater.emit(event.CandidateObserved, t.subject(observed.key),
		event.Data{Digest: observed.remoteDigest, Observations: candidate.Count})
	age := now.Sub(candidate.FirstSeen)
	mature := candidate.Count >= 2 && age >= time.Duration(t.updater.policy.CandidateMinAgeSeconds)*time.Second
	if mature {
		t.updater.emit(event.CandidateMatured, t.subject(observed.key),
			event.Data{Digest: observed.remoteDigest, Observations: candidate.Count})
	}
	candidateResult := Result{
		Key:  observed.key,
		Code: domain.ResultCandidate,
		Detail: fmt.Sprintf("candidate observed %d time(s), age %ds",
			candidate.Count, int(age.Seconds())),
		Digest: observed.remoteDigest,
	}
	if t.mode != domain.ModeApply || !observed.autoApply || !mature || !slotAvailable {
		return candidateResult, false
	}
	return t.apply(observed, accepted)
}

func (t *transaction) baseline(observed observation, now time.Time) (Result, bool) {
	if observed.runningDigest != "" {
		if err := t.updater.state.SetAcceptedDigest(observed.key, observed.runningDigest, now); err != nil {
			return t.failure(observed.key, err), false
		}
		t.updater.emit(event.BaselineRecorded, t.subject(observed.key),
			event.Data{Digest: observed.runningDigest})
		return Result{
			Key:    observed.key,
			Code:   domain.ResultBaselined,
			Detail: "recorded the proven running service digest as the accepted baseline",
			Digest: observed.runningDigest,
		}, false
	}
	if observed.imageStatus != "updated" {
		t.updater.emit(event.BaselineBlocked, t.subject(observed.key), event.Data{
			Digest: observed.remoteDigest,
			Detail: "an update is already pending; the running digest cannot be proven",
		})
		return Result{
			Key:    observed.key,
			Code:   domain.ResultBaselineBlocked,
			Detail: "an update is already pending; the running digest cannot be proven",
			Digest: observed.remoteDigest,
		}, false
	}
	if err := t.updater.state.SetAcceptedDigest(observed.key, observed.remoteDigest, now); err != nil {
		return t.failure(observed.key, err), false
	}
	t.updater.emit(event.BaselineRecorded, t.subject(observed.key),
		event.Data{Digest: observed.remoteDigest})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultBaselined,
		Detail: "recorded the current registry digest as the accepted baseline",
		Digest: observed.remoteDigest,
	}, false
}

func (t *transaction) acceptGitDeployment(observed observation, accepted, proposalURL string,
	now time.Time) (Result, bool) {
	if !t.healthyOnce(observed.stack) {
		reason := fmt.Sprintf("%s: the deployed proposal failed functional health verification",
			label(observed.key))
		if err := t.updater.state.OpenBreaker(reason, now); err != nil {
			return t.failure(observed.key, err), false
		}
		t.recordAttempt(observed, accepted, observed.runningDigest, domain.ResultError, reason, now)
		t.updater.emit(event.BreakerOpened, t.subject(observed.key), event.Data{Reason: reason})
		return Result{
			Key:    observed.key,
			Code:   domain.ResultError,
			Detail: "the deployed proposal failed health verification; the breaker is open",
			Digest: observed.runningDigest,
		}, false
	}
	detail := "the proposal deployed and passed functional health verification"
	if err := t.updater.state.SetAcceptedDigest(observed.key, observed.runningDigest, now); err != nil {
		return t.failure(observed.key, err), false
	}
	t.recordAttempt(observed, accepted, observed.runningDigest, domain.ResultUpdated, detail, now)
	t.updater.emit(event.ProposalDeployed, t.subject(observed.key),
		event.Data{Digest: observed.runningDigest, ProposalURL: proposalURL})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultUpdated,
		Detail: detail,
		Digest: observed.runningDigest,
	}, false
}

func (t *transaction) recordAttempt(observed observation, oldDigest, newDigest string,
	code domain.ResultCode, detail string, now time.Time) {
	if err := t.updater.state.RecordAttempt(state.Attempt{
		Key:       observed.key,
		RunID:     t.runID,
		Actor:     t.updater.actor,
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Result:    code,
		Detail:    detail,
	}, now); err != nil {
		t.updater.emit(event.StackError, t.subject(observed.key), event.Data{Detail: err.Error()})
	}
}

func (t *transaction) recovered(observed observation, accepted string, now time.Time) {
	last, err := t.updater.state.LastAttempt(observed.key)
	if err != nil || last == nil {
		return
	}
	switch last.Result {
	case domain.ResultError, domain.ResultRolledBack, domain.ResultRollbackFailed:
	default:
		return
	}
	if observed.runningDigest != "" && observed.runningDigest != accepted {
		return
	}
	if !t.healthyOnce(observed.stack) {
		return
	}
	detail := "the service is running its accepted baseline again"
	t.recordAttempt(observed, accepted, accepted, domain.ResultUpToDate, detail, now)
	t.updater.emit(event.StackRecovered, t.subject(observed.key),
		event.Data{Digest: accepted, Detail: detail})
}

func (t *transaction) subject(key state.Key) event.Subject {
	return event.Subject{
		RunID:   t.runID,
		Backend: key.Backend,
		Stack:   key.Stack,
		Service: key.Service,
	}
}

func label(key state.Key) string {
	if key.Service == "" {
		return key.Stack
	}
	return key.Stack + "/" + key.Service
}
