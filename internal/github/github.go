// Package github opens digest-pin Proposals as pull requests. It never
// merges, never deploys, and never force-pushes: it writes one file on
// one branch and opens one pull request, idempotently, so a run that
// repeats finds the Proposal it already made instead of making another.
package github

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/frankieramirez/ripen/internal/proposal"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	slugPattern   = regexp.MustCompile(`[^a-z0-9-]+`)
)

// HTTPError is a non-2xx GitHub API response.
type HTTPError struct {
	Status int
	Detail string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GitHub HTTP %d: %s", e.Status, e.Detail)
}

// Options configures an Adapter.
type Options struct {
	Repository string // owner/name
	BaseBranch string
	TokenFile  string
	Timeout    time.Duration // default 20s
	BaseURL    string        // default https://api.github.com
	HTTPClient *http.Client
}

// Adapter opens Proposals against one repository.
type Adapter struct {
	repository string
	owner      string
	baseBranch string
	baseURL    string
	headers    map[string]string
	client     *http.Client
	timeout    time.Duration
}

// New builds an Adapter, reading and validating the token file.
func New(options Options) (*Adapter, error) {
	owner, name, ok := strings.Cut(options.Repository, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, errors.New("the GitHub repository must be owner/repository")
	}
	info, err := os.Stat(options.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub token file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		// A token readable by the rest of the host is a token to treat as
		// already leaked.
		return nil, errors.New("the GitHub token file must not be readable by group or others")
	}
	raw, err := os.ReadFile(options.TokenFile) // #nosec G304 -- the token path is operator-supplied by design
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsFunc(token, unicode.IsSpace) {
		return nil, errors.New("the GitHub token file is empty or contains whitespace")
	}
	if options.Timeout == 0 {
		options.Timeout = 20 * time.Second
	}
	if options.BaseURL == "" {
		options.BaseURL = "https://api.github.com"
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{}
	}
	return &Adapter{
		repository: options.Repository,
		owner:      owner,
		baseBranch: options.BaseBranch,
		baseURL:    strings.TrimRight(options.BaseURL, "/"),
		headers: map[string]string{
			"Accept":               "application/vnd.github+json",
			"Authorization":        "Bearer " + token,
			"X-GitHub-Api-Version": "2022-11-28",
		},
		client:  options.HTTPClient,
		timeout: options.Timeout,
	}, nil
}

// Propose opens (or finds) the digest-pin pull request for one change.
func (a *Adapter) Propose(change proposal.Change) (proposal.Result, error) {
	if !digestPattern.MatchString(change.Digest) {
		return proposal.Result{}, errors.New("a proposal digest must be a sha256 digest")
	}
	if err := validRepositoryPath(change.RepositoryPath); err != nil {
		return proposal.Result{}, err
	}
	filePath := escapePath(change.RepositoryPath)
	baseRef := url.QueryEscape(a.baseBranch)

	basePayload, err := a.request(http.MethodGet, "/contents/"+filePath+"?ref="+baseRef, nil, false)
	if err != nil {
		return proposal.Result{}, err
	}
	baseContent, baseFileSHA, err := repositoryFile(basePayload)
	if err != nil {
		return proposal.Result{}, err
	}
	if subtle.ConstantTimeCompare([]byte(baseContent), []byte(change.ExpectedContent)) != 1 {
		// The repository is the source of truth for a Git-backed stack.
		// If it does not hold what is deployed, the difference is a human
		// question, not something to paper over with another commit.
		return proposal.Result{}, errors.New(
			"the repository source differs from the live reviewed compose file")
	}

	short := strings.TrimPrefix(change.Digest, "sha256:")[:12]
	branch, err := branchName(change.Label, short)
	if err != nil {
		return proposal.Result{}, err
	}
	encodedBranch := url.QueryEscape(branch)

	branchContent := baseContent
	branchFileSHA := baseFileSHA
	existing, err := a.request(http.MethodGet, "/git/ref/heads/"+encodedBranch, nil, true)
	if err != nil {
		return proposal.Result{}, err
	}
	if existing == nil {
		sourceRef, err := a.request(http.MethodGet, "/git/ref/heads/"+baseRef, nil, false)
		if err != nil {
			return proposal.Result{}, err
		}
		sourceSHA, err := refSHA(sourceRef)
		if err != nil {
			return proposal.Result{}, err
		}
		if _, err := a.request(http.MethodPost, "/git/refs", map[string]any{
			"ref": "refs/heads/" + branch,
			"sha": sourceSHA,
		}, false); err != nil {
			return proposal.Result{}, err
		}
	} else {
		branchFile, err := a.request(http.MethodGet, "/contents/"+filePath+"?ref="+encodedBranch, nil, false)
		if err != nil {
			return proposal.Result{}, err
		}
		if branchContent, branchFileSHA, err = repositoryFile(branchFile); err != nil {
			return proposal.Result{}, err
		}
		if branchContent != change.ExpectedContent && branchContent != change.ProposedContent {
			return proposal.Result{}, errors.New("the existing proposal branch contains an unexpected change")
		}
	}

	if branchContent != change.ProposedContent {
		if _, err := a.request(http.MethodPut, "/contents/"+filePath, map[string]any{
			"message": fmt.Sprintf("Pin %s to %s", change.Label, short),
			"content": base64.StdEncoding.EncodeToString([]byte(change.ProposedContent)),
			"sha":     branchFileSHA,
			"branch":  branch,
		}, false); err != nil {
			return proposal.Result{}, err
		}
	}

	query := url.Values{
		"state": {"open"},
		"head":  {a.owner + ":" + branch},
		"base":  {a.baseBranch},
	}
	pulls, err := a.request(http.MethodGet, "/pulls?"+query.Encode(), nil, false)
	if err != nil {
		return proposal.Result{}, err
	}
	open, ok := pulls.([]any)
	if !ok {
		return proposal.Result{}, errors.New("GitHub returned an unreadable pull request list")
	}
	if len(open) == 1 {
		if url, ok := pullURL(open[0]); ok {
			return proposal.Result{URL: url, Created: false}, nil
		}
		return proposal.Result{}, errors.New("GitHub returned an unreadable pull request")
	}
	if len(open) > 1 {
		return proposal.Result{}, errors.New("GitHub returned an ambiguous pull request result")
	}

	created, err := a.request(http.MethodPost, "/pulls", map[string]any{
		"title": fmt.Sprintf("Pin %s to %s", change.Label, short),
		"head":  branch,
		"base":  a.baseBranch,
		"body": "Automated digest-pin proposal from Ripen.\n\n" +
			fmt.Sprintf("Service: `%s`\n", change.Label) +
			fmt.Sprintf("Digest: `%s`\n\n", change.Digest) +
			"This proposal does not merge or deploy itself.",
	}, false)
	if err != nil {
		return proposal.Result{}, err
	}
	url, ok := pullURL(created)
	if !ok {
		return proposal.Result{}, errors.New("GitHub returned an unreadable pull request")
	}
	return proposal.Result{URL: url, Created: true}, nil
}

