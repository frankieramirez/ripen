// Package config loads and validates the Ripen policy file. Validation is
// fail-closed: unknown fields, ambiguous rules, and unsafe values are
// config-load errors, never warnings.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/frankieramirez/ripen/internal/domain"
)

// HealthPolicy is one HTTP functional health check.
type HealthPolicy struct {
	Type           string
	Target         string
	AcceptedStatus []int
	TimeoutSeconds float64
}

// ServicePolicy is a per-service rule inside a multi-service stack.
type ServicePolicy struct {
	Name      string
	AutoApply bool
	Health    HealthPolicy
	Enabled   bool
}

// StackPolicy is the policy for one stack.
type StackPolicy struct {
	Name             string
	Backend          domain.Backend
	Enabled          bool
	AutoApply        bool
	ExpectedServices []string
	Health           *HealthPolicy
	Services         []ServicePolicy
	GitPath          string
	// File and Project apply to compose backends only. File is the
	// symlink-resolved compose file path; Project defaults to Name.
	File    string
	Project string
}

// GitHubPolicy configures Git-native update Proposals.
type GitHubPolicy struct {
	Repository string
	BaseBranch string
	TokenFile  string
}

// PortainerSettings configures the Portainer backend connection.
type PortainerSettings struct {
	BaseURL              string
	APIKeyFile           string
	ExpectedUsername     string
	TLSCAFile            string
	TLSFingerprintSHA256 string
}

// EngineSettings configures one compose engine.
type EngineSettings struct {
	Binary string
	Socket string
}

// ComposeSettings holds the two compose engine configurations.
type ComposeSettings struct {
	Docker EngineSettings
	Podman EngineSettings
}

// Policy is the loaded, validated configuration.
type Policy struct {
	Mode                       domain.Mode
	MaxUpdatesPerRun           int
	VerificationTimeoutSeconds int
	CandidateMinAgeSeconds     int
	LeaseTTLSeconds            int
	CheckIntervalSeconds       int
	StateFile                  string
	Portainer                  *PortainerSettings
	Compose                    ComposeSettings
	Stacks                     []StackPolicy
	ExcludedStacks             []string
	GitHub                     *GitHubPolicy
}

var (
	repositoryNamePattern = regexp.MustCompile(
		`^[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?/[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?$`)
	branchForbiddenPattern = regexp.MustCompile(`[\s~^:?*\\\[]`)
	hexPattern             = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// privilegedDockerSockets are the socket paths that grant root-equivalent
// host access; a configured compose socket resolving to one of them is a
// config-load refusal (rework spec, invariant 10).
var privilegedDockerSockets = []string{"/var/run/docker.sock", "/run/docker.sock"}

// Load reads and validates a policy file.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the policy path is operator-supplied by design
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("config is not valid YAML: %w", err)
	}
	var payload any
	if err := document.Decode(&payload); err != nil {
		return nil, fmt.Errorf("config is not valid YAML: %w", err)
	}

	root, err := mapping(payload, "config")
	if err != nil {
		return nil, err
	}
	if err := exactKeys(root, []string{
		"mode", "max_updates_per_run", "verification_timeout_seconds",
		"candidate_min_age_seconds", "lease_ttl_seconds", "check_interval_seconds",
		"portainer", "github", "compose", "state_file", "stacks", "exclude",
	}, "config"); err != nil {
		return nil, err
	}

	policy := &Policy{}

	mode, err := domain.ParseMode(stringOr(root, "mode", "monitor"))
	if err != nil {
		return nil, err
	}
	policy.Mode = mode

	if policy.MaxUpdatesPerRun, err = positiveInt(root, "max_updates_per_run", 1); err != nil {
		return nil, err
	}
	if policy.MaxUpdatesPerRun != 1 {
		return nil, fmt.Errorf("v1 requires max_updates_per_run: 1")
	}
	if policy.VerificationTimeoutSeconds, err = positiveInt(root, "verification_timeout_seconds", 300); err != nil {
		return nil, err
	}
	if policy.CandidateMinAgeSeconds, err = positiveInt(root, "candidate_min_age_seconds", 86400); err != nil {
		return nil, err
	}
	if policy.LeaseTTLSeconds, err = positiveInt(root, "lease_ttl_seconds", 1800); err != nil {
		return nil, err
	}
	if policy.CheckIntervalSeconds, err = positiveInt(root, "check_interval_seconds", 86400); err != nil {
		return nil, err
	}
	policy.StateFile = stringOr(root, "state_file", "/data/updater.db")

	if policy.GitHub, err = githubSettings(root); err != nil {
		return nil, err
	}
	if policy.Compose, err = composeSettings(root); err != nil {
		return nil, err
	}
	if policy.Stacks, err = stackPolicies(root, stackKeyOrder(&document), policy.GitHub); err != nil {
		return nil, err
	}
	if policy.Portainer, err = portainerSettings(root, policy.Stacks); err != nil {
		return nil, err
	}
	if policy.ExcludedStacks, err = excludedStacks(root, policy.Stacks); err != nil {
		return nil, err
	}
	return policy, nil
}

