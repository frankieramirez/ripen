package registry

// Differential test backing the spec's registry-client decision: the
// deliberately minimal client must agree with google/go-containerregistry
// when both resolve the same multi-arch index from the same registry.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/frankieramirez/ripen/internal/domain"
)

func TestMinimalClientAgreesWithGoContainerregistry(t *testing.T) {
	configBytes, _ := json.Marshal(map[string]any{
		"os": "linux", "architecture": "amd64",
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{}},
	})
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(configBytes))

	childManifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size":      len(configBytes),
			"digest":    configDigest,
		},
		"layers": []any{},
	})
	childDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(childManifest))

	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"size":      len(childManifest),
				"digest":    childDigest,
				"platform":  map[string]any{"os": "linux", "architecture": "amd64"},
			},
		},
	})
	indexDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/example/app/manifests/latest":
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Docker-Content-Digest", indexDigest)
			_, _ = w.Write(index)
		case "/v2/example/app/manifests/" + childDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", childDigest)
			_, _ = w.Write(childManifest)
		case "/v2/example/app/blobs/" + configDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.config.v1+json")
			_, _ = w.Write(configBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host := server.Listener.Addr().String()

	image, err := domain.ParseImageReference(host + "/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := New(WithHTTPClient(server.Client())).
		ResolvePlatformDigest(image, Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("minimal client error: %v", err)
	}

	ref, err := name.ParseReference(host + "/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := remote.Get(ref,
		remote.WithTransport(server.Client().Transport),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
	if err != nil {
		t.Fatalf("go-containerregistry error: %v", err)
	}
	img, err := descriptor.Image()
	if err != nil {
		t.Fatal(err)
	}
	reference, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	if minimal != reference.String() {
		t.Errorf("minimal client resolved %s, go-containerregistry resolved %s", minimal, reference)
	}
	if minimal != childDigest {
		t.Errorf("resolved %s, want the amd64 child manifest %s", minimal, childDigest)
	}
}

func TestMinimalClientAgreesWithGoContainerregistryOnArmVariant(t *testing.T) {
	build := func(variant string) ([]byte, string) {
		configBytes, _ := json.Marshal(map[string]any{
			"os": "linux", "architecture": "arm", "variant": variant,
			"rootfs": map[string]any{"type": "layers", "diff_ids": []string{}},
		})
		configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(configBytes))
		manifest, _ := json.Marshal(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]any{
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"size":      len(configBytes),
				"digest":    configDigest,
			},
			"layers": []any{},
		})
		return manifest, fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	}
	v6Manifest, v6Digest := build("v6")
	v7Manifest, v7Digest := build("v7")

	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "size": len(v6Manifest),
				"digest": v6Digest, "platform": map[string]any{"os": "linux", "architecture": "arm", "variant": "v6"}},
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "size": len(v7Manifest),
				"digest": v7Digest, "platform": map[string]any{"os": "linux", "architecture": "arm", "variant": "v7"}},
		},
	})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/example/app/manifests/latest":
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			_, _ = w.Write(index)
		case "/v2/example/app/manifests/" + v6Digest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(v6Manifest)
		case "/v2/example/app/manifests/" + v7Digest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(v7Manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host := server.Listener.Addr().String()

	image, err := domain.ParseImageReference(host + "/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := New(WithHTTPClient(server.Client())).
		ResolvePlatformDigest(image, Platform{OS: "linux", Architecture: "arm", Variant: "v7"})
	if err != nil {
		t.Fatalf("minimal client error: %v", err)
	}

	ref, err := name.ParseReference(host + "/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := remote.Get(ref,
		remote.WithTransport(server.Client().Transport),
		remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "arm", Variant: "v7"}))
	if err != nil {
		t.Fatalf("go-containerregistry error: %v", err)
	}
	img, err := descriptor.Image()
	if err != nil {
		t.Fatal(err)
	}
	reference, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	if minimal != reference.String() {
		t.Errorf("minimal client resolved %s, go-containerregistry resolved %s", minimal, reference)
	}
	if minimal != v7Digest {
		t.Errorf("resolved %s, want the v7 manifest %s", minimal, v7Digest)
	}
}
