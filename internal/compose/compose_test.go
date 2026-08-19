package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/backend"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
)

var (
	digest1 = "sha256:" + strings.Repeat("1", 64)
	digest2 = "sha256:" + strings.Repeat("2", 64)
)

const twoServices = `services:
  web:
    image: ghcr.io/example/web:1.4.0
  sidecar:
    image: ghcr.io/example/sidecar:0.9.1
`

// fakeRunner answers engine commands from a script and records every
// command and environment override it was asked to run.
type fakeRunner struct {
	commands [][]string
	envs     [][]string

	version string
	config  string
	ps      string
	digests map[string]string
	fail    map[string]error
}

func (f *fakeRunner) Run(_ context.Context, binary string, args, env []string) ([]byte, error) {
	f.commands = append(f.commands, append([]string{binary}, args...))
	f.envs = append(f.envs, env)
	verb := verbOf(args)
	if err := f.fail[verb]; err != nil {
		return nil, err
	}
	switch verb {
	case "version":
		return []byte(f.version), nil
	case "config":
		return []byte(f.config), nil
	case "ps":
		return []byte(f.ps), nil
	case "inspect":
		reference := args[2]
		payload, ok := f.digests[reference]
		if !ok {
			return nil, errors.New("no such image: " + reference)
		}
		return []byte(payload), nil
	case "up":
		return nil, nil
	default:
		return nil, errors.New("unscripted command: " + strings.Join(args, " "))
	}
}

func verbOf(args []string) string {
	if len(args) > 1 && args[0] == "image" {
		return "inspect"
	}
	for _, arg := range args {
		switch arg {
		case "version", "config", "ps", "up":
			return arg
		}
	}
	return ""
}

func (f *fakeRunner) ran(verb string) []string {
	for _, command := range f.commands {
		if verbOf(command[1:]) == verb {
			return command
		}
	}
	return nil
}

func newRunner() *fakeRunner {
	return &fakeRunner{
		version: `{"version":"v2.31.0"}`,
		config: `{"services":{"web":{"image":"ghcr.io/example/web:1.4.0"},` +
			`"sidecar":{"image":"ghcr.io/example/sidecar:0.9.1"}}}`,
		ps: `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running","Health":"healthy"},` +
			`{"Service":"sidecar","Image":"ghcr.io/example/sidecar:0.9.1","State":"running","Health":""}]`,
		digests: map[string]string{
			"ghcr.io/example/web:1.4.0":     `["ghcr.io/example/web@` + digest1 + `"]`,
			"ghcr.io/example/sidecar:0.9.1": `["ghcr.io/example/sidecar@` + digest2 + `"]`,
		},
		fail: map[string]error{},
	}
}

func writeCompose(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	return path
}

func stackAt(path string) config.StackPolicy {
	return config.StackPolicy{
		Name:             "media",
		Backend:          domain.BackendDockerCompose,
		Enabled:          true,
		ExpectedServices: []string{"sidecar", "web"},
		File:             path,
		Project:          "media",
	}
}

