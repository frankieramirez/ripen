package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
)

const valid = `
mode: monitor
state_file: /tmp/updater.db
portainer:
  base_url: https://portainer:9443
  api_key_file: /secret
  expected_username: ripen
  tls_fingerprint_sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
stacks:
  example-app:
    enabled: true
    auto_apply: false
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
exclude: [arr]
`

func write(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, value string) (*Policy, error) {
	t.Helper()
	return Load(write(t, value))
}

func mustLoad(t *testing.T, value string) *Policy {
	t.Helper()
	policy, err := Load(write(t, value))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	return policy
}

func assertLoadError(t *testing.T, value, fragment string) {
	t.Helper()
	_, err := load(t, value)
	if err == nil {
		t.Fatalf("Load() succeeded, want error containing %q", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("Load() error = %q, want it to contain %q", err, fragment)
	}
}

func TestLoadDefaultsToSingleUpdateMonitorMode(t *testing.T) {
	policy := mustLoad(t, valid)

	if policy.Mode != domain.ModeMonitor {
		t.Errorf("Mode = %v, want monitor", policy.Mode)
	}
	if policy.MaxUpdatesPerRun != 1 {
		t.Errorf("MaxUpdatesPerRun = %d, want 1", policy.MaxUpdatesPerRun)
	}
	if policy.Stacks[0].Name != "example-app" {
		t.Errorf("Stacks[0].Name = %q, want example-app", policy.Stacks[0].Name)
	}
	if policy.Stacks[0].AutoApply {
		t.Error("Stacks[0].AutoApply = true, want false")
	}
	if policy.Stacks[0].Backend != domain.BackendPortainer {
		t.Errorf("Stacks[0].Backend = %v, want portainer", policy.Stacks[0].Backend)
	}
}

func TestLoadSupportsGitNativeStackSource(t *testing.T) {
	value := strings.Replace(valid,
		"portainer:\n",
		"github:\n  repository: example/nas\n  base_branch: main\n  token_file: /run/secrets/github_token\nportainer:\n",
		1)
	value = strings.Replace(value,
		"    expected_services: [example-app]\n",
		"    expected_services: [example-app]\n    git_path: stacks/example-app/compose.yaml\n",
		1)

	policy := mustLoad(t, value)

	if policy.GitHub == nil {
		t.Fatal("GitHub = nil, want configured")
	}
	if policy.GitHub.Repository != "example/nas" {
		t.Errorf("GitHub.Repository = %q, want example/nas", policy.GitHub.Repository)
	}
	if policy.Stacks[0].GitPath != "stacks/example-app/compose.yaml" {
		t.Errorf("GitPath = %q", policy.Stacks[0].GitPath)
	}
}

func TestGitPathRequiresGitHubConfiguration(t *testing.T) {
	value := strings.Replace(valid,
		"    expected_services: [example-app]\n",
		"    expected_services: [example-app]\n    git_path: stacks/example-app/compose.yaml\n",
		1)

	assertLoadError(t, value, "requires github configuration")
}

const multiServiceStack = `  arr:
    enabled: true
    expected_services: [radarr, sonarr]
    services:
      radarr:
        auto_apply: false
        health:
          type: http
          url: http://radarr:7878/
          accepted_status: [200, 302]
      sonarr:
        auto_apply: false
        health:
          type: http
          url: http://sonarr:8989/
          accepted_status: [200, 302]
`

const singleStack = `  example-app:
    enabled: true
    auto_apply: false
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
`

func TestLoadSupportsExplicitPerServiceHealthRules(t *testing.T) {
	value := strings.Replace(valid, singleStack, multiServiceStack, 1)
	value = strings.Replace(value, "exclude: [arr]", "exclude: []", 1)

	policy := mustLoad(t, value)

	stack := policy.Stacks[0]
	if stack.Name != "arr" {
		t.Fatalf("Stacks[0].Name = %q, want arr", stack.Name)
	}
	if len(stack.Services) != 2 || stack.Services[0].Name != "radarr" || stack.Services[1].Name != "sonarr" {
		t.Fatalf("Services = %+v, want radarr, sonarr", stack.Services)
	}
	if stack.Services[0].Health.Target != "http://radarr:7878/" {
		t.Errorf("radarr health target = %q", stack.Services[0].Health.Target)
	}
	if got := stack.Services[0].Health.AcceptedStatus; len(got) != 2 || got[0] != 200 || got[1] != 302 {
		t.Errorf("radarr accepted_status = %v, want [200 302]", got)
	}
}

func TestMultiServicePolicySupportsHealthOnlyService(t *testing.T) {
	replacement := `  arr:
    enabled: true
    expected_services: [radarr, readarr]
    services:
      radarr:
        enabled: true
        auto_apply: false
        health:
          type: http
          url: http://radarr:7878/
      readarr:
        enabled: false
        auto_apply: false
        health:
          type: http
          url: http://readarr:8787/
`
	value := strings.Replace(valid, singleStack, replacement, 1)
	value = strings.Replace(value, "exclude: [arr]", "exclude: []", 1)

	policy := mustLoad(t, value)

	if !policy.Stacks[0].Services[0].Enabled {
		t.Error("radarr Enabled = false, want true")
	}
	if policy.Stacks[0].Services[1].Enabled {
		t.Error("readarr Enabled = true, want false")
	}
}

func TestMultiServicePolicyRequiresOneManagedService(t *testing.T) {
	replacement := `  example-app:
    enabled: true
    expected_services: [example-app]
    services:
      example-app:
        enabled: false
        auto_apply: false
        health:
          type: http
          url: http://example-app:8091/
`
	assertLoadError(t, strings.Replace(valid, singleStack, replacement, 1), "at least one enabled service")
}

func TestPerServiceFlagsRequireRealBooleans(t *testing.T) {
	for field, quoted := range map[string]string{"enabled": `"false"`, "auto_apply": `"true"`} {
		replacement := `  example-app:
    enabled: true
    expected_services: [example-app]
    services:
      example-app:
        ` + field + `: ` + quoted + `
        health:
          type: http
          url: http://example-app:8091/
`
		assertLoadError(t, strings.Replace(valid, singleStack, replacement, 1), field+" must be a boolean")
	}
}

func TestMultiServicePolicyRequiresExplicitServiceRules(t *testing.T) {
	value := strings.Replace(valid,
		"expected_services: [example-app]",
		"expected_services: [example-app, sidecar]", 1)

	assertLoadError(t, value, "requires per-service rules")
}

func TestExpectedServicesRejectsDuplicateNames(t *testing.T) {
	value := strings.Replace(valid,
		"expected_services: [example-app]",
		"expected_services: [example-app, example-app]", 1)

	assertLoadError(t, value, "duplicate")
}

func TestHealthStatusesAreNonemptyHTTPCodes(t *testing.T) {
	for _, statuses := range []string{"[]", "[true]", "[99]", "[600]"} {
		value := strings.Replace(valid,
			"      url: http://nas:8091/",
			"      url: http://nas:8091/\n      accepted_status: "+statuses, 1)

		assertLoadError(t, value, "non-empty list of HTTP status codes")
	}
}

func TestPerServicePolicyRejectsAmbiguousStackLevelApplySetting(t *testing.T) {
	replacement := `  example-app:
    enabled: true
    auto_apply: true
    expected_services: [example-app]
    services:
      example-app:
        auto_apply: false
        health:
          type: http
          url: http://example-app:8091/
`
	assertLoadError(t, strings.Replace(valid, singleStack, replacement, 1), "cannot use stack-level auto_apply or health")
}

func TestUnknownFieldIsRejected(t *testing.T) {
	assertLoadError(t, valid+"surprise: true\n", "unknown config fields")
}

func TestEnabledStackCannotAlsoBeExcluded(t *testing.T) {
	value := strings.Replace(valid, "exclude: [arr]", "exclude: [example-app]", 1)

	assertLoadError(t, value, "also excluded")
}

func TestMoreThanOneUpdatePerRunIsRejected(t *testing.T) {
	value := strings.Replace(valid, "mode: monitor", "mode: monitor\nmax_updates_per_run: 2", 1)

	assertLoadError(t, value, "requires max_updates_per_run")
}

func TestInvalidTLSFingerprintIsRejected(t *testing.T) {
	value := strings.Replace(valid, strings.Repeat("a", 64), "abcdef", 1)

	assertLoadError(t, value, "64 hexadecimal")
}

func TestPortainerBaseURLMustUseHTTPS(t *testing.T) {
	value := strings.Replace(valid, "https://portainer:9443", "http://portainer:9000", 1)

	assertLoadError(t, value, "base_url must use https")
}

func TestTLSTrustMechanismIsRequired(t *testing.T) {
	value := strings.Replace(valid, "  tls_fingerprint_sha256: "+strings.Repeat("a", 64)+"\n", "", 1)

	assertLoadError(t, value, "exactly one")
}

func TestMalformedNumericSettingIsAConfigError(t *testing.T) {
	value := strings.Replace(valid, "mode: monitor", "mode: monitor\nlease_ttl_seconds: nope", 1)

	assertLoadError(t, value, "lease_ttl_seconds must be an integer")
}

func composeStack(t *testing.T, backend string) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return strings.Replace(valid, singleStack, `  example-app:
    enabled: true
    auto_apply: false
    backend: `+backend+`
    file: `+file+`
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
`, 1)
}