func stackPolicies(root map[string]any, order []string, github *GitHubPolicy) ([]StackPolicy, error) {
	value, ok := root["stacks"]
	if !ok {
		return nil, fmt.Errorf("config.stacks is required")
	}
	raw, err := mapping(value, "stacks")
	if err != nil {
		return nil, err
	}
	// Policy order is document order: with one update per run, the order
	// stacks are declared in decides which mature Candidate goes first.
	stacks := make([]StackPolicy, 0, len(raw))
	for _, name := range order {
		stack, err := stackPolicy(name, raw[name], github)
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, stack)
	}
	return stacks, nil
}

// stackKeyOrder walks the parsed YAML document and returns the stacks
// mapping's keys in document order, which plain map decoding loses.
func stackKeyOrder(document *yaml.Node) []string {
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "stacks" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		value := root.Content[i+1]
		keys := make([]string, 0, len(value.Content)/2)
		for j := 0; j+1 < len(value.Content); j += 2 {
			keys = append(keys, value.Content[j].Value)
		}
		return keys
	}
	return nil
}

func stackPolicy(name string, value any, github *GitHubPolicy) (StackPolicy, error) {
	path := "stacks." + name
	raw, err := mapping(value, path)
	if err != nil {
		return StackPolicy{}, err
	}
	if err := exactKeys(raw, []string{
		"enabled", "auto_apply", "expected_services", "health", "services",
		"git_path", "backend", "file", "project",
	}, path); err != nil {
		return StackPolicy{}, err
	}

	stack := StackPolicy{Name: name, Backend: domain.BackendPortainer}

	if backendRaw, ok := raw["backend"]; ok {
		backendName, ok := backendRaw.(string)
		if !ok {
			return StackPolicy{}, fmt.Errorf("%s.backend must be a string", path)
		}
		if stack.Backend, err = domain.ParseBackend(backendName); err != nil {
			return StackPolicy{}, err
		}
	}
	if stack.Backend.IsCompose() {
		file, ok := raw["file"].(string)
		if !ok || file == "" {
			return StackPolicy{}, fmt.Errorf("%s.file is required for compose backends", path)
		}
		stack.File = resolveFile(file)
		stack.Project = name
		if projectRaw, ok := raw["project"]; ok {
			project, ok := projectRaw.(string)
			if !ok || project == "" {
				return StackPolicy{}, fmt.Errorf("%s.project must be a non-empty string", path)
			}
			stack.Project = project
		}
	} else {
		for _, key := range []string{"file", "project"} {
			if _, ok := raw[key]; ok {
				return StackPolicy{}, fmt.Errorf("%s.%s applies to only compose backends", path, key)
			}
		}
	}

	if stack.Enabled, err = boolOr(raw, "enabled", false, path); err != nil {
		return StackPolicy{}, err
	}
	if stack.AutoApply, err = boolOr(raw, "auto_apply", false, path); err != nil {
		return StackPolicy{}, err
	}

	expected, ok := raw["expected_services"].([]any)
	if !ok || len(expected) == 0 {
		return StackPolicy{}, fmt.Errorf("%s.expected_services must be a non-empty list", path)
	}
	for _, item := range expected {
		serviceName, ok := item.(string)
		if !ok || serviceName == "" {
			return StackPolicy{}, fmt.Errorf("%s.expected_services must be a non-empty list", path)
		}
		if slices.Contains(stack.ExpectedServices, serviceName) {
			return StackPolicy{}, fmt.Errorf("%s.expected_services must not contain duplicate names", path)
		}
		stack.ExpectedServices = append(stack.ExpectedServices, serviceName)
	}

	_, hasServices := raw["services"]
	_, hasAutoApply := raw["auto_apply"]
	_, hasHealth := raw["health"]
	if hasServices && (hasAutoApply || hasHealth) {
		return StackPolicy{}, fmt.Errorf(
			"%s cannot use stack-level auto_apply or health with per-service rules", path)
	}
	if len(stack.ExpectedServices) > 1 && !hasServices {
		return StackPolicy{}, fmt.Errorf("%s requires per-service rules for multiple services", path)
	}

	if hasServices {
		if stack.Services, err = servicePolicies(raw["services"], stack.ExpectedServices, path); err != nil {
			return StackPolicy{}, err
		}
	} else {
		healthRaw, ok := raw["health"]
		if !ok {
			return StackPolicy{}, fmt.Errorf("%s.health is required", path)
		}
		health, err := healthPolicy(healthRaw, path+".health")
		if err != nil {
			return StackPolicy{}, err
		}
		stack.Health = &health
	}

	if gitPathRaw, ok := raw["git_path"]; ok {
		gitPath, _ := gitPathRaw.(string)
		ext := filepath.Ext(gitPath)
		if github == nil || gitPath == "" || strings.HasPrefix(gitPath, "/") ||
			strings.Contains(gitPath, "\\") ||
			slices.Contains(strings.Split(gitPath, "/"), "..") ||
			(ext != ".yaml" && ext != ".yml") {
			return StackPolicy{}, fmt.Errorf(
				"%s.git_path requires github configuration and a relative YAML path", path)
		}
		stack.GitPath = gitPath
	}
	return stack, nil
}

