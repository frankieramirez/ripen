// Package compose is the compose-runtime backend: one adapter driving a
// compose CLI, with docker-compose and podman-compose as thin
// constructors over the same code. Podman goes through `podman compose`,
// the engine's own wrapper, never the third-party podman-compose tool.
//
// The connection is the compose CLI itself. A rootless socket may be
// pointed at with DOCKER_HOST/CONTAINER_HOST; a socket resolving to the
// privileged docker socket is refused at config load, not here.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/composefile"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

var pinnedImagePattern = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)

// Runner runs one engine command. Production runs a subprocess; tests
// answer from a script.
type Runner interface {
	Run(ctx context.Context, binary string, args, env []string) ([]byte, error)
}

// Handle is the compose adapter's stack identity, carried on StackState
// from Observe into Deploy.
type Handle struct {
	File     string
	Project  string
	EnvFiles []string
}

// Adapter is the compose-runtime backend.
type Adapter struct {
	backendName   domain.Backend
	binary        string
	socketVar     string
	socket        string
	runner        Runner
	timeout       time.Duration
	deployTimeout time.Duration
	probed        bool
}

// Option adjusts an Adapter.
type Option func(*Adapter)

// WithRunner replaces the command runner.
func WithRunner(runner Runner) Option {
	return func(a *Adapter) { a.runner = runner }
}

// WithTimeouts sets the per-command and deployment timeouts.
func WithTimeouts(command, deploy time.Duration) Option {
	return func(a *Adapter) {
		a.timeout = command
		a.deployTimeout = deploy
	}
}

// NewDocker builds the Docker Compose adapter.
func NewDocker(settings config.EngineSettings, options ...Option) *Adapter {
	return newAdapter(domain.BackendDockerCompose, settings, "docker", "DOCKER_HOST", options)
}

// NewPodman builds the Podman Compose adapter.
func NewPodman(settings config.EngineSettings, options ...Option) *Adapter {
	return newAdapter(domain.BackendPodmanCompose, settings, "podman", "CONTAINER_HOST", options)
}

func newAdapter(name domain.Backend, settings config.EngineSettings,
	defaultBinary, socketVar string, options []Option) *Adapter {
	adapter := &Adapter{
		backendName:   name,
		binary:        settings.Binary,
		socketVar:     socketVar,
		socket:        settings.Socket,
		runner:        execRunner{},
		timeout:       60 * time.Second,
		deployTimeout: 600 * time.Second,
	}
	if adapter.binary == "" {
		adapter.binary = defaultBinary
	}
	for _, option := range options {
		option(adapter)
	}
	return adapter
}

// Backend reports which backend this adapter serves.
func (a *Adapter) Backend() domain.Backend { return a.backendName }

// Preflight probes the engine. There is no identity to check: the
// compose CLI acts as whoever runs Ripen.
func (a *Adapter) Preflight() error { return a.probe() }

// RunningDigests re-reads which digests the project is running.
func (a *Adapter) RunningDigests(state backend.StackState) (map[string]string, error) {
	handle, ok := state.Handle.(Handle)
	if !ok {
		return nil, errors.New("compose digest discovery needs a compose stack observation")
	}
	return a.digestsFor(handle)
}

// Observe reads the stack's current state: the Compose document on disk,
// the resolved services, and the digests actually running.
func (a *Adapter) Observe(stack config.StackPolicy) (backend.StackState, error) {
	if stack.Backend != a.backendName {
		return backend.StackState{}, fmt.Errorf("stack %q is not a %s stack", stack.Name, a.backendName)
	}
	if err := a.probe(); err != nil {
		return backend.StackState{}, err
	}

	raw, err := os.ReadFile(stack.File) // #nosec G304 -- the compose path is operator-supplied by design
	if err != nil {
		return backend.StackState{}, backend.Ineligible("compose file %s cannot be read: %v", stack.File, err)
	}
	compose := string(raw)
	if err := preflightWritable(stack.File); err != nil {
		return backend.StackState{}, err
	}

	resolved, err := a.resolveConfig(stack)
	if err != nil {
		return backend.StackState{}, err
	}
	services := make([]string, 0, len(resolved.Services))
	images := map[string]string{}
	for name, service := range resolved.Services {
		services = append(services, name)
		if service.Image != "" {
			images[name] = service.Image
		}
	}
	slices.Sort(services)

	declared := map[string]string{}
	for _, name := range services {
		if image, err := composefile.ServiceImage(compose, name); err == nil {
			declared[name] = image
		}
	}

	envFiles, envBytes, err := envFileContents(stack.File, compose)
	if err != nil {
		return backend.StackState{}, err
	}

	running, err := a.digestsFor(Handle{File: stack.File, Project: stack.Project})
	if err != nil {
		return backend.StackState{}, err
	}

	return backend.StackState{
		Backend:        a.backendName,
		Stack:          stack.Name,
		Compose:        compose,
		Fingerprint:    fingerprint(raw, envFiles, envBytes, services),
		Services:       services,
		ServiceImages:  images,
		DeclaredImages: declared,
		RunningDigests: running,
		Handle:         Handle{File: stack.File, Project: stack.Project, EnvFiles: envFiles},
	}, nil
}