func TestComposeBackendStackParsesFileAndDefaultsProjectToStackName(t *testing.T) {
	policy := mustLoad(t, composeStack(t, "docker-compose"))

	stack := policy.Stacks[0]
	if stack.Backend != domain.BackendDockerCompose {
		t.Errorf("Backend = %v, want docker-compose", stack.Backend)
	}
	if stack.File == "" {
		t.Error("File is empty, want the compose file path")
	}
	if stack.Project != "example-app" {
		t.Errorf("Project = %q, want example-app (default to stack name)", stack.Project)
	}
}

func TestComposeBackendRequiresFile(t *testing.T) {
	value := strings.Replace(valid, singleStack, `  example-app:
    enabled: true
    auto_apply: false
    backend: podman-compose
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
`, 1)

	assertLoadError(t, value, "file is required")
}

func TestPortainerBackendRejectsComposeOnlyKeys(t *testing.T) {
	value := strings.Replace(valid, singleStack, `  example-app:
    enabled: true
    auto_apply: false
    file: /srv/example/compose.yaml
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
`, 1)

	assertLoadError(t, value, "only compose backends")
}

func TestUnknownBackendIsRejected(t *testing.T) {
	assertLoadError(t, composeStack(t, "kubernetes"), "backend must be portainer, docker-compose, or podman-compose")
}