func servicePolicies(value any, expected []string, stackPath string) ([]ServicePolicy, error) {
	raw, err := mapping(value, stackPath+".services")
	if err != nil {
		return nil, err
	}
	if len(raw) != len(expected) {
		return nil, fmt.Errorf("%s.services must exactly match expected_services", stackPath)
	}
	services := make([]ServicePolicy, 0, len(expected))
	anyEnabled := false
	for _, name := range expected {
		serviceRaw, ok := raw[name]
		if !ok {
			return nil, fmt.Errorf("%s.services must exactly match expected_services", stackPath)
		}
		path := stackPath + ".services." + name
		entry, err := mapping(serviceRaw, path)
		if err != nil {
			return nil, err
		}
		if err := exactKeys(entry, []string{"enabled", "auto_apply", "health"}, path); err != nil {
			return nil, err
		}
		service := ServicePolicy{Name: name}
		if service.Enabled, err = boolOr(entry, "enabled", true, path); err != nil {
			return nil, err
		}
		if service.AutoApply, err = boolOr(entry, "auto_apply", false, path); err != nil {
			return nil, err
		}
		if !service.Enabled && service.AutoApply {
			return nil, fmt.Errorf("%s cannot auto-apply when disabled", path)
		}
		healthRaw, ok := entry["health"]
		if !ok {
			return nil, fmt.Errorf("%s.health is required", path)
		}
		if service.Health, err = healthPolicy(healthRaw, path+".health"); err != nil {
			return nil, err
		}
		anyEnabled = anyEnabled || service.Enabled
		services = append(services, service)
	}
	if !anyEnabled {
		return nil, fmt.Errorf("%s.services requires at least one enabled service", stackPath)
	}
	return services, nil
}

func healthPolicy(value any, path string) (HealthPolicy, error) {
	raw, err := mapping(value, path)
	if err != nil {
		return HealthPolicy{}, err
	}
	if err := exactKeys(raw, []string{"type", "url", "target", "accepted_status", "timeout_seconds"}, path); err != nil {
		return HealthPolicy{}, err
	}
	if _, hasTarget := raw["target"]; hasTarget {
		if _, hasURL := raw["url"]; hasURL {
			return HealthPolicy{}, fmt.Errorf("%s: set target or url, not both", path)
		}
	}
	target, ok := raw["target"].(string)
	if !ok {
		target, ok = raw["url"].(string)
	}
	if !ok || target == "" {
		return HealthPolicy{}, fmt.Errorf("%s.target or url is required", path)
	}
	if typeRaw, present := raw["type"]; present {
		if _, ok := typeRaw.(string); !ok {
			return HealthPolicy{}, fmt.Errorf("%s.type must be a string", path)
		}
	}

	statuses := []int{200}
	if statusesRaw, present := raw["accepted_status"]; present {
		list, ok := statusesRaw.([]any)
		if !ok || len(list) == 0 {
			return HealthPolicy{}, fmt.Errorf(
				"%s.accepted_status must be a non-empty list of HTTP status codes", path)
		}
		statuses = nil
		for _, item := range list {
			code, ok := item.(int)
			if !ok || code < 100 || code > 599 {
				return HealthPolicy{}, fmt.Errorf(
					"%s.accepted_status must be a non-empty list of HTTP status codes", path)
			}
			statuses = append(statuses, code)
		}
	}

	timeout := 5.0
	if timeoutRaw, present := raw["timeout_seconds"]; present {
		switch parsed := timeoutRaw.(type) {
		case int:
			timeout = float64(parsed)
		case float64:
			timeout = parsed
		default:
			return HealthPolicy{}, fmt.Errorf("%s.timeout_seconds must be a number", path)
		}
	}

	return HealthPolicy{
		Type:           stringOr(raw, "type", "http"),
		Target:         target,
		AcceptedStatus: statuses,
		TimeoutSeconds: timeout,
	}, nil
}