func (a *Adapter) request(method, endpoint string, body map[string]any, allowNotFound bool) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method,
		a.baseURL+"/repos/"+a.repository+endpoint, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range a.headers {
		request.Header.Set(key, value)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := a.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the GitHub request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("the GitHub request failed: %w", err)
	}

	var payload any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil, errors.New("GitHub returned malformed JSON")
			}
			payload = nil
		}
	}
	if response.StatusCode == http.StatusNotFound && allowNotFound {
		return nil, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := "request failed"
		if mapped, ok := payload.(map[string]any); ok {
			if message, ok := mapped["message"].(string); ok {
				detail = message
			}
		}
		return nil, &HTTPError{Status: response.StatusCode, Detail: detail}
	}
	return payload, nil
}

func validRepositoryPath(value string) error {
	cleaned := path.Clean(value)
	extension := path.Ext(cleaned)
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) ||
		cleaned != value || strings.Contains(value, "..") ||
		(extension != ".yaml" && extension != ".yml") {
		return errors.New("a proposal repository path must be a relative YAML path")
	}
	return nil
}

func escapePath(value string) string {
	segments := strings.Split(value, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// repositoryFile decodes a contents response: GitHub wraps base64 in
// newlines, which the standard decoder will not accept.
func repositoryFile(payload any) (string, string, error) {
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", "", errors.New("GitHub returned an invalid repository file")
	}
	encoded, contentOK := mapped["content"].(string)
	sha, shaOK := mapped["sha"].(string)
	if !contentOK || !shaOK {
		return "", "", errors.New("the GitHub repository file is missing content or sha")
	}
	content, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return "", "", errors.New("the GitHub repository file content is invalid")
	}
	return string(content), sha, nil
}

func refSHA(payload any) (string, error) {
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", errors.New("the GitHub base branch response is invalid")
	}
	object, ok := mapped["object"].(map[string]any)
	if !ok {
		return "", errors.New("the GitHub base branch response is invalid")
	}
	sha, ok := object["sha"].(string)
	if !ok || sha == "" {
		return "", errors.New("the GitHub base branch response is invalid")
	}
	return sha, nil
}

func pullURL(payload any) (string, bool) {
	mapped, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	url, ok := mapped["html_url"].(string)
	return url, ok && url != ""
}

// branchName is deterministic: the same Service and digest always name
// the same branch, which is what makes proposing idempotent.
func branchName(label, shortDigest string) (string, error) {
	slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if slug == "" {
		return "", errors.New("the proposal label has no safe branch name")
	}
	if len(slug) > 80 {
		slug = strings.Trim(slug[:80], "-")
	}
	return "ripen/" + slug + "-" + shortDigest, nil
}