func TestComposeFileSymlinkIsResolvedAtLoad(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(target, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "compose.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	value := strings.Replace(valid, singleStack, `  example-app:
    enabled: true
    auto_apply: false
    backend: docker-compose
    file: `+link+`
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
`, 1)

	policy := mustLoad(t, value)

	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Stacks[0].File != resolved {
		t.Errorf("File = %q, want symlink-resolved %q", policy.Stacks[0].File, resolved)
	}
}

func TestPrivilegedDockerSocketRefusesAtConfigLoad(t *testing.T) {
	value := strings.Replace(composeStack(t, "docker-compose"), "state_file: /tmp/updater.db",
		"state_file: /tmp/updater.db\ncompose:\n  docker:\n    socket: /var/run/docker.sock", 1)

	assertLoadError(t, value, "privileged docker socket")
}

func TestSymlinkToPrivilegedDockerSocketRefusesAtConfigLoad(t *testing.T) {
	link := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.Symlink("/var/run/docker.sock", link); err != nil {
		t.Fatal(err)
	}
	value := strings.Replace(composeStack(t, "docker-compose"), "state_file: /tmp/updater.db",
		"state_file: /tmp/updater.db\ncompose:\n  docker:\n    socket: "+link, 1)

	assertLoadError(t, value, "privileged docker socket")
}

func TestRootlessSocketIsAccepted(t *testing.T) {
	value := strings.Replace(composeStack(t, "docker-compose"), "state_file: /tmp/updater.db",
		"state_file: /tmp/updater.db\ncompose:\n  docker:\n    socket: /run/user/1000/docker.sock", 1)

	policy := mustLoad(t, value)

	if policy.Compose.Docker.Socket != "/run/user/1000/docker.sock" {
		t.Errorf("Docker.Socket = %q", policy.Compose.Docker.Socket)
	}
	if policy.Compose.Docker.Binary != "docker" {
		t.Errorf("Docker.Binary = %q, want default docker", policy.Compose.Docker.Binary)
	}
	if policy.Compose.Podman.Binary != "podman" {
		t.Errorf("Podman.Binary = %q, want default podman", policy.Compose.Podman.Binary)
	}
}

func TestPortainerSectionIsOptionalForComposeOnlyConfigs(t *testing.T) {
	value := composeStack(t, "docker-compose")
	start := strings.Index(value, "portainer:")
	end := strings.Index(value, "stacks:")
	value = value[:start] + value[end:]

	policy := mustLoad(t, value)

	if policy.Portainer != nil {
		t.Errorf("Portainer = %+v, want nil", policy.Portainer)
	}
}

func TestPortainerBackedStackRequiresPortainerSection(t *testing.T) {
	start := strings.Index(valid, "portainer:")
	end := strings.Index(valid, "stacks:")
	value := valid[:start] + valid[end:]

	assertLoadError(t, value, "portainer is required")
}

func TestGitPathRejectsInteriorParentTraversal(t *testing.T) {
	value := strings.Replace(valid,
		"portainer:\n",
		"github:\n  repository: example/nas\n  base_branch: main\n  token_file: /run/secrets/github_token\nportainer:\n",
		1)
	value = strings.Replace(value,
		"    expected_services: [example-app]\n",
		"    expected_services: [example-app]\n    git_path: stacks/../evil.yaml\n",
		1)

	assertLoadError(t, value, "relative YAML path")
}

func TestStacksPreserveDocumentOrder(t *testing.T) {
	value := strings.Replace(valid, singleStack, `  zeta:
    enabled: true
    auto_apply: false
    expected_services: [zeta]
    health:
      type: http
      url: http://zeta:1/
  alpha:
    enabled: true
    auto_apply: false
    expected_services: [alpha]
    health:
      type: http
      url: http://alpha:1/
`, 1)

	policy := mustLoad(t, value)

	if len(policy.Stacks) != 2 || policy.Stacks[0].Name != "zeta" || policy.Stacks[1].Name != "alpha" {
		t.Errorf("stack order = %v, want document order [zeta alpha]", []string{policy.Stacks[0].Name, policy.Stacks[1].Name})
	}
}

func TestHealthTargetAndURLBothSetIsAmbiguous(t *testing.T) {
	value := strings.Replace(valid,
		"      url: http://nas:8091/",
		"      url: http://nas:8091/\n      target: http://other:1/", 1)

	assertLoadError(t, value, "not both")
}

func TestHealthTypeMustBeAString(t *testing.T) {
	value := strings.Replace(valid, "      type: http", "      type: 7", 1)

	assertLoadError(t, value, "type must be a string")
}

func TestNotifierWebhookIsReadWithItsDefaults(t *testing.T) {
	policy := mustLoad(t, `
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [web]
    health:
      target: http://media:8080/health
notifier:
  heartbeat_interval_seconds: 86400
  webhook:
    url_file: /run/secrets/webhook_url
    token_file: /run/secrets/webhook_token
`)

	if policy.Notifier == nil || policy.Notifier.Webhook == nil {
		t.Fatal("the notifier section was not loaded")
	}
	webhook := policy.Notifier.Webhook
	if webhook.URLFile != "/run/secrets/webhook_url" || webhook.TokenFile != "/run/secrets/webhook_token" {
		t.Errorf("webhook = %+v, want the configured secret paths", webhook)
	}
	if webhook.TimeoutSeconds != 10 {
		t.Errorf("timeout = %d, want the default", webhook.TimeoutSeconds)
	}
	if len(webhook.Events) != len(event.DefaultPaging) {
		t.Errorf("events = %v, want the default paging set", webhook.Events)
	}
	if policy.Notifier.HeartbeatIntervalSeconds != 86400 {
		t.Errorf("heartbeat = %d, want the configured interval", policy.Notifier.HeartbeatIntervalSeconds)
	}
}

func TestAnUnknownNotifierEventNameIsAConfigError(t *testing.T) {
	assertLoadError(t, `
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [web]
    health:
      target: http://media:8080/health
notifier:
  webhook:
    url_file: /run/secrets/webhook_url
    events:
      - breaker.opened
      - breaker.exploded
`, "unknown event name")
}

func TestAbsentNotifierConfigurationIsSilentButLogging(t *testing.T) {
	policy := mustLoad(t, `
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [web]
    health:
      target: http://media:8080/health
`)

	if policy.Notifier != nil {
		t.Errorf("notifier = %+v, want none configured", policy.Notifier)
	}
}

func TestTheWebUIIsOffUnlessTheConfigTurnsItOn(t *testing.T) {
	base := `
stacks:
  media:
    enabled: true
    backend: docker-compose
    file: /srv/media/compose.yaml
    expected_services: [web]
    health:
      target: http://media:8080/health
`

	if policy := mustLoad(t, base); policy.UI != nil {
		t.Errorf("ui = %+v, want none when unconfigured", policy.UI)
	}

	present := mustLoad(t, base+`
ui:
  enabled: true
  address: 0.0.0.0:7476
  token_file: /run/secrets/ui_token
`)
	if present.UI == nil || !present.UI.Enabled {
		t.Fatalf("ui = %+v, want it enabled", present.UI)
	}
	if present.UI.Address != "0.0.0.0:7476" || present.UI.TokenFile != "/run/secrets/ui_token" {
		t.Errorf("ui = %+v, want the configured address and token file", present.UI)
	}

	defaults := mustLoad(t, base+`
ui: {}
`)
	if defaults.UI.Enabled {
		t.Error("an empty ui section must leave the interface off")
	}
	if defaults.UI.Address != "127.0.0.1:7476" {
		t.Errorf("address = %q, want the loopback default", defaults.UI.Address)
	}
}