// Deploy writes the Compose document and brings the project up. The file
// is replaced atomically and always before the engine is asked to
// converge, so a rollback restores the previous bytes even if the engine
// call then fails.
func (a *Adapter) Deploy(state backend.StackState, compose string, repull bool) error {
	handle, ok := state.Handle.(Handle)
	if !ok {
		return errors.New("compose deploy needs a compose stack observation")
	}
	if repull {
		return errors.New("the compose backend pins digests and has no repull path")
	}
	if err := a.probe(); err != nil {
		return err
	}
	if err := writeAtomically(handle.File, compose); err != nil {
		return err
	}
	_, err := a.run(a.deployTimeout, a.projectArgs(handle, "up", "--detach", "--no-build")...)
	return err
}

// ServicesRunning reports whether every service of the project is
// running, and healthy wherever the engine tracks a healthcheck. It is
// conjunctive by design: one stopped sibling blocks the Transaction.
func (a *Adapter) ServicesRunning(state backend.StackState) (bool, string, error) {
	handle, ok := state.Handle.(Handle)
	if !ok {
		return false, "", errors.New("compose verification needs a compose stack observation")
	}
	containers, err := a.projectContainers(handle)
	if err != nil {
		return false, "", err
	}
	for _, name := range state.Services {
		container, present := containers[name]
		switch {
		case !present:
			return false, fmt.Sprintf("service %q has no container", name), nil
		case container.State != "running":
			return false, fmt.Sprintf("service %q is %s", name, container.State), nil
		case container.Health != "" && container.Health != "healthy":
			return false, fmt.Sprintf("service %q is %s", name, container.Health), nil
		}
	}
	return true, "", nil
}

type resolvedConfig struct {
	Services map[string]struct {
		Image string `json:"image"`
	} `json:"services"`
}

