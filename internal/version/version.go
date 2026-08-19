// Package version holds the build metadata stamped into release binaries.
package version

import "fmt"

// Populated at build time via -ldflags; see .goreleaser.yaml.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the build metadata as a single human-readable line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
