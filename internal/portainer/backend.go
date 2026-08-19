package portainer

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/composefile"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

// Backend adapts the Portainer API to the orchestrator port.
type Backend struct {
	adapter          *Adapter
	expectedUsername string
}

// NewBackend wraps an Adapter as a backend port. The expected username
// is the identity every run is checked against before it touches
// anything.
func NewBackend(adapter *Adapter, expectedUsername string) *Backend {
	return &Backend{adapter: adapter, expectedUsername: expectedUsername}
}

// Preflight refuses to run at all under the wrong Portainer identity.
// This is not a per-stack problem: a key that authenticates as someone
// else can see and change things the reviewed policy never described.
func (b *Backend) Preflight() error {
	username, err := b.adapter.CurrentUsername()
	if err != nil {
		return err
	}
	if username != b.expectedUsername {
		return fmt.Errorf("the Portainer API key belongs to %q, expected %q",
			username, b.expectedUsername)
	}
	return nil
}

// Observe reads one stack: its Compose document, its services, and
// either the digests running or Portainer's own image-status verdict.
func (b *Backend) Observe(stack config.StackPolicy) (backend.StackState, error) {
	stacks, err := b.adapter.ListStacks()
	if err != nil {
		return backend.StackState{}, err
	}
	index := slices.IndexFunc(stacks, func(entry Stack) bool { return entry.Name == stack.Name })
	if index < 0 {
		return backend.StackState{}, backend.NotVisible(stack.Name)
	}
	entry := stacks[index]
	if entry.Status != 1 {
		return backend.StackState{}, backend.Ineligible("stack %q is not active", stack.Name)
	}

	compose, err := b.adapter.StackFile(entry.ID)
	if err != nil {
		return backend.StackState{}, err
	}
	services, err := composefile.Services(compose)
	if err != nil {
		return backend.StackState{}, backend.Ineligible("%v", err)
	}
	images := map[string]string{}
	for _, name := range services {
		// Portainer stores the document it deploys, so what is written
		// is what runs: declared and resolved are the same reference.
		if image, err := composefile.ServiceImage(compose, name); err == nil {
			images[name] = image
		}
	}

	state := backend.StackState{
		Backend:        domain.BackendPortainer,
		Stack:          stack.Name,
		Compose:        compose,
		Fingerprint:    fingerprint(compose, entry.Env),
		Services:       services,
		ServiceImages:  images,
		DeclaredImages: images,
		GitBacked:      entry.GitBacked,
		Handle:         entry,
	}

	// Running digests are only worth the extra API calls where the
	// Transaction needs per-service truth: a multi-service policy, or a
	// Git-backed stack whose deployment Ripen has to confirm.
	if len(stack.Services) > 0 || entry.GitBacked {
		if state.RunningDigests, err = b.adapter.ServiceImageDigests(entry); err != nil {
			return backend.StackState{}, err
		}
		return state, nil
	}
	if state.ImageStatus, err = b.adapter.ImageStatus(entry.ID); err != nil {
		return backend.StackState{}, err
	}
	return state, nil
}

// RunningDigests re-reads the digests the stack's compose project runs.
func (b *Backend) RunningDigests(state backend.StackState) (map[string]string, error) {
	entry, ok := state.Handle.(Stack)
	if !ok {
		return nil, fmt.Errorf("portainer digest discovery needs a portainer stack observation")
	}
	return b.adapter.ServiceImageDigests(entry)
}

// Deploy redeploys the stack with new Compose content, keeping its
// environment exactly as Portainer holds it.
func (b *Backend) Deploy(state backend.StackState, compose string, repull bool) error {
	entry, ok := state.Handle.(Stack)
	if !ok {
		return fmt.Errorf("portainer deploy needs a portainer stack observation")
	}
	return b.adapter.UpdateStack(entry, compose, entry.Env, repull)
}

// ServicesRunning is always satisfied for Portainer: the API exposes no
// per-service liveness beyond the digest discovery an apply already
// does, so the functional health checks are the whole verification here.
func (b *Backend) ServicesRunning(backend.StackState) (bool, string, error) {
	return true, "", nil
}

// fingerprint covers the Compose document and the stack environment.
// Environment order is Portainer's business, not a change: the parts are
// sorted before hashing so a reordered list is not drift.
func fingerprint(compose string, env []EnvVar) string {
	entries := make([]string, 0, len(env))
	for _, pair := range env {
		encoded, err := json.Marshal([]string{pair.Name, pair.Value})
		if err != nil {
			encoded = []byte(pair.Name + "=" + pair.Value)
		}
		entries = append(entries, string(encoded))
	}
	slices.Sort(entries)

	digest := domain.NewFingerprint()
	digest.Add("compose", compose)
	digest.Add("env", strings.Join(entries, "\n"))
	return digest.Sum()
}
