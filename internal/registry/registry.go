// Package registry is a deliberately minimal OCI registry client for
// digest observation: resolve what digest a tag points at, per platform.
// It is read-only, anonymous-or-bearer-token only, and fail-closed —
// differential-tested against google/go-containerregistry (see
// differential_test.go). It never pulls image content.
package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
)

const acceptHeader = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Platform selects one manifest out of a multi-arch index. Variant is
// empty when the architecture has no variants.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
}

func (p Platform) String() string {
	if p.Variant != "" {
		return p.OS + "/" + p.Architecture + "/" + p.Variant
	}
	return p.OS + "/" + p.Architecture
}

// Client resolves image digests over the registry HTTP API.
type Client struct {
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client (tests inject a
// fake transport here).
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.httpClient = httpClient }
}

// New builds a Client with a 20-second default timeout.
func New(options ...Option) *Client {
	client := &Client{httpClient: &http.Client{Timeout: 20 * time.Second}}
	for _, option := range options {
		option(client)
	}
	return client
}

// ResolveDigest returns the digest the image's tag currently points at,
// from the Docker-Content-Digest header of a HEAD request. For a
// multi-arch image this is the index digest; use ResolvePlatformDigest
// to match what a single-platform engine reports as running.
func (c *Client) ResolveDigest(image domain.ImageReference) (string, error) {
	response, err := c.manifestRequest(http.MethodHead, image)
	if err != nil {
		return "", err
	}
	defer closeBody(response)
	digest := response.Header.Get("Docker-Content-Digest")
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("registry did not return a sha256 digest")
	}
	return digest, nil
}

// ResolvePlatformDigest returns the manifest digest for one platform.
// For an index, exactly one child manifest must match the platform; for
// a single manifest, the image config's platform is verified against the
// request and a mismatch is an error.
func (c *Client) ResolvePlatformDigest(image domain.ImageReference, platform Platform) (string, error) {
	response, err := c.manifestRequest(http.MethodGet, image)
	if err != nil {
		return "", err
	}
	headerDigest := response.Header.Get("Docker-Content-Digest")
	authorization := response.Request.Header.Get("Authorization")
	body, err := io.ReadAll(response.Body)
	closeBody(response)
	if err != nil {
		return "", fmt.Errorf("registry request failed: %w", err)
	}

	var payload struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
		Config *struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("registry returned malformed manifest JSON: %w", err)
	}

	if payload.Manifests != nil {
		var matches []string
		for _, manifest := range payload.Manifests {
			if manifest.Platform == nil || !digestPattern.MatchString(manifest.Digest) {
				continue
			}
			if manifest.Platform.OS != platform.OS ||
				manifest.Platform.Architecture != platform.Architecture {
				continue
			}
			if platform.Variant != "" && manifest.Platform.Variant != platform.Variant {
				continue
			}
			matches = append(matches, manifest.Digest)
		}
		if len(matches) != 1 {
			return "", fmt.Errorf("registry returned %d manifests for %s", len(matches), platform)
		}
		return matches[0], nil
	}

	if !digestPattern.MatchString(headerDigest) {
		return "", fmt.Errorf("registry did not return a sha256 digest")
	}
	if payload.Config == nil || !digestPattern.MatchString(payload.Config.Digest) {
		return "", fmt.Errorf("registry manifest has no verifiable config digest")
	}
	if err := c.verifyConfigPlatform(image, payload.Config.Digest, authorization, platform); err != nil {
		return "", err
	}
	return headerDigest, nil
}

func (c *Client) verifyConfigPlatform(image domain.ImageReference, configDigest, authorization string, platform Platform) error {
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", image.Registry, image.Repository, configDigest)
	request, err := http.NewRequest(http.MethodGet, blobURL, nil)
	if err != nil {
		return err
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("registry config request failed: %w", err)
	}
	defer closeBody(response)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("registry config HTTP %d", response.StatusCode)
	}
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	}
	if err := json.NewDecoder(response.Body).Decode(&config); err != nil {
		return fmt.Errorf("registry config request failed: %w", err)
	}
	if config.OS != platform.OS || config.Architecture != platform.Architecture ||
		(platform.Variant != "" && config.Variant != platform.Variant) {
		return fmt.Errorf("registry manifest does not match requested platform")
	}
	return nil
}

// manifestRequest performs a manifest request, following one bearer-token
// challenge. The returned response has status 200 and an open body.
func (c *Client) manifestRequest(method string, image domain.ImageReference) (*http.Response, error) {
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", image.Registry, image.Repository, image.Tag)
	request, err := http.NewRequest(method, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", acceptHeader)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("registry request failed: %w", err)
	}
	if response.StatusCode == http.StatusOK {
		return response, nil
	}
	challenge := response.Header.Get("Www-Authenticate")
	status := response.StatusCode
	closeBody(response)
	if status != http.StatusUnauthorized {
		return nil, fmt.Errorf("registry HTTP %d", status)
	}

	token, err := c.bearerToken(challenge)
	if err != nil {
		return nil, err
	}
	retry, err := http.NewRequest(method, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	retry.Header.Set("Accept", acceptHeader)
	retry.Header.Set("Authorization", "Bearer "+token)
	response, err = c.httpClient.Do(retry)
	if err != nil {
		return nil, fmt.Errorf("registry request failed: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		closeBody(response)
		return nil, fmt.Errorf("registry HTTP %d", status)
	}
	return response, nil
}

// bearerToken fetches an anonymous bearer token for a registry challenge.
// The token endpoint (realm) must be https — fail closed, no plaintext
// token traffic.
func (c *Client) bearerToken(challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("unsupported registry authentication challenge")
	}
	values := map[string]string{}
	for _, item := range strings.Split(challenge[7:], ",") {
		key, value, found := strings.Cut(strings.TrimSpace(item), "=")
		if found {
			values[key] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	realm, ok := values["realm"]
	if !ok || realm == "" {
		return "", fmt.Errorf("registry bearer challenge missing realm")
	}
	delete(values, "realm")
	parsed, err := url.Parse(realm)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("registry bearer realm must use https")
	}
	query := parsed.Query()
	for key, value := range values {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()

	response, err := c.httpClient.Get(parsed.String())
	if err != nil {
		return "", fmt.Errorf("registry token request failed: %w", err)
	}
	defer closeBody(response)
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token request failed: HTTP %d", response.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("registry token request failed: %w", err)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("registry token response missing token")
	}
	return token, nil
}

func closeBody(response *http.Response) {
	if response.Body != nil {
		_ = response.Body.Close()
	}
}
