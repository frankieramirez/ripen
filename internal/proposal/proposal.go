// Package proposal is the vocabulary of Git-native updates: the change
// Ripen asks a forge to open, and the Proposal that now exists. A
// Proposal is never merged or deployed by Ripen — a human reviews it,
// merges it, and the next Monitor run confirms what the forge deployed.
package proposal

// Change is one digest-pin Proposal to open.
type Change struct {
	// Label names the Service the pin belongs to, as an operator reads
	// it: "stack" or "stack/service". It titles the change and seeds the
	// branch name.
	Label string
	// RepositoryPath is the Compose file's path inside the repository.
	RepositoryPath string
	// ExpectedContent is the live reviewed document. A repository whose
	// base branch holds something else has drifted, and the Proposal is
	// refused rather than opened against unknown content.
	ExpectedContent string
	// ProposedContent is the same document with one image pinned.
	ProposedContent string
	// Digest is the digest being pinned.
	Digest string
}

// Result is the Proposal that exists after the call, whether or not this
// call is the one that created it.
type Result struct {
	URL     string
	Created bool
}

// Port opens digest-pin Proposals on a forge.
type Port interface {
	Propose(change Change) (Result, error)
}
