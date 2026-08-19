// Package domain holds the core vocabulary of Ripen: modes, backends,
// actors, result codes, and image references. Capitalized terms in the
// documentation (Transaction, Baseline, Candidate, ...) are defined in
// CONTEXT.md; the types here are their code form.
package domain

import "fmt"

// Mode selects what a run may do: Monitor observes and baselines only,
// Apply may additionally deploy a mature Candidate.
type Mode string

// The two run modes.
const (
	ModeMonitor Mode = "monitor"
	ModeApply   Mode = "apply"
)

// ParseMode validates a mode string from configuration or the CLI.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case ModeMonitor, ModeApply:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("mode must be monitor or apply, got %q", value)
	}
}

// Backend names the orchestrator a stack is managed by. The enum is closed
// at three values for v1.
type Backend string

// The three v1 backends.
const (
	BackendPortainer     Backend = "portainer"
	BackendDockerCompose Backend = "docker-compose"
	BackendPodmanCompose Backend = "podman-compose"
)

// ParseBackend validates a backend string from configuration.
func ParseBackend(value string) (Backend, error) {
	switch Backend(value) {
	case BackendPortainer, BackendDockerCompose, BackendPodmanCompose:
		return Backend(value), nil
	default:
		return "", fmt.Errorf("backend must be portainer, docker-compose, or podman-compose, got %q", value)
	}
}

// IsCompose reports whether the backend drives a compose engine rather
// than the Portainer API.
func (b Backend) IsCompose() bool {
	return b == BackendDockerCompose || b == BackendPodmanCompose
}

// Actor identifies the surface that performed a write or emitted an Event.
// It is determined by the running surface, never accepted as a parameter.
type Actor string

// The three surfaces that can act.
const (
	ActorCLI    Actor = "cli"
	ActorDaemon Actor = "daemon"
	ActorMCP    Actor = "mcp"
)

// ResultCode is the closed set of per-stack run outcomes. Receivers must
// ignore unknown codes, so additions are non-breaking.
type ResultCode string

// The v1 result codes.
const (
	ResultBaselined         ResultCode = "baselined"
	ResultBaselineBlocked   ResultCode = "baseline_blocked"
	ResultBreakerOpen       ResultCode = "breaker_open"
	ResultBusy              ResultCode = "busy"
	ResultCandidate         ResultCode = "candidate"
	ResultDrifted           ResultCode = "drifted"
	ResultEngineUnavailable ResultCode = "engine_unavailable"
	ResultError             ResultCode = "error"
	ResultExcluded          ResultCode = "excluded"
	ResultIneligible        ResultCode = "ineligible"
	ResultNotVisible        ResultCode = "not_visible"
	ResultProposed          ResultCode = "proposed"
	ResultRollbackFailed    ResultCode = "rollback_failed"
	ResultRolledBack        ResultCode = "rolled_back"
	ResultUpdated           ResultCode = "updated"
	ResultUpToDate          ResultCode = "up_to_date"
)