func githubSettings(root map[string]any) (*GitHubPolicy, error) {
	value, ok := root["github"]
	if !ok {
		return nil, nil
	}
	raw, err := mapping(value, "github")
	if err != nil {
		return nil, err
	}
	if err := exactKeys(raw, []string{"repository", "base_branch", "token_file"}, "github"); err != nil {
		return nil, err
	}
	repository, _ := raw["repository"].(string)
	if !repositoryNamePattern.MatchString(repository) {
		return nil, fmt.Errorf("github.repository must be an owner/repository name")
	}
	baseBranch, _ := raw["base_branch"].(string)
	if baseBranch == "" || strings.HasPrefix(baseBranch, "/") ||
		strings.HasSuffix(baseBranch, "/") || strings.HasSuffix(baseBranch, ".") ||
		strings.HasSuffix(baseBranch, ".lock") || strings.Contains(baseBranch, "..") ||
		strings.Contains(baseBranch, "//") || branchForbiddenPattern.MatchString(baseBranch) {
		return nil, fmt.Errorf("github.base_branch is invalid")
	}
	tokenFile, _ := raw["token_file"].(string)
	if tokenFile == "" {
		return nil, fmt.Errorf("github.token_file is required")
	}
	return &GitHubPolicy{Repository: repository, BaseBranch: baseBranch, TokenFile: tokenFile}, nil
}