type container struct {
	Service string `json:"Service"`
	Image   string `json:"Image"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

func (a *Adapter) probe() error {
	if a.probed {
		return nil
	}
	output, err := a.run(a.timeout, "compose", "version", "--format", "json")
	if err != nil {
		return backend.EngineUnavailable(string(a.backendName), err)
	}
	var payload any
	if err := json.Unmarshal(bytes.TrimSpace(output), &payload); err != nil {
		return backend.EngineUnavailable(string(a.backendName),
			fmt.Errorf("%s compose does not support --format json", a.binary))
	}
	a.probed = true
	return nil
}

func (a *Adapter) resolveConfig(stack config.StackPolicy) (resolvedConfig, error) {
	handle := Handle{File: stack.File, Project: stack.Project}
	output, err := a.run(a.timeout, a.projectArgs(handle, "config", "--format", "json")...)
	if err != nil {
		return resolvedConfig{}, backend.Ineligible(
			"compose configuration for %s could not be resolved: %v", stack.Name, err)
	}
	var resolved resolvedConfig
	if err := json.Unmarshal(output, &resolved); err != nil {
		return resolvedConfig{}, backend.Ineligible(
			"compose configuration for %s is not readable JSON: %v", stack.Name, err)
	}
	if len(resolved.Services) == 0 {
		return resolvedConfig{}, backend.Ineligible("compose file for %s declares no services", stack.Name)
	}
	return resolved, nil
}

func (a *Adapter) digestsFor(handle Handle) (map[string]string, error) {
	containers, err := a.projectContainers(handle)
	if err != nil {
		return nil, err
	}
	digests := map[string]string{}
	for name, entry := range containers {
		if entry.State != "running" {
			continue
		}
		digest, err := a.imageDigest(entry.Image, name)
		if err != nil {
			return nil, err
		}
		digests[name] = digest
	}
	return digests, nil
}

func (a *Adapter) projectContainers(handle Handle) (map[string]container, error) {
	output, err := a.run(a.timeout, a.projectArgs(handle, "ps", "--format", "json")...)
	if err != nil {
		return nil, backend.EngineUnavailable(string(a.backendName), err)
	}
	entries, err := decodeContainers(output)
	if err != nil {
		return nil, err
	}
	containers := map[string]container{}
	for _, entry := range entries {
		if entry.Service == "" {
			return nil, errors.New("the compose engine reported a container with no service name")
		}
		if _, duplicate := containers[entry.Service]; duplicate {
			return nil, fmt.Errorf("the compose engine reported two containers for service %q", entry.Service)
		}
		containers[entry.Service] = entry
	}
	return containers, nil
}

func decodeContainers(output []byte) ([]container, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var entries []container
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("compose ps output is not readable JSON: %w", err)
		}
		return entries, nil
	}
	var entries []container
	for line := range bytes.Lines(trimmed) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry container
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("compose ps output is not readable JSON: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *Adapter) imageDigest(image, service string) (string, error) {
	if match := pinnedImagePattern.FindStringSubmatch(image); match != nil {
		return match[1], nil
	}
	if image == "" {
		return "", fmt.Errorf("service %q is running an image with no reference", service)
	}
	output, err := a.run(a.timeout, "image", "inspect", image, "--format", "{{json .RepoDigests}}")
	if err != nil {
		return "", backend.EngineUnavailable(string(a.backendName), err)
	}
	var references []string
	if err := json.Unmarshal(bytes.TrimSpace(output), &references); err != nil {
		return "", fmt.Errorf("image inspect output for service %q is not readable JSON: %w", service, err)
	}
	var digests []string
	for _, reference := range references {
		if at := strings.LastIndex(reference, "@"); at >= 0 {
			digest := reference[at+1:]
			if strings.HasPrefix(digest, "sha256:") && !slices.Contains(digests, digest) {
				digests = append(digests, digest)
			}
		}
	}
	if len(digests) != 1 {
		return "", fmt.Errorf("the image running service %q has no unique digest", service)
	}
	return digests[0], nil
}

func (a *Adapter) projectArgs(handle Handle, verb ...string) []string {
	args := []string{"compose", "--project-name", handle.Project, "--file", handle.File}
	return append(args, verb...)
}

func (a *Adapter) run(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.runner.Run(ctx, a.binary, args, a.environment())
}

func (a *Adapter) environment() []string {
	if a.socket == "" {
		return nil
	}
	value := a.socket
	if !strings.Contains(value, "://") {
		value = "unix://" + value
	}
	return []string{a.socketVar + "=" + value}
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args, env []string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- the engine binary is operator-configured
	command.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", binary, strings.Join(args, " "), detail)
	}
	return stdout.Bytes(), nil
}

func preflightWritable(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0) // #nosec G304 -- the compose path is operator-supplied by design
	if err != nil {
		return backend.Ineligible("compose file %s is not writable: %v", path, err)
	}
	if err := file.Close(); err != nil {
		return backend.Ineligible("compose file %s is not writable: %v", path, err)
	}
	probe, err := os.CreateTemp(filepath.Dir(path), ".ripen-preflight-*")
	if err != nil {
		return backend.Ineligible("compose directory for %s is not writable: %v", path, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return backend.Ineligible("compose directory for %s is not writable: %v", path, err)
	}
	return nil
}

func writeAtomically(path, content string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ripen-compose-*")
	if err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing compose file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing compose file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("writing compose file: %w", err)
	}
	return nil
}

func envFileContents(composePath, compose string) ([]string, [][]byte, error) {
	declared, err := declaredEnvFiles(composePath, compose)
	if err != nil {
		return nil, nil, err
	}
	implicit := filepath.Join(filepath.Dir(composePath), ".env")
	if _, err := os.Stat(implicit); err == nil && !slices.Contains(declared, implicit) {
		declared = append(declared, implicit)
	}
	slices.Sort(declared)

	contents := make([][]byte, 0, len(declared))
	for _, path := range declared {
		content, err := os.ReadFile(path) // #nosec G304 -- env file paths come from the operator's compose file
		if err != nil {
			return nil, nil, backend.Ineligible("compose env file %s cannot be read: %v", path, err)
		}
		contents = append(contents, content)
	}
	return declared, contents, nil
}

func declaredEnvFiles(composePath, compose string) ([]string, error) {
	var document struct {
		Services map[string]struct {
			EnvFile yaml.Node `yaml:"env_file"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		return nil, backend.Ineligible("compose file %s is not valid YAML: %v", composePath, err)
	}
	directory := filepath.Dir(composePath)
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	for _, service := range document.Services {
		node := service.EnvFile
		switch node.Kind {
		case 0:
		case yaml.ScalarNode:
			add(node.Value)
		case yaml.SequenceNode:
			for _, item := range node.Content {
				switch item.Kind {
				case yaml.ScalarNode:
					add(item.Value)
				case yaml.MappingNode:
					for i := 0; i+1 < len(item.Content); i += 2 {
						if item.Content[i].Value == "path" {
							add(item.Content[i+1].Value)
						}
					}
				}
			}
		default:
			return nil, backend.Ineligible("compose file %s declares an unreadable env_file", composePath)
		}
	}
	return paths, nil
}

func fingerprint(compose []byte, envFiles []string, envBytes [][]byte, services []string) string {
	digest := domain.NewFingerprint()
	digest.AddBytes("compose", compose)
	for i, path := range envFiles {
		digest.AddBytes("env:"+path, envBytes[i])
	}
	digest.Add("services", strings.Join(services, ","))
	return digest.Sum()
}
