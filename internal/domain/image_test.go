package domain

import (
	"strings"
	"testing"
)

const (
	oldDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestParseImageReferenceNormalizesDockerHubAndPreservesGHCR(t *testing.T) {
	docker, err := ParseImageReference("nginx:stable")
	if err != nil {
		t.Fatalf("ParseImageReference(nginx:stable) error: %v", err)
	}
	ghcr, err := ParseImageReference("ghcr.io/example/app:latest")
	if err != nil {
		t.Fatalf("ParseImageReference(ghcr.io/example/app:latest) error: %v", err)
	}

	if docker.Registry != "registry-1.docker.io" {
		t.Errorf("docker.Registry = %q, want registry-1.docker.io", docker.Registry)
	}
	if docker.Repository != "library/nginx" {
		t.Errorf("docker.Repository = %q, want library/nginx", docker.Repository)
	}
	if docker.Tag != "stable" {
		t.Errorf("docker.Tag = %q, want stable", docker.Tag)
	}
	if ghcr.Registry != "ghcr.io" {
		t.Errorf("ghcr.Registry = %q, want ghcr.io", ghcr.Registry)
	}
	if ghcr.Repository != "example/app" {
		t.Errorf("ghcr.Repository = %q, want example/app", ghcr.Repository)
	}
}

func TestParseImageReferenceDefaultsToLatestTag(t *testing.T) {
	image, err := ParseImageReference("ghcr.io/example/app")
	if err != nil {
		t.Fatalf("ParseImageReference error: %v", err)
	}
	if image.Tag != "latest" {
		t.Errorf("Tag = %q, want latest", image.Tag)
	}
}

func TestTaggedDigestReferencePreservesUpdateChannelAndPin(t *testing.T) {
	image, err := ParseImageReference("ghcr.io/example/app:latest@" + oldDigest)
	if err != nil {
		t.Fatalf("ParseImageReference error: %v", err)
	}

	if got, want := image.Tagged(), "ghcr.io/example/app:latest"; got != want {
		t.Errorf("Tagged() = %q, want %q", got, want)
	}
	if image.PinnedDigest != oldDigest {
		t.Errorf("PinnedDigest = %q, want %q", image.PinnedDigest, oldDigest)
	}
	if got, want := image.Pinned(newDigest), "ghcr.io/example/app:latest@"+newDigest; got != want {
		t.Errorf("Pinned() = %q, want %q", got, want)
	}
}

func TestInvalidImageReferencesAreRejected(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/example/../../escape:latest",
		"ghcr.io/example/app:bad/tag",
		"ghcr.io/Example/app:latest",
	} {
		if _, err := ParseImageReference(image); err == nil {
			t.Errorf("ParseImageReference(%q) succeeded, want error", image)
		} else if !strings.Contains(err.Error(), "valid OCI") {
			t.Errorf("ParseImageReference(%q) error = %q, want it to mention 'valid OCI'", image, err)
		}
	}
}

func TestMalformedDigestPinIsRejected(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/example/app:latest@sha256:short",
		"ghcr.io/example/app:latest@" + oldDigest + "@" + newDigest,
		"",
	} {
		if _, err := ParseImageReference(image); err == nil {
			t.Errorf("ParseImageReference(%q) succeeded, want error", image)
		}
	}
}