func observe(t *testing.T, runner *fakeRunner, stack config.StackPolicy) backend.StackState {
	t.Helper()
	state, err := NewDocker(config.EngineSettings{Binary: "docker"}, WithRunner(runner)).Observe(stack)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestObserveReportsResolvedServicesAndRunningDigests(t *testing.T) {
	runner := newRunner()

	state := observe(t, runner, stackAt(writeCompose(t, twoServices)))

	if got := strings.Join(state.Services, ","); got != "sidecar,web" {
		t.Errorf("services = %q, want sorted sidecar,web", got)
	}
	if state.RunningDigests["web"] != digest1 || state.RunningDigests["sidecar"] != digest2 {
		t.Errorf("running digests = %v, want web=%s sidecar=%s", state.RunningDigests, digest1, digest2)
	}
	if state.Compose != twoServices {
		t.Error("observation must carry the compose file as written")
	}
	if state.Backend != domain.BackendDockerCompose || state.Stack != "media" {
		t.Errorf("identity = %s/%s, want docker-compose/media", state.Backend, state.Stack)
	}
}

func TestEveryEngineCommandNamesTheProjectExplicitly(t *testing.T) {
	runner := newRunner()

	observe(t, runner, stackAt(writeCompose(t, twoServices)))

	for _, command := range runner.commands {
		if !slices.Contains(command, "compose") || verbOf(command[1:]) == "version" {
			continue
		}
		index := slices.Index(command, "--project-name")
		if index < 0 || command[index+1] != "media" {
			t.Errorf("command %v does not pass --project-name media", command)
		}
	}
}

func TestAnEngineThatCannotRunIsUnavailableRatherThanAStackFault(t *testing.T) {
	runner := newRunner()
	runner.fail["version"] = errors.New("exec: \"docker\": executable file not found in $PATH")

	_, err := NewDocker(config.EngineSettings{}, WithRunner(runner)).Observe(stackAt(writeCompose(t, twoServices)))

	var unavailable *backend.EngineUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want an EngineUnavailableError", err)
	}
	if unavailable.Engine != string(domain.BackendDockerCompose) {
		t.Errorf("engine = %q, want docker-compose", unavailable.Engine)
	}
}

func TestAnEngineWithoutJSONOutputIsUnavailable(t *testing.T) {
	runner := newRunner()
	runner.version = "Docker Compose version v1.29.2"

	_, err := NewDocker(config.EngineSettings{}, WithRunner(runner)).Observe(stackAt(writeCompose(t, twoServices)))

	var unavailable *backend.EngineUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want an EngineUnavailableError", err)
	}
	if !strings.Contains(err.Error(), "--format json") {
		t.Errorf("error = %v, want it to name the missing JSON support", err)
	}
}

func TestTheEngineIsProbedOnceNotOncePerCall(t *testing.T) {
	runner := newRunner()
	stack := stackAt(writeCompose(t, twoServices))
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))

	if _, err := adapter.Observe(stack); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Observe(stack); err != nil {
		t.Fatal(err)
	}

	probes := 0
	for _, command := range runner.commands {
		if verbOf(command[1:]) == "version" {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("probes = %d, want 1", probes)
	}
}

func TestAnInterpolatedImageIsReportedAsDeclaredAndResolved(t *testing.T) {
	runner := newRunner()
	runner.config = `{"services":{"web":{"image":"ghcr.io/example/web:1.4.0"}}}`
	runner.ps = `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running"}]`
	source := "services:\n  web:\n    image: ghcr.io/example/web:${TAG}\n"

	state := observe(t, runner, stackAt(writeCompose(t, source)))

	if state.ServiceImages["web"] != "ghcr.io/example/web:1.4.0" {
		t.Errorf("resolved image = %q, want the interpolated value", state.ServiceImages["web"])
	}
	if state.DeclaredImages["web"] != "ghcr.io/example/web:${TAG}" {
		t.Errorf("declared image = %q, want the line as written", state.DeclaredImages["web"])
	}
}

func TestADeclaredButMissingEnvFileIsIneligible(t *testing.T) {
	runner := newRunner()
	source := "services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n    env_file: ./media.env\n"

	_, err := NewDocker(config.EngineSettings{}, WithRunner(runner)).Observe(stackAt(writeCompose(t, source)))

	var ineligible *backend.IneligibleError
	if !errors.As(err, &ineligible) {
		t.Fatalf("error = %v, want an IneligibleError", err)
	}
	if !strings.Contains(err.Error(), "media.env") {
		t.Errorf("error = %v, want it to name the missing env file", err)
	}
}

