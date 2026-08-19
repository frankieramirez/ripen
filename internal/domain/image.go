package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ImageReference is a parsed OCI image reference. Tag is the update
// channel; PinnedDigest, when present, is the exact deployed content.
type ImageReference struct {
	Registry     string
	Repository   string
	Tag          string
	Original     string
	PinnedDigest string
}

// Tagged renders the mutable (channel) form: registry/repository:tag.
func (r ImageReference) Tagged() string {
	return fmt.Sprintf("%s/%s:%s", r.Registry, r.Repository, r.Tag)
}

// Pinned renders the tagged form pinned to an exact digest.
func (r ImageReference) Pinned(digest string) string {
	return r.Tagged() + "@" + digest
}

// ParseImageReference parses an image reference as written in a compose
// file. Docker Hub references normalize to registry-1.docker.io with a
// library/ prefix for official images; other registries are preserved as
// written. A missing tag defaults to latest.
func ParseImageReference(value string) (ImageReference, error) {
	original := strings.TrimSpace(value)
	if original == "" {
		return ImageReference{}, errors.New("image must be a mutable tagged reference")
	}

	taggedPart, pinnedDigest, hasDigest := strings.Cut(original, "@")
	if hasDigest && !digestPattern.MatchString(pinnedDigest) {
		return ImageReference{}, errors.New("image digest pin must be a sha256 digest")
	}

	repositoryPart := taggedPart
	tag := "latest"
	// A colon marks a tag only in the last path segment; earlier colons
	// belong to a registry port (registry:5000/repo).
	if cut := strings.LastIndex(taggedPart, ":"); cut > strings.LastIndex(taggedPart, "/") {
		repositoryPart, tag = taggedPart[:cut], taggedPart[cut+1:]
	}

	registry := "registry-1.docker.io"
	repository := repositoryPart
	first, _, _ := strings.Cut(repositoryPart, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		registry, repository, _ = strings.Cut(repositoryPart, "/")
	} else if !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}

	if !repositoryPattern.MatchString(repository) {
		return ImageReference{}, errors.New("image repository is not a valid OCI reference")
	}
	if !tagPattern.MatchString(tag) {
		return ImageReference{}, errors.New("image tag is not a valid OCI tag")
	}

	ref := ImageReference{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Original:   original,
	}
	if hasDigest {
		ref.PinnedDigest = pinnedDigest
	}
	return ref, nil
}
