package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/frankieramirez/ripen/internal/domain"
)

var (
	digest0 = "sha256:" + strings.Repeat("0", 64)
	digest1 = "sha256:" + strings.Repeat("1", 64)
	digest2 = "sha256:" + strings.Repeat("2", 64)
	digest6 = "sha256:" + strings.Repeat("6", 64)
	digest7 = "sha256:" + strings.Repeat("7", 64)
)

// fakeTransport routes requests to canned handlers by URL substring, in
// registration order.
type fakeTransport struct {
	handlers []func(*http.Request) (*http.Response, bool)
	requests []*http.Request
}

func (f *fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, request)
	for _, handler := range f.handlers {
		if response, ok := handler(request); ok {
			response.Request = request
			return response, nil
		}
	}
	return nil, fmt.Errorf("unexpected request: %s %s", request.Method, request.URL)
}

func (f *fakeTransport) on(match func(*http.Request) bool, status int, headers map[string]string, body any) {
	f.handlers = append(f.handlers, func(request *http.Request) (*http.Response, bool) {
		if !match(request) {
			return nil, false
		}
		payload, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		if body == nil {
			payload = nil
		}
		header := http.Header{}
		for key, value := range headers {
			header.Set(key, value)
		}
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(string(payload))),
		}, true
	})
}

func client(transport *fakeTransport) *Client {
	return New(WithHTTPClient(&http.Client{Transport: transport}))
}

func mustParse(t *testing.T, image string) domain.ImageReference {
	t.Helper()
	ref, err := domain.ParseImageReference(image)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func pathMatch(fragment string) func(*http.Request) bool {
	return func(request *http.Request) bool {
		return strings.Contains(request.URL.String(), fragment)
	}
}

func TestRejectsInsecureBearerRealm(t *testing.T) {
	transport := &fakeTransport{}
	transport.on(pathMatch("/manifests/latest"), http.StatusUnauthorized, map[string]string{
		"Www-Authenticate": `Bearer realm="http://auth.example/token",service="registry.example"`,
	}, nil)

	_, err := client(transport).ResolveDigest(mustParse(t, "example/app:latest"))

	if err == nil || !strings.Contains(err.Error(), "realm must use https") {
		t.Errorf("error = %v, want 'realm must use https'", err)
	}
}

func TestBearerChallengeIsFollowedOverHTTPS(t *testing.T) {
	transport := &fakeTransport{}
	unauthenticated := func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, "/manifests/latest") &&
			request.Header.Get("Authorization") == ""
	}
	authenticated := func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, "/manifests/latest") &&
			request.Header.Get("Authorization") == "Bearer tok123"
	}
	transport.on(unauthenticated, http.StatusUnauthorized, map[string]string{
		"Www-Authenticate": `Bearer realm="https://auth.example/token",service="registry.example",scope="repository:example/app:pull"`,
	}, nil)
	transport.on(pathMatch("auth.example/token"), http.StatusOK, nil, map[string]string{"token": "tok123"})
	transport.on(authenticated, http.StatusOK, map[string]string{"Docker-Content-Digest": digest1}, nil)

	digest, err := client(transport).ResolveDigest(mustParse(t, "example/app:latest"))
	if err != nil {
		t.Fatalf("ResolveDigest error: %v", err)
	}
	if digest != digest1 {
		t.Errorf("digest = %q, want %q", digest, digest1)
	}

	var tokenRequest *http.Request
	for _, request := range transport.requests {
		if strings.Contains(request.URL.Host, "auth.example") {
			tokenRequest = request
		}
	}
	if tokenRequest == nil {
		t.Fatal("no token request made")
	}
	query := tokenRequest.URL.Query()
	if query.Get("service") != "registry.example" || query.Get("scope") != "repository:example/app:pull" {
		t.Errorf("token request query = %v, want challenge params forwarded", query)
	}
}

