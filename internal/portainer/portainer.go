// Package portainer is the Portainer API backend. TLS trust is exactly
// one of a CA file or a pinned certificate fingerprint (decided at
// config load); there is no insecure mode. Git-backed stacks are never
// updated directly — Proposals go through Git.
package portainer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

var pinnedImagePattern = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)

// EnvVar is one stack environment variable, in Portainer's wire shape.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Stack is one Portainer stack as listed by the API.
type Stack struct {
	ID         int
	EndpointID int
	Name       string
	Status     int
	Env        []EnvVar
	GitBacked  bool
}

// HTTPError is a non-2xx Portainer API response.
type HTTPError struct {
	Status int
	Detail string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Portainer HTTP %d: %s", e.Status, e.Detail)
}

type requester interface {
	request(method, path string, headers map[string]string, body any, timeout time.Duration) (int, any, error)
}

// Options configures an Adapter.
type Options struct {
	BaseURL           string
	APIKeyFile        string
	CAFile            string
	FingerprintSHA256 string
	Timeout           time.Duration
	UpdateTimeout     time.Duration
}

// Adapter is the Portainer backend.
type Adapter struct {
	client        requester
	headers       map[string]string
	updateTimeout time.Duration
}

// New builds an Adapter, reading and validating the API key file.
func New(options Options) (*Adapter, error) {
	raw, err := os.ReadFile(options.APIKeyFile) // #nosec G304 -- the key path is operator-supplied by design
	if err != nil {
		return nil, fmt.Errorf("reading Portainer API key file: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsFunc(key, unicode.IsSpace) {
		return nil, errors.New("the Portainer API key file is empty or contains whitespace")
	}
	if options.Timeout == 0 {
		options.Timeout = 20 * time.Second
	}
	if options.UpdateTimeout == 0 {
		options.UpdateTimeout = 600 * time.Second
	}
	client, err := newHTTPSClient(options.BaseURL, options.CAFile, options.FingerprintSHA256, options.Timeout)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		client:        client,
		headers:       map[string]string{"X-API-Key": key},
		updateTimeout: options.UpdateTimeout,
	}, nil
}

func (a *Adapter) request(method, path string, body any, timeout time.Duration) (any, error) {
	status, payload, err := a.client.request(method, path, a.headers, body, timeout)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		detail := "request failed"
		if mapped, ok := payload.(map[string]any); ok {
			if message, ok := mapped["message"].(string); ok {
				detail = message
			} else if details, ok := mapped["details"].(string); ok {
				detail = details
			}
		}
		return nil, &HTTPError{Status: status, Detail: detail}
	}
	return payload, nil
}

// CurrentUsername returns the username the API key authenticates as.
func (a *Adapter) CurrentUsername() (string, error) {
	payload, err := a.request(http.MethodGet, "/api/users/me", nil, 0)
	if err != nil {
		return "", err
	}
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", errors.New("unexpected Portainer user response")
	}
	username, ok := mapped["Username"].(string)
	if !ok {
		return "", errors.New("unexpected Portainer user response")
	}
	return username, nil
}

// ListStacks lists all stacks, marking Git-backed ones.
func (a *Adapter) ListStacks() ([]Stack, error) {
	payload, err := a.request(http.MethodGet, "/api/stacks", nil, 0)
	if err != nil {
		return nil, err
	}
	items, ok := payload.([]any)
	if !ok {
		return nil, errors.New("unexpected Portainer stacks response")
	}
	var stacks []Stack
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stack, err := parseStackEntry(entry)
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, stack)
	}
	return stacks, nil
}

