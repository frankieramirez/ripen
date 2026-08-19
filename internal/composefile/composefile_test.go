package composefile

import (
	"strings"
	"testing"
)

const digest = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

const multiService = `# Reviewed by the operator, 2026-08-01.
x-common: &common
  restart: unless-stopped
  networks: [edge]

services:
  web:
    <<: *common
    image: ghcr.io/example/web:1.4.0   # pinned by hand until 2.0
    ports:
      - "8080:80"
  sidecar:
    <<: *common
    image: ghcr.io/example/sidecar:0.9.1

networks:
  edge: {}
`

func TestPinningOneImagePreservesCommentsAnchorsAndSiblings(t *testing.T) {
	updated, err := ReplaceServiceImage(multiService, "web",
		"ghcr.io/example/web:1.4.0", "ghcr.io/example/web:1.4.0@"+digest)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(updated, `image: "ghcr.io/example/web:1.4.0@`+digest+`"`) {
		t.Errorf("pinned image missing or unquoted:\n%s", updated)
	}
	for _, kept := range []string{
		"# Reviewed by the operator, 2026-08-01.",
		"x-common: &common",
		"<<: *common",
		"# pinned by hand until 2.0",
		"image: ghcr.io/example/sidecar:0.9.1",
		`      - "8080:80"`,
	} {
		if !strings.Contains(updated, kept) {
			t.Errorf("rewrite dropped %q:\n%s", kept, updated)
		}
	}
}

func TestPinningRewritesOnlyTheTargetServicesBytes(t *testing.T) {
	updated, err := ReplaceServiceImage(multiService, "web",
		"ghcr.io/example/web:1.4.0", "ghcr.io/example/web:1.4.0@"+digest)
	if err != nil {
		t.Fatal(err)
	}

	before, after, _ := strings.Cut(multiService, "image: ghcr.io/example/web:1.4.0")
	if !strings.HasPrefix(updated, before) {
		t.Error("bytes before the pinned image changed")
	}
	if !strings.HasSuffix(updated, after) {
		t.Error("bytes after the pinned image changed")
	}
}

func TestQuotedImageScalarsArePinnedInPlace(t *testing.T) {
	for _, source := range []string{
		"services:\n  web:\n    image: \"nginx:1.27\"\n",
		"services:\n  web:\n    image: 'nginx:1.27'\n",
	} {
		updated, err := ReplaceServiceImage(source, "web", "nginx:1.27", "nginx:1.27@"+digest)
		if err != nil {
			t.Fatalf("%q: %v", source, err)
		}
		if want := "image: \"nginx:1.27@" + digest + "\"\n"; !strings.HasSuffix(updated, want) {
			t.Errorf("updated = %q, want it to end with %q", updated, want)
		}
	}
}

func TestPinningRefusesWhenTheImageChangedSincePlanning(t *testing.T) {
	_, err := ReplaceServiceImage(multiService, "web", "ghcr.io/example/web:1.3.0", "whatever")

	if err == nil || !strings.Contains(err.Error(), "changed before replacement") {
		t.Errorf("error = %v, want the stale-plan refusal", err)
	}
}

func TestPinningRefusesAnAliasedService(t *testing.T) {
	source := `x-web: &web
  image: nginx:1.27
services:
  web: *web
`

	_, err := ReplaceServiceImage(source, "web", "nginx:1.27", "nginx:1.27@"+digest)

	if err == nil || !strings.Contains(err.Error(), "aliased") {
		t.Errorf("error = %v, want the aliased-entry refusal", err)
	}
}

func TestPinningRefusesAnAnchoredImage(t *testing.T) {
	source := "services:\n  web:\n    image: &shared nginx:1.27\n  mirror:\n    image: *shared\n"

	_, err := ReplaceServiceImage(source, "web", "nginx:1.27", "nginx:1.27@"+digest)

	if err == nil || !strings.Contains(err.Error(), "anchored") {
		t.Errorf("error = %v, want the anchored-image refusal", err)
	}
}

func TestPinningRefusesANonScalarImage(t *testing.T) {
	source := "services:\n  web:\n    image:\n      - nginx:1.27\n"

	_, err := ReplaceServiceImage(source, "web", "nginx:1.27", "nginx:1.27@"+digest)

	if err == nil || !strings.Contains(err.Error(), "literal image reference") {
		t.Errorf("error = %v, want the literal-image refusal", err)
	}
}

func TestServicesAreReportedSorted(t *testing.T) {
	services, err := Services(multiService)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := strings.Join(services, ","), "sidecar,web"; got != want {
		t.Errorf("services = %q, want %q", got, want)
	}
}

func TestServiceImageReadsTheReferenceAsWritten(t *testing.T) {
	image, err := ServiceImage("services:\n  web:\n    image: ${REGISTRY}/web:${TAG}\n", "web")
	if err != nil {
		t.Fatal(err)
	}

	if want := "${REGISTRY}/web:${TAG}"; image != want {
		t.Errorf("image = %q, want %q — interpolation is the caller's to notice", image, want)
	}
}

func TestAMissingServiceIsAnError(t *testing.T) {
	_, err := ServiceImage(multiService, "absent")

	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error = %v, want the missing-entry error", err)
	}
}