func portainerSettings(root map[string]any, stacks []StackPolicy) (*PortainerSettings, error) {
	value, ok := root["portainer"]
	if !ok {
		for _, stack := range stacks {
			if stack.Backend == domain.BackendPortainer {
				return nil, fmt.Errorf("portainer is required when a stack uses the portainer backend")
			}
		}
		return nil, nil
	}
	raw, err := mapping(value, "portainer")
	if err != nil {
		return nil, err
	}
	if err := exactKeys(raw, []string{
		"base_url", "api_key_file", "expected_username", "tls_ca_file", "tls_fingerprint_sha256",
	}, "portainer"); err != nil {
		return nil, err
	}

	settings := &PortainerSettings{}
	if fingerprintRaw, ok := raw["tls_fingerprint_sha256"]; ok {
		fingerprint, _ := fingerprintRaw.(string)
		fingerprint = strings.ReplaceAll(strings.ToLower(fingerprint), ":", "")
		if !hexPattern.MatchString(fingerprint) {
			return nil, fmt.Errorf("portainer.tls_fingerprint_sha256 must be 64 hexadecimal characters")
		}
		settings.TLSFingerprintSHA256 = fingerprint
	}
	settings.TLSCAFile, _ = raw["tls_ca_file"].(string)
	if settings.TLSCAFile != "" && settings.TLSFingerprintSHA256 != "" {
		return nil, fmt.Errorf("choose tls_ca_file or tls_fingerprint_sha256, not both")
	}
	if settings.TLSCAFile == "" && settings.TLSFingerprintSHA256 == "" {
		return nil, fmt.Errorf("configure exactly one of portainer.tls_ca_file or tls_fingerprint_sha256")
	}

	baseURL, ok := raw["base_url"].(string)
	if !ok {
		return nil, fmt.Errorf("portainer.base_url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("portainer.base_url must use https")
	}
	settings.BaseURL = baseURL

	if settings.APIKeyFile, ok = raw["api_key_file"].(string); !ok || settings.APIKeyFile == "" {
		return nil, fmt.Errorf("portainer.api_key_file is required")
	}
	if settings.ExpectedUsername, ok = raw["expected_username"].(string); !ok || settings.ExpectedUsername == "" {
		return nil, fmt.Errorf("portainer.expected_username is required")
	}
	return settings, nil
}

func composeSettings(root map[string]any) (ComposeSettings, error) {
	settings := ComposeSettings{
		Docker: EngineSettings{Binary: "docker"},
		Podman: EngineSettings{Binary: "podman"},
	}
	value, ok := root["compose"]
	if !ok {
		return settings, nil
	}
	raw, err := mapping(value, "compose")
	if err != nil {
		return ComposeSettings{}, err
	}
	if err := exactKeys(raw, []string{"docker", "podman"}, "compose"); err != nil {
		return ComposeSettings{}, err
	}
	if settings.Docker, err = engineSettings(raw, "docker", settings.Docker); err != nil {
		return ComposeSettings{}, err
	}
	if settings.Podman, err = engineSettings(raw, "podman", settings.Podman); err != nil {
		return ComposeSettings{}, err
	}
	return settings, nil
}

func engineSettings(raw map[string]any, key string, defaults EngineSettings) (EngineSettings, error) {
	value, ok := raw[key]
	if !ok {
		return defaults, nil
	}
	path := "compose." + key
	entry, err := mapping(value, path)
	if err != nil {
		return EngineSettings{}, err
	}
	if err := exactKeys(entry, []string{"binary", "socket"}, path); err != nil {
		return EngineSettings{}, err
	}
	settings := defaults
	if binary, ok := entry["binary"].(string); ok && binary != "" {
		settings.Binary = binary
	}
	if socket, ok := entry["socket"].(string); ok && socket != "" {
		if privileged, hit := resolvesToPrivilegedSocket(socket); privileged {
			return EngineSettings{}, fmt.Errorf(
				"%s.socket resolves to the privileged docker socket %s; rootless sockets only", path, hit)
		}
		settings.Socket = socket
	}
	return settings, nil
}

func excludedStacks(root map[string]any, stacks []StackPolicy) ([]string, error) {
	value, ok := root["exclude"]
	if !ok {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("config.exclude must be a list of stack names")
	}
	excluded := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("config.exclude must be a list of stack names")
		}
		excluded = append(excluded, name)
	}
	var overlap []string
	for _, stack := range stacks {
		if stack.Enabled && slices.Contains(excluded, stack.Name) {
			overlap = append(overlap, stack.Name)
		}
	}
	if len(overlap) > 0 {
		slices.Sort(overlap)
		return nil, fmt.Errorf("enabled stacks also excluded: %s", strings.Join(overlap, ", "))
	}
	return excluded, nil
}

// resolveFile resolves compose-file symlinks at config load so drift is
// recorded against the real path. A path that cannot be fully resolved
// (e.g. not yet present) is kept as written.
func resolveFile(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// resolvesToPrivilegedSocket follows the symlink chain from a configured
// socket path and reports whether any step lands on a privileged docker
// socket path. Every step is checked — not just the final target — so
// configuring /var/run/docker.sock is refused even on hosts where that
// path is itself a symlink elsewhere (e.g. Docker Desktop) or absent.
func resolvesToPrivilegedSocket(path string) (bool, string) {
	resolved := filepath.Clean(path)
	for range 16 {
		if slices.Contains(privilegedDockerSockets, resolved) {
			return true, resolved
		}
		info, err := os.Lstat(resolved)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return false, ""
		}
		target, err := os.Readlink(resolved)
		if err != nil {
			return false, ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(resolved), target)
		}
		resolved = filepath.Clean(target)
	}
	return false, ""
}

// --- generic mapping helpers ---

func mapping(value any, path string) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a mapping", path)
	}
	return raw, nil
}

func exactKeys(raw map[string]any, allowed []string, path string) error {
	var unknown []string
	for key := range raw {
		if !slices.Contains(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("unknown %s fields: %s", path, strings.Join(unknown, ", "))
	}
	return nil
}

func stringOr(raw map[string]any, key, fallback string) string {
	if value, ok := raw[key].(string); ok {
		return value
	}
	return fallback
}

func boolOr(raw map[string]any, key string, fallback bool, path string) (bool, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s.%s must be a boolean", path, key)
	}
	return parsed, nil
}

func positiveInt(raw map[string]any, key string, fallback int) (int, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	parsed, ok := value.(int)
	if !ok {
		return 0, fmt.Errorf("config.%s must be an integer", key)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("config.%s must be greater than zero", key)
	}
	return parsed, nil
}