func parseStackEntry(entry map[string]any) (Stack, error) {
	malformed := errors.New("unexpected Portainer stack entry")
	id, ok := asInt(entry["Id"])
	if !ok {
		return Stack{}, malformed
	}
	endpointID, ok := asInt(entry["EndpointId"])
	if !ok {
		return Stack{}, malformed
	}
	name, ok := entry["Name"].(string)
	if !ok {
		return Stack{}, malformed
	}
	status, ok := asInt(entry["Status"])
	if !ok {
		return Stack{}, malformed
	}
	gitConfig, hasGitConfig := entry["GitConfig"].(map[string]any)
	stack := Stack{ID: id, EndpointID: endpointID, Name: name, Status: status,
		GitBacked: hasGitConfig && len(gitConfig) > 0}
	if rawEnv, present := entry["Env"]; present && rawEnv != nil {
		pairs, ok := rawEnv.([]any)
		if !ok {
			return Stack{}, malformed
		}
		for _, pair := range pairs {
			mapped, ok := pair.(map[string]any)
			if !ok {
				continue
			}
			name, _ := mapped["name"].(string)
			value, _ := mapped["value"].(string)
			stack.Env = append(stack.Env, EnvVar{Name: name, Value: value})
		}
	}
	return stack, nil
}

// StackFile returns the live compose file content of a stack.
func (a *Adapter) StackFile(stackID int) (string, error) {
	payload, err := a.request(http.MethodGet, fmt.Sprintf("/api/stacks/%d/file", stackID), nil, 0)
	if err != nil {
		return "", err
	}
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", errors.New("unexpected Portainer stack file response")
	}
	content, ok := mapped["StackFileContent"].(string)
	if !ok {
		return "", errors.New("unexpected Portainer stack file response")
	}
	return content, nil
}

// ImageStatus returns the stack's image status, lowercased.
func (a *Adapter) ImageStatus(stackID int) (string, error) {
	payload, err := a.request(http.MethodGet, fmt.Sprintf("/api/stacks/%d/images_status", stackID), nil, 0)
	if err != nil {
		return "", err
	}
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", errors.New("unexpected Portainer image status response")
	}
	status, ok := mapped["Status"].(string)
	if !ok {
		return "", errors.New("unexpected Portainer image status response")
	}
	return strings.ToLower(status), nil
}

// ServiceImageDigests maps each running service of the stack's compose
// project to its image digest. Only running containers of the stack's
// own project are ever queried — never the whole host.
func (a *Adapter) ServiceImageDigests(stack Stack) (map[string]string, error) {
	filters, err := json.Marshal(map[string][]string{
		"label":  {"com.docker.compose.project=" + stack.Name},
		"status": {"running"},
	})
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api/endpoints/%d/docker/containers/json?%s",
		stack.EndpointID, url.Values{"filters": {string(filters)}}.Encode())
	payload, err := a.request(http.MethodGet, path, nil, 0)
	if err != nil {
		return nil, err
	}
	items, ok := payload.([]any)
	if !ok {
		return nil, errors.New("unexpected Portainer container response")
	}

	result := map[string]string{}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("unexpected Portainer container entry")
		}
		labels, ok := entry["Labels"].(map[string]any)
		imageID, idOK := entry["ImageID"].(string)
		if !ok || !idOK {
			return nil, errors.New("a Portainer container entry is missing image metadata")
		}
		if project, _ := labels["com.docker.compose.project"].(string); project != stack.Name {
			return nil, errors.New("portainer returned a container from another project")
		}
		service, _ := labels["com.docker.compose.service"].(string)
		if service == "" {
			return nil, errors.New("portainer returned an invalid Compose service identity")
		}
		if _, duplicate := result[service]; duplicate {
			return nil, errors.New("portainer returned an invalid Compose service identity")
		}
		if image, ok := entry["Image"].(string); ok {
			if match := pinnedImagePattern.FindStringSubmatch(image); match != nil {
				result[service] = match[1]
				continue
			}
		}
		digest, err := a.imageDigest(stack.EndpointID, imageID, service)
		if err != nil {
			return nil, err
		}
		result[service] = digest
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("portainer returned no running containers for project %q", stack.Name)
	}
	return result, nil
}