func TestResolvesTheLinuxAmd64ManifestDigest(t *testing.T) {
	transport := &fakeTransport{}
	transport.on(pathMatch("/v2/linuxserver/radarr/manifests/latest"), http.StatusOK, map[string]string{
		"Content-Type":          "application/vnd.oci.image.index.v1+json",
		"Docker-Content-Digest": digest0,
	}, map[string]any{
		"manifests": []map[string]any{
			{"digest": digest1, "platform": map[string]any{"os": "linux", "architecture": "arm64"}},
			{"digest": digest2, "platform": map[string]any{"os": "linux", "architecture": "amd64"}},
		},
	})

	digest, err := client(transport).ResolvePlatformDigest(
		mustParse(t, "lscr.io/linuxserver/radarr:latest"),
		Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("ResolvePlatformDigest error: %v", err)
	}
	if digest != digest2 {
		t.Errorf("digest = %q, want the amd64 manifest %q", digest, digest2)
	}
}

func TestUsesVariantToDisambiguateArmManifests(t *testing.T) {
	transport := &fakeTransport{}
	transport.on(pathMatch("/manifests/latest"), http.StatusOK, map[string]string{
		"Docker-Content-Digest": digest0,
	}, map[string]any{
		"manifests": []map[string]any{
			{"digest": digest6, "platform": map[string]any{"os": "linux", "architecture": "arm", "variant": "v6"}},
			{"digest": digest7, "platform": map[string]any{"os": "linux", "architecture": "arm", "variant": "v7"}},
		},
	})

	digest, err := client(transport).ResolvePlatformDigest(
		mustParse(t, "example/app:latest"),
		Platform{OS: "linux", Architecture: "arm", Variant: "v7"})
	if err != nil {
		t.Fatalf("ResolvePlatformDigest error: %v", err)
	}
	if digest != digest7 {
		t.Errorf("digest = %q, want the v7 manifest %q", digest, digest7)
	}
}

func TestVerifiesPlatformForSingleManifest(t *testing.T) {
	transport := &fakeTransport{}
	transport.on(pathMatch("/manifests/latest"), http.StatusOK, map[string]string{
		"Docker-Content-Digest": digest1,
	}, map[string]any{"config": map[string]any{"digest": digest2}})
	transport.on(pathMatch("/blobs/"+digest2), http.StatusOK, nil,
		map[string]any{"os": "linux", "architecture": "arm64", "variant": "v8"})

	_, err := client(transport).ResolvePlatformDigest(
		mustParse(t, "example/app:latest"),
		Platform{OS: "linux", Architecture: "amd64"})

	if err == nil || !strings.Contains(err.Error(), "does not match requested platform") {
		t.Errorf("error = %v, want 'does not match requested platform'", err)
	}
}

func TestResolveDigestRequiresSha256Header(t *testing.T) {
	transport := &fakeTransport{}
	transport.on(pathMatch("/manifests/latest"), http.StatusOK, nil, nil)

	_, err := client(transport).ResolveDigest(mustParse(t, "example/app:latest"))

	if err == nil || !strings.Contains(err.Error(), "sha256 digest") {
		t.Errorf("error = %v, want 'sha256 digest'", err)
	}
}

func TestChallengeScopeWithQuotedCommaSurvivesParsing(t *testing.T) {
	transport := &fakeTransport{}
	unauthenticated := func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, "/manifests/latest") &&
			request.Header.Get("Authorization") == ""
	}
	authenticated := func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, "/manifests/latest") &&
			request.Header.Get("Authorization") == "Bearer tok123"
	}
	transport.on(unauthenticated, http.StatusUnauthorized, map[string]string{
		"Www-Authenticate": `Bearer realm="https://auth.example/token",service="registry.example",scope="repository:example/app:pull,push"`,
	}, nil)
	transport.on(pathMatch("auth.example/token"), http.StatusOK, nil, map[string]string{"token": "tok123"})
	transport.on(authenticated, http.StatusOK, map[string]string{"Docker-Content-Digest": digest1}, nil)

	if _, err := client(transport).ResolveDigest(mustParse(t, "example/app:latest")); err != nil {
		t.Fatalf("ResolveDigest error: %v", err)
	}

	var tokenRequest *http.Request
	for _, request := range transport.requests {
		if strings.Contains(request.URL.Host, "auth.example") {
			tokenRequest = request
		}
	}
	if tokenRequest == nil {
		t.Fatal("no token request made")
	}
	if got := tokenRequest.URL.Query().Get("scope"); got != "repository:example/app:pull,push" {
		t.Errorf("scope = %q, want the quoted comma preserved", got)
	}
}
