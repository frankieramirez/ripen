// Package app assembles Ripen from its parts and answers the reads.
// Every surface — CLI, MCP, Web UI — builds the same App, so they cannot
// answer the same question differently.
//
// Reads need only the policy and the state store. The network clients
// and credentials live behind Updater, which the read paths never call:
// that is what lets a read-only surface run without loading a single
// secret.
package app

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/compose"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/github"
	"github.com/frankieramirez/ripen/internal/health"
	"github.com/frankieramirez/ripen/internal/notifier"
	"github.com/frankieramirez/ripen/internal/portainer"
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
	"github.com/frankieramirez/ripen/internal/updater"
	"github.com/frankieramirez/ripen/internal/version"
)

// App is a loaded policy and an open state store.
type App struct {
	Policy *config.Policy
	Store  *state.Store
	Clock  updater.Clock
}

// Open loads the policy and opens the state store it names.
func Open(configPath string) (*App, error) {
	policy, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	store, err := state.Open(policy.StateFile)
	if err != nil {
		return nil, err
	}
	return &App{Policy: policy, Store: store, Clock: updater.SystemClock{}}, nil
}

// Close releases the state store.
func (a *App) Close() error {
	return a.Store.Close()
}

// Events builds the Event stream for one surface: the structured sink,
// which is always on and writes every Event, plus the webhook Notifier
// when the policy configures one. The returned Webhook is nil when no
// Notifier is configured; close it to drain what is queued.
func (a *App) Events(actor domain.Actor, stream io.Writer) (*event.Stream, *notifier.Webhook, error) {
	events := event.NewStream(actor, event.NewWriterSink(stream))
	if a.Policy.Notifier == nil || a.Policy.Notifier.Webhook == nil {
		// Absent notifier configuration is silent-but-logging, not
		// silent: the stream still records everything.
		return events, nil, nil
	}
	webhook, err := notifier.New(notifier.Options{
		Settings:  *a.Policy.Notifier.Webhook,
		Heartbeat: time.Duration(a.Policy.Notifier.HeartbeatIntervalSeconds) * time.Second,
		Store:     a.Store,
		Stream:    events,
	})
	if err != nil {
		return nil, nil, err
	}
	events.Add(webhook)
	return events, webhook, nil
}

// NotifierHealth reports what is durable about Notifier delivery, plus
// this process's in-memory drop count.
func (a *App) NotifierHealth(dropped int) (response.NotifierHealth, error) {
	health, err := a.Store.NotifierHealth()
	if err != nil {
		return response.NotifierHealth{}, err
	}
	reported := response.NotifierHealth{
		ConsecutiveFailures: health.ConsecutiveFailures,
		DroppedSinceStart:   dropped,
	}
	if health.LastSuccessAt != nil {
		stamp := response.Stamp(*health.LastSuccessAt)
		reported.LastSuccessAt = &stamp
	}
	return reported, nil
}

// Updater builds the write path: backend clients, the registry client,
// health checks, and the forge adapter. Calling this is what loads
// credentials, so a read-only surface must not.
func (a *App) Updater(actor domain.Actor, events updater.EventSink) (*updater.Updater, error) {
	backends := map[domain.Backend]backend.Port{}
	for _, stack := range a.Policy.Stacks {
		if !stack.Enabled {
			continue
		}
		if _, built := backends[stack.Backend]; built {
			continue
		}
		port, err := a.backendFor(stack.Backend)
		if err != nil {
			return nil, err
		}
		backends[stack.Backend] = port
	}

	var proposals *github.Adapter
	if a.Policy.GitHub != nil {
		adapter, err := github.New(github.Options{
			Repository: a.Policy.GitHub.Repository,
			BaseBranch: a.Policy.GitHub.BaseBranch,
			TokenFile:  a.Policy.GitHub.TokenFile,
		})
		if err != nil {
			return nil, err
		}
		proposals = adapter
	}

	options := updater.Options{
		Policy:   a.Policy,
		Backends: backends,
		Registry: registry.New(),
		Health:   health.New(),
		State:    a.Store,
		Events:   events,
		Clock:    a.Clock,
		Actor:    actor,
	}
	// A nil *github.Adapter in an interface is not a nil interface, and
	// the engine tests "are proposals configured" on the interface.
	if proposals != nil {
		options.Proposals = proposals
	}
	return updater.New(options)
}

