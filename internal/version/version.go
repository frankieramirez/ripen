// Package version holds the build metadata stamped into release binaries.
package version

import "fmt"

// Populated at build time via -ldflags by .goreleaser.yaml for releases and
// by flake.nix for source builds. A build that stamps nothing keeps these,
// which is how `ripen version` says so rather than inventing a version.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the build metadata as a single human-readable line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