func (a *Adapter) imageDigest(endpointID int, imageID, service string) (string, error) {
	path := fmt.Sprintf("/api/endpoints/%d/docker/images/%s/json", endpointID, url.QueryEscape(imageID))
	payload, err := a.request(http.MethodGet, path, nil, 0)
	if err != nil {
		return "", err
	}
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", errors.New("unexpected Portainer image response")
	}
	repoDigests, ok := mapped["RepoDigests"].([]any)
	if !ok {
		return "", errors.New("unexpected Portainer image response")
	}
	var digests []string
	for _, value := range repoDigests {
		reference, ok := value.(string)
		if !ok {
			continue
		}
		if at := strings.LastIndex(reference, "@"); at >= 0 {
			if digest := reference[at+1:]; strings.HasPrefix(digest, "sha256:") &&
				!slices.Contains(digests, digest) {
				digests = append(digests, digest)
			}
		}
	}
	if len(digests) != 1 {
		return "", fmt.Errorf("portainer image for service %q has no unique digest", service)
	}
	return digests[0], nil
}

// UpdateStack redeploys a stack with new compose content. Git-backed
// stacks are refused before any HTTP traffic — they deploy through Git.
func (a *Adapter) UpdateStack(stack Stack, compose string, env []EnvVar, repull bool) error {
	if stack.GitBacked {
		return errors.New("refusing direct update of a Git-backed stack; deploy through Git")
	}
	envBody := make([]map[string]string, 0, len(env))
	for _, pair := range env {
		envBody = append(envBody, map[string]string{"name": pair.Name, "value": pair.Value})
	}
	body := map[string]any{
		"Env":                    envBody,
		"Prune":                  false,
		"RepullImageAndRedeploy": repull,
		"StackFileContent":       compose,
	}
	_, err := a.request(http.MethodPut,
		fmt.Sprintf("/api/stacks/%d?endpointId=%d", stack.ID, stack.EndpointID),
		body, a.updateTimeout)
	return err
}

type httpsClient struct {
	base       string
	httpClient *http.Client
	timeout    time.Duration
}

func newHTTPSClient(baseURL, caFile, fingerprint string, timeout time.Duration) (*httpsClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("portainer base_url must use https")
	}

	var tlsConfig *tls.Config
	switch {
	case fingerprint != "":
		expected, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(fingerprint), ":", ""))
		if err != nil || len(expected) != sha256.Size {
			return nil, errors.New("portainer TLS fingerprint must be 64 hexadecimal characters")
		}
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- pin verified in VerifyConnection
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("portainer TLS fingerprint mismatch: no peer certificate")
				}
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(sum[:], expected) != 1 {
					return errors.New("portainer TLS fingerprint mismatch")
				}
				return nil
			},
			MinVersion: tls.VersionTLS12,
		}
	case caFile != "":
		pem, err := os.ReadFile(caFile) // #nosec G304 -- the CA path is operator-supplied by design
		if err != nil {
			return nil, fmt.Errorf("portainer TLS CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("portainer TLS CA file contains no certificates")
		}
		tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	default:
		return nil, errors.New("portainer TLS trust requires a CA file or a pinned fingerprint")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &httpsClient{
		base:       strings.TrimRight(parsed.String(), "/"),
		httpClient: &http.Client{Transport: transport},
		timeout:    timeout,
	}, nil
}

func (c *httpsClient) request(method, path string, headers map[string]string, body any, timeout time.Duration) (int, any, error) {
	if timeout == 0 {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("portainer request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("portainer request failed: %w", err)
	}
	var payload any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			payload = map[string]any{"message": "non-JSON response"}
		}
	}
	return response.StatusCode, payload, nil
}

func asInt(value any) (int, bool) {
	switch parsed := value.(type) {
	case float64:
		if parsed != float64(int(parsed)) {
			return 0, false
		}
		return int(parsed), true
	case int:
		return parsed, true
	default:
		return 0, false
	}
}
