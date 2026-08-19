// Package updater is the Transaction engine: the deep module that owns
// one complete Monitor or Apply run. Everything fail-closed lives here —
// baselining only what it can prove is running, maturing Candidates
// before acting, applying at most one Service per run, verifying,
// rolling back, and opening the Circuit breaker when verification fails.
//
// Backends, the registry, health checks, state, and the Event stream are
// ports; this package never talks to a network or a filesystem itself.
package updater

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/proposal"
	"github.com/frankieramirez/ripen/internal/registry"
	"github.com/frankieramirez/ripen/internal/state"
)

// Registry resolves what digest an image tag currently points at.
type Registry interface {
	ResolveDigest(image domain.ImageReference) (string, error)
	ResolvePlatformDigest(image domain.ImageReference, platform registry.Platform) (string, error)
}

// Health runs one functional health check.
type Health interface {
	Check(policy config.HealthPolicy) (bool, error)
}

// Clock is the engine's view of time, faked in tests.
type Clock interface {
	Now() time.Time
	Sleep(duration time.Duration)
}

// EventSink receives Events. The narrow shape here is what the engine
// needs; the Event envelope and the full catalogue arrive with the
// daemon and Notifier PR of the migration plan (docs/rework/SPEC.md).
type EventSink interface {
	Emit(name string, fields map[string]any)
}

// The failures a caller has to tell apart: a name that does not exist,
// and a stack that exists but cannot be proposed for.
var (
	// ErrUnknownStack means no stack by that name is configured.
	ErrUnknownStack = errors.New("no such stack is configured")
	// ErrNotProposable means the stack exists but has no proposal path:
	// no git_path, or no forge configured.
	ErrNotProposable = errors.New("this stack has no proposal configuration")
)

// Result is one Service's outcome in a run.
type Result struct {
	Key    state.Key
	Code   domain.ResultCode
	Detail string
	Digest string
	// Proposal is set only on a proposed result: the Proposal that now
	// exists, whether or not this run is what created it.
	Proposal *proposal.Result
}

// Report is one complete run.
type Report struct {
	RunID          string
	Mode           domain.Mode
	Actor          domain.Actor
	Started        time.Time
	Finished       time.Time
	Results        []Result
	UpdatesApplied int
	BreakerOpen    bool
}

// Options builds an Updater.
type Options struct {
	Policy    *config.Policy
	Backends  map[domain.Backend]backend.Port
	Registry  Registry
	Health    Health
	State     *state.Store
	Proposals proposal.Port
	Events    EventSink
	Clock     Clock
	Actor     domain.Actor
}