func (a *App) backendFor(name domain.Backend) (backend.Port, error) {
	switch name {
	case domain.BackendPortainer:
		if a.Policy.Portainer == nil {
			return nil, errors.New("the portainer backend is used but not configured")
		}
		adapter, err := portainer.New(portainer.Options{
			BaseURL:           a.Policy.Portainer.BaseURL,
			APIKeyFile:        a.Policy.Portainer.APIKeyFile,
			CAFile:            a.Policy.Portainer.TLSCAFile,
			FingerprintSHA256: a.Policy.Portainer.TLSFingerprintSHA256,
		})
		if err != nil {
			return nil, err
		}
		return portainer.NewBackend(adapter, a.Policy.Portainer.ExpectedUsername), nil
	case domain.BackendDockerCompose:
		return compose.NewDocker(a.Policy.Compose.Docker), nil
	case domain.BackendPodmanCompose:
		return compose.NewPodman(a.Policy.Compose.Podman), nil
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}

// --- the policy's Services ---

// ServiceRef is one Service the policy declares: the state Key it is
// recorded under, plus the rules that apply to it. Reads are driven by
// this list, not by what the state store happens to contain, so a
// configured-but-never-observed Service still appears.
type ServiceRef struct {
	Key       state.Key
	Stack     config.StackPolicy
	Enabled   bool
	AutoApply bool
	Health    config.HealthPolicy
}

// Services enumerates every configured Service in policy order.
func (a *App) Services() []ServiceRef {
	var refs []ServiceRef
	for _, stack := range a.Policy.Stacks {
		if len(stack.Services) == 0 {
			health := config.HealthPolicy{}
			if stack.Health != nil {
				health = *stack.Health
			}
			refs = append(refs, ServiceRef{
				Key:       state.Key{Backend: stack.Backend, Stack: stack.Name},
				Stack:     stack,
				Enabled:   true,
				AutoApply: stack.AutoApply,
				Health:    health,
			})
			continue
		}
		for _, service := range stack.Services {
			refs = append(refs, ServiceRef{
				Key: state.Key{
					Backend: stack.Backend, Stack: stack.Name, Service: service.Name,
				},
				Stack:     stack,
				Enabled:   service.Enabled,
				AutoApply: service.AutoApply,
				Health:    service.Health,
			})
		}
	}
	return refs
}

// --- reads ---

// Status answers `ripen status`: policy-driven, with every configured
// Service present whether or not it has ever been observed.
func (a *App) Status() (response.Status, error) {
	now := a.Clock.Now()
	stored, err := a.Store.Status(now)
	if err != nil {
		return response.Status{}, err
	}

	notifierHealth, err := a.NotifierHealth(0)
	if err != nil {
		return response.Status{}, err
	}
	status := response.Status{
		Breaker:         breaker(stored),
		Lease:           response.Lease{Active: stored.LeaseActive},
		Notifier:        notifierHealth,
		Versions:        Versions(),
		EffectivePolicy: a.effectivePolicy(),
		Services:        []response.Service{},
	}
	for _, ref := range a.Services() {
		service := response.Service{
			Identity:  identity(ref.Key),
			Enabled:   ref.Enabled,
			AutoApply: ref.AutoApply,
		}
		baseline, found, err := a.Store.AcceptedDigest(ref.Key)
		if err != nil {
			return response.Status{}, err
		}
		if found {
			service.Baseline = &baseline
		}
		if service.Candidate, err = a.observation(ref.Key, now); err != nil {
			return response.Status{}, err
		}
		if service.PendingProposal, err = a.proposal(ref.Key); err != nil {
			return response.Status{}, err
		}
		attempt, err := a.Store.LastAttempt(ref.Key)
		if err != nil {
			return response.Status{}, err
		}
		if attempt != nil {
			service.LastResult = &response.AttemptSummary{
				RunID:       attempt.RunID,
				Actor:       string(attempt.Actor),
				Result:      string(attempt.Result),
				Detail:      attempt.Detail,
				AttemptedAt: response.Stamp(attempt.AttemptedAt),
			}
		}
		status.Services = append(status.Services, service)
	}
	return status, nil
}

// Candidates answers `ripen candidates`: every Candidate under
// observation, with whether it has matured.
func (a *App) Candidates() (response.Candidates, error) {
	now := a.Clock.Now()
	records, err := a.Store.Candidates()
	if err != nil {
		return response.Candidates{}, err
	}
	candidates := response.Candidates{Candidates: []response.Candidate{}}
	for _, record := range records {
		candidates.Candidates = append(candidates.Candidates, response.Candidate{
			Identity:    identity(record.Key),
			Observation: a.observed(record, now),
		})
	}
	return candidates, nil
}

// Audit answers `ripen audit` from the attempts table — the record of
// what Ripen did, never the Event stream.
func (a *App) Audit(filter state.AuditFilter) (response.Audit, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	// Read one past the page to learn whether another page exists.
	filter.Limit++
	attempts, err := a.Store.AuditPage(filter)
	if err != nil {
		return response.Audit{}, err
	}
	audit := response.Audit{Attempts: []response.Attempt{}}
	if len(attempts) == filter.Limit {
		attempts = attempts[:filter.Limit-1]
		cursor := fmt.Sprintf("%d", attempts[len(attempts)-1].ID)
		audit.NextCursor = &cursor
	}
	for _, attempt := range attempts {
		audit.Attempts = append(audit.Attempts, response.Attempt{
			Identity:    identity(attempt.Key),
			RunID:       attempt.RunID,
			Actor:       string(attempt.Actor),
			Result:      string(attempt.Result),
			Detail:      attempt.Detail,
			OldDigest:   response.Optional(attempt.OldDigest),
			NewDigest:   response.Optional(attempt.NewDigest),
			AttemptedAt: response.Stamp(attempt.AttemptedAt),
		})
	}
	return audit, nil
}

// Explain answers `ripen explain <stack>`: what the next run would do
// with this stack, and what is standing in the way. It reads policy and
// state only — no backend, no registry, no network.
func (a *App) Explain(stack string) (response.Explain, error) {
	index := slices.IndexFunc(a.Policy.Stacks, func(policy config.StackPolicy) bool {
		return policy.Name == stack
	})
	if index < 0 {
		return response.Explain{}, fmt.Errorf("%w: %q", updater.ErrUnknownStack, stack)
	}
	policy := a.Policy.Stacks[index]
	now := a.Clock.Now()
	stored, err := a.Store.Status(now)
	if err != nil {
		return response.Explain{}, err
	}

	explain := response.Explain{
		Backend:          string(policy.Backend),
		Stack:            policy.Name,
		Enabled:          policy.Enabled,
		Excluded:         slices.Contains(a.Policy.ExcludedStacks, policy.Name),
		GitPath:          response.Optional(policy.GitPath),
		ExpectedServices: policy.ExpectedServices,
		Breaker:          breaker(stored),
		Mode:             string(a.Policy.Mode),
		Services:         []response.ExplainService{},
	}
	for _, ref := range a.Services() {
		if ref.Key.Stack != policy.Name {
			continue
		}
		service := response.ExplainService{
			Identity:  identity(ref.Key),
			Enabled:   ref.Enabled,
			AutoApply: ref.AutoApply,
			Health: response.Health{
				Type:           ref.Health.Type,
				Target:         ref.Health.Target,
				AcceptedStatus: ref.Health.AcceptedStatus,
			},
		}
		baseline, found, err := a.Store.AcceptedDigest(ref.Key)
		if err != nil {
			return response.Explain{}, err
		}
		if found {
			service.Baseline = &baseline
		}
		if service.Candidate, err = a.observation(ref.Key, now); err != nil {
			return response.Explain{}, err
		}
		if service.PendingProposal, err = a.proposal(ref.Key); err != nil {
			return response.Explain{}, err
		}
		service.Blockers = a.blockers(explain, service)
		explain.Services = append(explain.Services, service)
	}
	return explain, nil
}

// blockers lists what stands between a Service and an apply, in the
// order a run would hit them.
func (a *App) blockers(explain response.Explain, service response.ExplainService) []string {
	blockers := []string{}
	if !explain.Enabled {
		blockers = append(blockers, "the stack is not enabled")
	}
	if explain.Excluded {
		blockers = append(blockers, "the stack is excluded")
	}
	if explain.Breaker.Open {
		blockers = append(blockers, "the circuit breaker is open")
	}
	if explain.Mode != string(domain.ModeApply) {
		blockers = append(blockers, "the configured mode is monitor")
	}
	if !service.Enabled {
		blockers = append(blockers, "the service is health-only")
	} else if !service.AutoApply {
		blockers = append(blockers, "auto_apply is off for this service")
	}
	if service.PendingProposal != nil {
		blockers = append(blockers, "a proposal is already pending review")
	}
	switch {
	case service.Baseline == nil:
		blockers = append(blockers, "no baseline has been recorded yet")
	case service.Candidate == nil:
		blockers = append(blockers, "no candidate has been observed")
	case !service.Candidate.Mature:
		blockers = append(blockers, "the candidate matures at "+service.Candidate.MatureAt)
	}
	return blockers
}

// Versions reports every independently moving version.
func Versions() response.Versions {
	return response.Versions{
		Ripen:          version.Version,
		Commit:         version.Commit,
		BuiltAt:        version.Date,
		ResponseSchema: response.SchemaVersion,
		EventSchema:    domain.EventSchemaVersion,
		StateSchema:    domain.StateSchemaVersion,
	}
}

func (a *App) effectivePolicy() response.EffectivePolicy {
	backends := []string{}
	for _, stack := range a.Policy.Stacks {
		if name := string(stack.Backend); !slices.Contains(backends, name) {
			backends = append(backends, name)
		}
	}
	slices.Sort(backends)
	return response.EffectivePolicy{
		Mode:                       string(a.Policy.Mode),
		MaxUpdatesPerRun:           a.Policy.MaxUpdatesPerRun,
		CandidateMinAgeSeconds:     a.Policy.CandidateMinAgeSeconds,
		VerificationTimeoutSeconds: a.Policy.VerificationTimeoutSeconds,
		LeaseTTLSeconds:            a.Policy.LeaseTTLSeconds,
		CheckIntervalSeconds:       a.Policy.CheckIntervalSeconds,
		StateFile:                  a.Policy.StateFile,
		Backends:                   backends,
		StackCount:                 len(a.Policy.Stacks),
		ProposalsConfigured:        a.Policy.GitHub != nil,
		NotifierConfigured:         a.Policy.Notifier != nil && a.Policy.Notifier.Webhook != nil,
	}
}

func (a *App) observation(key state.Key, now time.Time) (*response.Observation, error) {
	record, err := a.Store.Candidate(key)
	if err != nil || record == nil {
		return nil, err
	}
	observed := a.observed(*record, now)
	return &observed, nil
}

// observed applies the maturity rule: two observations, and the window
// elapsed since the digest was first seen.
func (a *App) observed(record state.CandidateRecord, now time.Time) response.Observation {
	window := time.Duration(a.Policy.CandidateMinAgeSeconds) * time.Second
	matureAt := record.FirstSeen.Add(window)
	return response.Observation{
		Digest:       record.Digest,
		FirstSeen:    response.Stamp(record.FirstSeen),
		LastSeen:     response.Stamp(record.LastSeen),
		Observations: record.Count,
		Mature:       record.Count >= 2 && !now.Before(matureAt),
		MatureAt:     response.Stamp(matureAt),
	}
}

func (a *App) proposal(key state.Key) (*response.Proposal, error) {
	pending, err := a.Store.PendingProposal(key)
	if err != nil || pending == nil {
		return nil, err
	}
	return &response.Proposal{
		Digest:     pending.Digest,
		URL:        pending.URL,
		ProposedAt: response.Stamp(pending.ProposedAt),
	}, nil
}

func identity(key state.Key) response.Identity {
	return response.Identity{
		Backend: string(key.Backend),
		Stack:   key.Stack,
		Service: response.Optional(key.Service),
	}
}

func breaker(stored state.Status) response.Breaker {
	return response.Breaker{
		Open:   stored.BreakerOpen,
		Reason: response.Optional(stored.BreakerReason),
	}
}