func TestDriftFingerprintCoversComposeBytesEnvFilesAndTheServiceSet(t *testing.T) {
	runner := newRunner()
	runner.config = `{"services":{"web":{"image":"ghcr.io/example/web:1.4.0"}}}`
	runner.ps = `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running"}]`
	source := "services:\n  web:\n    image: ghcr.io/example/web:1.4.0\n    env_file: ./media.env\n"
	path := writeCompose(t, source)
	envPath := filepath.Join(filepath.Dir(path), "media.env")
	if err := os.WriteFile(envPath, []byte("TZ=UTC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stack := stackAt(path)

	baseline := observe(t, runner, stack).Fingerprint

	if same := observe(t, runner, stack).Fingerprint; same != baseline {
		t.Error("an unchanged stack must fingerprint the same twice")
	}
	if err := os.WriteFile(envPath, []byte("TZ=Europe/Madrid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := observe(t, runner, stack).Fingerprint; changed == baseline {
		t.Error("an env file edit must change the fingerprint")
	}

	if err := os.WriteFile(envPath, []byte("TZ=UTC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source+"# a trailing comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := observe(t, runner, stack).Fingerprint; changed == baseline {
		t.Error("a compose file edit must change the fingerprint")
	}
}

func TestAnUnwritableComposeFileIsIneligibleBeforeAnythingIsPlanned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	runner := newRunner()
	path := writeCompose(t, twoServices)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}

	_, err := NewDocker(config.EngineSettings{}, WithRunner(runner)).Observe(stackAt(path))

	var ineligible *backend.IneligibleError
	if !errors.As(err, &ineligible) {
		t.Fatalf("error = %v, want an IneligibleError", err)
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error = %v, want the writability refusal", err)
	}
}

func TestDeployWritesTheComposeFileBeforeBringingTheProjectUp(t *testing.T) {
	runner := newRunner()
	path := writeCompose(t, twoServices)
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))
	state := observe(t, runner, stackAt(path))
	pinned := strings.Replace(twoServices, "web:1.4.0", "web:1.4.0@"+digest1, 1)

	if err := adapter.Deploy(state, pinned, false); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != pinned {
		t.Errorf("compose file = %q, want the pinned document", written)
	}
	up := runner.ran("up")
	if up == nil || !slices.Contains(up, "--detach") || !slices.Contains(up, "--no-build") {
		t.Errorf("up command = %v, want a detached no-build convergence", up)
	}
}

func TestDeployRestoresBytesEvenWhenTheEngineCallFails(t *testing.T) {
	runner := newRunner()
	path := writeCompose(t, twoServices)
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))
	state := observe(t, runner, stackAt(path))
	pinned := strings.Replace(twoServices, "web:1.4.0", "web:1.4.0@"+digest1, 1)
	if err := adapter.Deploy(state, pinned, false); err != nil {
		t.Fatal(err)
	}
	runner.fail["up"] = errors.New("service web failed to start")

	err := adapter.Deploy(state, twoServices, false)

	if err == nil {
		t.Fatal("a failed convergence must be reported")
	}
	written, readErr := os.ReadFile(path) // #nosec G304 -- test fixture path
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(written) != twoServices {
		t.Errorf("compose file = %q, want the previous bytes restored first", written)
	}
}

func TestDeployRefusesARepullRequest(t *testing.T) {
	runner := newRunner()
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))
	state := observe(t, runner, stackAt(writeCompose(t, twoServices)))

	err := adapter.Deploy(state, twoServices, true)

	if err == nil || !strings.Contains(err.Error(), "repull") {
		t.Errorf("error = %v, want the repull refusal", err)
	}
	if runner.ran("up") != nil {
		t.Error("a refused deploy must not touch the engine")
	}
}

func TestVerificationRequiresEverySiblingRunningAndHealthy(t *testing.T) {
	runner := newRunner()
	adapter := NewDocker(config.EngineSettings{}, WithRunner(runner))
	state := observe(t, runner, stackAt(writeCompose(t, twoServices)))

	running, detail, err := adapter.ServicesRunning(state)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatalf("running = false (%s), want true", detail)
	}

	runner.ps = `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running","Health":"unhealthy"},` +
		`{"Service":"sidecar","Image":"ghcr.io/example/sidecar:0.9.1","State":"running"}]`
	running, detail, err = adapter.ServicesRunning(state)
	if err != nil {
		t.Fatal(err)
	}
	if running || !strings.Contains(detail, "web") {
		t.Errorf("running = %v (%s), want an unhealthy web to block", running, detail)
	}

	runner.ps = `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running","Health":"healthy"}]`
	running, detail, err = adapter.ServicesRunning(state)
	if err != nil {
		t.Fatal(err)
	}
	if running || !strings.Contains(detail, "sidecar") {
		t.Errorf("running = %v (%s), want a missing sidecar to block", running, detail)
	}
}

