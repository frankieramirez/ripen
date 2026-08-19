// Package backend is the orchestrator seam of the Transaction: the port
// every backend (Portainer API, compose runtimes) implements, reshaped
// around what one Transaction needs — observe a stack, deploy a compose
// document, and prove the stack's services are running. Drift comparison
// stays in the updater core, which is why observation carries a
// fingerprint rather than the pieces it was computed from.
package backend

import (
	"fmt"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

// StackState is one stack as a backend sees it at one instant.
type StackState struct {
	Backend domain.Backend
	// Stack is the policy-declared name, the identity used everywhere.
	Stack string
	// Compose is the deployed Compose document as text — the bytes an
	// apply rewrites and a rollback restores.
	Compose string
	// Fingerprint covers everything an apply depends on staying still.
	// Any change between planning and applying is drift.
	Fingerprint string
	// Services are the resolved service names, sorted.
	Services []string
	// ServiceImages is each service's image reference with variable
	// interpolation applied — what the engine will actually run.
	ServiceImages map[string]string
	// DeclaredImages is each service's image reference as literally
	// written in the Compose document. It differs from ServiceImages
	// only for interpolated lines, which cannot be pinned.
	DeclaredImages map[string]string
	// RunningDigests maps service name to the digest currently running.
	// Nil when the backend cannot prove running digests and only the
	// stack-level image status is available.
	RunningDigests map[string]string
	// ImageStatus is the backend's own "updated"/"outdated" verdict, set
	// only when RunningDigests is nil.
	ImageStatus string
	// GitBacked marks a stack deployed from Git, which is never mutated
	// in place — changes go through a Proposal.
	GitBacked bool
	// Handle carries backend-private identity (a Portainer stack, a
	// compose file and project) back into Deploy.
	Handle any
}

// Port is the backend seam. Implementations are the Portainer API
// adapter and the compose-runtime adapter.
type Port interface {
	// Preflight proves the backend is reachable and acting as the
	// identity the policy expects, before a run does any inventory work.
	Preflight() error
	// Observe reads the stack's current state through the backend.
	Observe(stack config.StackPolicy) (StackState, error)
	// RunningDigests re-reads only which digests are running, for the
	// repeated checks verification makes.
	RunningDigests(state StackState) (map[string]string, error)
	// Deploy redeploys the stack with a new Compose document. repull
	// asks the engine to re-pull mutable tags; backends that always pin
	// digests refuse it.
	Deploy(state StackState, compose string, repull bool) error
	// ServicesRunning reports whether every configured service of the
	// stack is running (and healthy where the engine tracks health),
	// with a human-readable detail when it is not. It is the engine-level
	// half of verification; the functional health check is the other.
	ServicesRunning(state StackState) (bool, string, error)
}

// IneligibleError marks a stack the Transaction must refuse to act on —
// a policy/reality mismatch rather than a failure. It maps to
// ResultCode ineligible.
type IneligibleError struct {
	Reason string
}

func (e *IneligibleError) Error() string { return e.Reason }

// Ineligible builds an IneligibleError.
func Ineligible(format string, args ...any) error {
	return &IneligibleError{Reason: fmt.Sprintf(format, args...)}
}

// NotVisibleError marks a stack the policy declares but the backend
// cannot see — usually a stack the automation identity has no access to.
// It maps to ResultCode not_visible.
type NotVisibleError struct {
	Stack string
}

func (e *NotVisibleError) Error() string {
	return fmt.Sprintf("stack %q is not visible to the automation identity", e.Stack)
}

// NotVisible builds a NotVisibleError.
func NotVisible(stack string) error {
	return &NotVisibleError{Stack: stack}
}

// EngineUnavailableError marks a backend that could not be reached or
// spoken to at all — a missing binary, a dead socket, an unusable API.
// It maps to ResultCode engine_unavailable, never to a stack fault.
type EngineUnavailableError struct {
	Engine string
	Err    error
}

func (e *EngineUnavailableError) Error() string {
	return fmt.Sprintf("%s engine is unavailable: %v", e.Engine, e.Err)
}

func (e *EngineUnavailableError) Unwrap() error { return e.Err }

// EngineUnavailable builds an EngineUnavailableError.
func EngineUnavailable(engine string, err error) error {
	return &EngineUnavailableError{Engine: engine, Err: err}
}
