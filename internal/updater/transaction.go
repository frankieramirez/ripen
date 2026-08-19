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
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/state"
)

// runtimePlatform is the platform digests are resolved for. Ripen
// manages Linux container hosts; other platforms are roadmap.
var runtimePlatform = registry.Platform{OS: "linux", Architecture: "amd64"}

// transaction is one stack's part of a run.
type transaction struct {
	updater *Updater
	stack   config.StackPolicy
	runID   string
	mode    domain.Mode
}

// observation is one Service as observed this run: what the policy says,
// what is deployed, and what the registry offers.
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

// failure maps an error to the result code that says what an operator
// should do about it: nothing (not visible, ineligible), fix the engine
// (engine unavailable), or look (error).
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
	t.updater.emit("stack.error", map[string]any{
		"run_id": t.runID, "backend": string(key.Backend), "stack": key.Stack, "error": err.Error()})
	return Result{Key: key, Code: domain.ResultError, Detail: err.Error()}
}

// observe turns one backend observation into per-Service observations,
// refusing anything the reviewed policy does not describe exactly.
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
			// A disabled Service is health-only: it gates its siblings
			// but is never resolved against a registry or baselined.
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
		// The backend can prove what is running, so the comparison is
		// digest to digest and the registry is asked for this platform.
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

	// Otherwise the backend's own verdict on the stack stands in, and
	// the registry answers for the tag as a whole (an index digest).
	if stackState.ImageStatus != "updated" && stackState.ImageStatus != "outdated" {
		return observation{}, backend.Ineligible("the backend image status is %q", stackState.ImageStatus)
	}
	observed.imageStatus = stackState.ImageStatus
	if observed.remoteDigest, err = t.updater.registry.ResolveDigest(image); err != nil {
		return observation{}, err
	}
	return observed, nil
}

// evaluate decides one Service's outcome, and reports whether it
// consumed the run's single update slot.
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
		if observed.stack.GitBacked && pending != nil &&
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
	age := now.Sub(candidate.FirstSeen)
	mature := candidate.Count >= 2 && age >= time.Duration(t.updater.policy.CandidateMinAgeSeconds)*time.Second
	if mature {
		t.updater.emit("candidate.matured", map[string]any{
			"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
			"service": observed.key.Service, "digest": observed.remoteDigest})
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

// baseline records what is provably running as the accepted Baseline.
// Nothing is ever baselined that cannot be proven: when the backend can
// only say "outdated", the running digest is unknown and baselining
// would silently bless an update nobody reviewed.
func (t *transaction) baseline(observed observation, now time.Time) (Result, bool) {
	if observed.runningDigest != "" {
		if err := t.updater.state.SetAcceptedDigest(observed.key, observed.runningDigest, now); err != nil {
			return t.failure(observed.key, err), false
		}
		return Result{
			Key:    observed.key,
			Code:   domain.ResultBaselined,
			Detail: "recorded the proven running service digest as the accepted baseline",
			Digest: observed.runningDigest,
		}, false
	}
	if observed.imageStatus != "updated" {
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
	return Result{
		Key:    observed.key,
		Code:   domain.ResultBaselined,
		Detail: "recorded the current registry digest as the accepted baseline",
		Digest: observed.remoteDigest,
	}, false
}

// acceptGitDeployment closes the loop on a merged Proposal: the digest
// is accepted only once the live stack shows the pin, runs it, and
// passes health. Anything less opens the breaker.
func (t *transaction) acceptGitDeployment(observed observation, accepted, proposalURL string,
	now time.Time) (Result, bool) {
	if !t.healthyOnce(observed.stack) {
		reason := fmt.Sprintf("%s: the deployed proposal failed functional health verification",
			label(observed.key))
		if err := t.updater.state.OpenBreaker(reason, now); err != nil {
			return t.failure(observed.key, err), false
		}
		t.recordAttempt(observed, accepted, observed.runningDigest, domain.ResultError, reason, now)
		t.updater.emit("breaker.opened", map[string]any{
			"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
			"service": observed.key.Service, "reason": reason})
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
	t.updater.emit("proposal.deployed", map[string]any{
		"run_id": t.runID, "backend": string(observed.key.Backend), "stack": observed.key.Stack,
		"service": observed.key.Service, "digest": observed.runningDigest, "proposal_url": proposalURL})
	return Result{
		Key:    observed.key,
		Code:   domain.ResultUpdated,
		Detail: detail,
		Digest: observed.runningDigest,
	}, false
}

func (t *transaction) recordAttempt(observed observation, oldDigest, newDigest string,
	code domain.ResultCode, detail string, now time.Time) {
	// A failed audit write must not change what the Transaction decided;
	// it is reported through the Event stream instead.
	if err := t.updater.state.RecordAttempt(state.Attempt{
		Key:       observed.key,
		RunID:     t.runID,
		Actor:     t.updater.actor,
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Result:    code,
		Detail:    detail,
	}, now); err != nil {
		t.updater.emit("stack.error", map[string]any{
			"run_id": t.runID, "stack": observed.key.Stack, "error": err.Error()})
	}
}

// label renders a Key the way an operator reads it.
func label(key state.Key) string {
	if key.Service == "" {
		return key.Stack
	}
	return key.Stack + "/" + key.Service
}