func TestAPinnedRunningImageAnswersItsOwnDigestWithoutAskingTheEngine(t *testing.T) {
	runner := newRunner()
	runner.config = `{"services":{"web":{"image":"ghcr.io/example/web:1.4.0"}}}`
	runner.ps = `[{"Service":"web","Image":"ghcr.io/example/web:1.4.0@` + digest1 + `","State":"running"}]`
	runner.digests = nil

	state := observe(t, runner, stackAt(writeCompose(t, twoServices)))

	if state.RunningDigests["web"] != digest1 {
		t.Errorf("running digest = %q, want the pin from the reference", state.RunningDigests["web"])
	}
	if runner.ran("inspect") != nil {
		t.Error("a pinned reference must not need an image inspect")
	}
}

func TestNewlineDelimitedPsOutputIsUnderstood(t *testing.T) {
	runner := newRunner()
	runner.ps = `{"Service":"web","Image":"ghcr.io/example/web:1.4.0","State":"running"}
{"Service":"sidecar","Image":"ghcr.io/example/sidecar:0.9.1","State":"running"}`

	state := observe(t, runner, stackAt(writeCompose(t, twoServices)))

	if len(state.RunningDigests) != 2 {
		t.Errorf("running digests = %v, want both services", state.RunningDigests)
	}
}

func TestARootlessSocketReachesTheEngineThroughItsOwnEnvironmentVariable(t *testing.T) {
	dockerRunner := newRunner()
	podmanRunner := newRunner()
	stack := stackAt(writeCompose(t, twoServices))

	observe(t, dockerRunner, stack)
	podmanStack := stack
	podmanStack.Backend = domain.BackendPodmanCompose
	if _, err := NewPodman(config.EngineSettings{Socket: "/run/user/1000/podman/podman.sock"},
		WithRunner(podmanRunner)).Observe(podmanStack); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(dockerRunner.envs[0], ","); got != "" {
		t.Errorf("docker environment = %q, want no override when no socket is configured", got)
	}
	if got := strings.Join(podmanRunner.envs[0], ","); got != "CONTAINER_HOST=unix:///run/user/1000/podman/podman.sock" {
		t.Errorf("podman environment = %q, want the rootless CONTAINER_HOST override", got)
	}
	if podmanRunner.commands[0][0] != "podman" {
		t.Errorf("podman binary = %q, want podman", podmanRunner.commands[0][0])
	}
}

func TestADockerSocketOverrideUsesDockerHost(t *testing.T) {
	runner := newRunner()

	observe(t, runner, stackAt(writeCompose(t, twoServices)))
	adapter := NewDocker(config.EngineSettings{Socket: "unix:///run/user/1000/docker.sock"}, WithRunner(runner))
	if _, err := adapter.Observe(stackAt(writeCompose(t, twoServices))); err != nil {
		t.Fatal(err)
	}

	last := runner.envs[len(runner.envs)-1]
	if got := strings.Join(last, ","); got != "DOCKER_HOST=unix:///run/user/1000/docker.sock" {
		t.Errorf("docker environment = %q, want the configured socket passed through as written", got)
	}
}

func TestAStackFromAnotherBackendIsRefused(t *testing.T) {
	stack := stackAt(writeCompose(t, twoServices))
	stack.Backend = domain.BackendPortainer

	_, err := NewDocker(config.EngineSettings{}, WithRunner(newRunner())).Observe(stack)

	if err == nil || !strings.Contains(err.Error(), "not a docker-compose stack") {
		t.Errorf("error = %v, want the wrong-backend refusal", err)
	}
}