// Updater runs Transactions.
type Updater struct {
	policy    *config.Policy
	backends  map[domain.Backend]backend.Port
	registry  Registry
	health    Health
	state     *state.Store
	proposals proposal.Port
	events    EventSink
	clock     Clock
	actor     domain.Actor
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Sleep blocks for the given duration.
func (SystemClock) Sleep(duration time.Duration) { time.Sleep(duration) }

type discardSink struct{}

func (discardSink) Emit(string, map[string]any) {}

// New builds an Updater.
func New(options Options) (*Updater, error) {
	if options.Policy == nil {
		return nil, errors.New("an updater needs a policy")
	}
	if options.State == nil {
		return nil, errors.New("an updater needs a state store")
	}
	if options.Registry == nil || options.Health == nil {
		return nil, errors.New("an updater needs a registry and a health checker")
	}
	if options.Clock == nil {
		options.Clock = SystemClock{}
	}
	if options.Events == nil {
		options.Events = discardSink{}
	}
	if options.Actor == "" {
		options.Actor = domain.ActorCLI
	}
	return &Updater{
		policy:    options.Policy,
		backends:  options.Backends,
		registry:  options.Registry,
		health:    options.Health,
		state:     options.State,
		proposals: options.Proposals,
		events:    options.Events,
		clock:     options.Clock,
		actor:     options.Actor,
	}, nil
}

// emit sends one Event. A sink that fails, blocks on nothing, or panics
// must never change what a Transaction decided or reported, so the
// failure stops here.
func (u *Updater) emit(name string, fields map[string]any) {
	defer func() { _ = recover() }()
	u.events.Emit(name, fields)
}

// Status reads the durable state snapshot.
func (u *Updater) Status() (state.Status, error) {
	return u.state.Status(u.clock.Now())
}

// ClearBreaker closes the Circuit breaker. The reason is mandatory and
// recorded: an operator has to say what they fixed.
func (u *Updater) ClearBreaker(reason string) (state.Status, error) {
	if err := u.state.ClearBreaker(reason, u.clock.Now()); err != nil {
		return state.Status{}, err
	}
	u.emit("breaker.cleared", map[string]any{"reason": reason})
	return u.Status()
}

// ClearProposal drops a reviewed, stale pending Proposal so the Service
// can propose again.
func (u *Updater) ClearProposal(stack, reason string) (state.Status, error) {
	if stack == "" || reason == "" {
		return state.Status{}, errors.New("a stack and a reason are required")
	}
	cleared := false
	for _, key := range u.proposalKeys(stack) {
		removed, err := u.state.ClearPendingProposal(key)
		if err != nil {
			return state.Status{}, err
		}
		cleared = cleared || removed
	}
	if !cleared {
		return state.Status{}, fmt.Errorf("no pending proposal exists for %q", stack)
	}
	u.emit("proposal.cleared", map[string]any{"stack": stack, "reason": reason})
	return u.Status()
}

// proposalKeys enumerates every state Key a stack's Proposals can live
// under: the stack-level key and one per configured Service.
func (u *Updater) proposalKeys(stack string) []state.Key {
	for _, policy := range u.policy.Stacks {
		if policy.Name != stack {
			continue
		}
		keys := []state.Key{{Backend: policy.Backend, Stack: stack}}
		for _, service := range policy.Services {
			keys = append(keys, state.Key{Backend: policy.Backend, Stack: stack, Service: service.Name})
		}
		return keys
	}
	return nil
}

// Run executes one Transaction over every enabled stack. The run holds
// the state lease for its whole life, so two Ripen processes can never
// act at once.
func (u *Updater) Run(mode domain.Mode) (Report, error) {
	started := u.clock.Now()
	report := Report{RunID: newRunID(), Mode: mode, Actor: u.actor, Started: started}

	token, acquired, err := u.state.AcquireLease(started, u.policy.LeaseTTLSeconds)
	if err != nil {
		return Report{}, err
	}
	if !acquired {
		report.Finished = u.clock.Now()
		report.Results = []Result{{
			Key:    state.Key{Stack: "*"},
			Code:   domain.ResultBusy,
			Detail: "another run holds the lease",
		}}
		return report, nil
	}
	defer func() { _ = u.state.ReleaseLease(token) }()

	status, err := u.state.Status(started)
	if err != nil {
		return Report{}, err
	}
	if mode == domain.ModeApply && status.BreakerOpen {
		// An open breaker halts every outbound action — Apply and
		// Proposal alike. Monitor observation and reads continue.
		report.Finished = u.clock.Now()
		report.BreakerOpen = true
		report.Results = []Result{{
			Key:    state.Key{Stack: "*"},
			Code:   domain.ResultBreakerOpen,
			Detail: breakerDetail(status),
		}}
		return report, nil
	}

	u.emit("run.started", map[string]any{
		"run_id": report.RunID, "mode": string(mode), "actor": string(u.actor)})

	unavailable, err := u.preflight()
	if err != nil {
		return Report{}, err
	}

	// Policy order is document order: with one update per run, the order
	// stacks are declared in decides which mature Candidate goes first.
	for _, stack := range u.policy.Stacks {
		if !stack.Enabled {
			continue
		}
		if reason, down := unavailable[stack.Backend]; down {
			report.Results = append(report.Results, Result{
				Key:    state.Key{Backend: stack.Backend, Stack: stack.Name},
				Code:   domain.ResultEngineUnavailable,
				Detail: reason,
			})
			continue
		}
		transaction := &transaction{updater: u, stack: stack, runID: report.RunID, mode: mode}
		results, applied := transaction.run(u.policy.MaxUpdatesPerRun - report.UpdatesApplied)
		report.Results = append(report.Results, results...)
		report.UpdatesApplied += applied
	}

	report.Finished = u.clock.Now()
	final, err := u.state.Status(report.Finished)
	if err != nil {
		return Report{}, err
	}
	report.BreakerOpen = final.BreakerOpen
	u.emit("run.finished", map[string]any{
		"run_id":       report.RunID,
		"mode":         string(mode),
		"updates":      report.UpdatesApplied,
		"breaker_open": report.BreakerOpen,
		"results":      len(report.Results),
	})
	return report, nil
}

// preflight proves each backend in use is usable before any inventory
// work. An unusable engine takes its own stacks out of the run; a
// backend that refuses on identity or credentials fails the whole run,
// because acting on the wrong account is never a per-stack problem.
func (u *Updater) preflight() (map[domain.Backend]string, error) {
	unavailable := map[domain.Backend]string{}
	checked := map[domain.Backend]bool{}
	for _, stack := range u.policy.Stacks {
		if !stack.Enabled || checked[stack.Backend] {
			continue
		}
		checked[stack.Backend] = true
		port, ok := u.backends[stack.Backend]
		if !ok || port == nil {
			unavailable[stack.Backend] = fmt.Sprintf("no %s backend is configured", stack.Backend)
			continue
		}
		err := port.Preflight()
		if err == nil {
			continue
		}
		var engine *backend.EngineUnavailableError
		if errors.As(err, &engine) {
			unavailable[stack.Backend] = err.Error()
			continue
		}
		return nil, err
	}
	return unavailable, nil
}

func breakerDetail(status state.Status) string {
	if status.BreakerReason != "" {
		return status.BreakerReason
	}
	return "the circuit breaker is open"
}

// newRunID mints the run's UUIDv7. A failed mint is not worth failing a
// run over: a random v4 is still unique, it only loses time ordering.
func newRunID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// Propose opens a Proposal for one stack's matured Candidate outside a
// run. Its preconditions are the same ones a run would apply — a
// configured git_path, a closed breaker, a matured Candidate, and no
// Proposal already under review — because the verb is the run's
// proposal step, not a way around it.
func (u *Updater) Propose(stackName string) (Result, string, error) {
	runID := newRunID()
	index := slices.IndexFunc(u.policy.Stacks, func(stack config.StackPolicy) bool {
		return stack.Name == stackName
	})
	if index < 0 {
		return Result{}, runID, fmt.Errorf("%w: %q", ErrUnknownStack, stackName)
	}
	stack := u.policy.Stacks[index]
	key := state.Key{Backend: stack.Backend, Stack: stack.Name}
	if stack.GitPath == "" {
		return Result{}, runID, fmt.Errorf("%w: stack %q has no git_path", ErrNotProposable, stackName)
	}
	if u.proposals == nil {
		return Result{}, runID, fmt.Errorf("%w: no github section is configured", ErrNotProposable)
	}

	started := u.clock.Now()
	token, acquired, err := u.state.AcquireLease(started, u.policy.LeaseTTLSeconds)
	if err != nil {
		return Result{}, runID, err
	}
	if !acquired {
		return Result{Key: key, Code: domain.ResultBusy, Detail: "another run holds the lease"}, runID, nil
	}
	defer func() { _ = u.state.ReleaseLease(token) }()

	status, err := u.state.Status(started)
	if err != nil {
		return Result{}, runID, err
	}
	if status.BreakerOpen {
		return Result{Key: key, Code: domain.ResultBreakerOpen, Detail: breakerDetail(status)}, runID, nil
	}

	transaction := &transaction{updater: u, stack: stack, runID: runID, mode: domain.ModeApply}
	if err := transaction.port().Preflight(); err != nil {
		return transaction.failure(key, err), runID, nil
	}
	stackState, err := transaction.port().Observe(stack)
	if err != nil {
		return transaction.failure(key, err), runID, nil
	}
	observations, err := transaction.observe(stackState)
	if err != nil {
		return transaction.failure(key, err), runID, nil
	}

	for _, observed := range observations {
		accepted, found, err := u.state.AcceptedDigest(observed.key)
		if err != nil {
			return Result{}, runID, err
		}
		if !found || observed.remoteDigest == accepted {
			continue
		}
		mature, err := u.matured(observed.key, observed.remoteDigest, started)
		if err != nil {
			return Result{}, runID, err
		}
		if !mature {
			continue
		}
		result, _ := transaction.propose(observed, stackState, accepted)
		return result, runID, nil
	}
	return Result{
		Key:    key,
		Code:   domain.ResultIneligible,
		Detail: "no matured candidate is waiting for a proposal",
	}, runID, nil
}

// matured applies the maturity rule to a stored Candidate: two
// observations of the same digest, and the window elapsed.
func (u *Updater) matured(key state.Key, digest string, now time.Time) (bool, error) {
	candidate, err := u.state.Candidate(key)
	if err != nil || candidate == nil || candidate.Digest != digest {
		return false, err
	}
	window := time.Duration(u.policy.CandidateMinAgeSeconds) * time.Second
	return candidate.Count >= 2 && !now.Before(candidate.FirstSeen.Add(window)), nil
}
