// Package version holds the build metadata stamped into release binaries.
package version

import (
	"fmt"
	"runtime/debug"
)

// Populated at build time via -ldflags by .goreleaser.yaml for releases and
// by flake.nix for source builds. A build that stamps nothing falls back to
// runtime/debug.ReadBuildInfo, so `go install pkg@version` still reports
// the module version rather than "dev". A build with neither keeps these
// defaults, which is how `ripen version` says so rather than inventing one.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	Version, Commit, Date = fromBuildInfo(Version, Commit, Date, info)
}

// fromBuildInfo fills any remaining ldflag defaults from the toolchain's
// build info: Main.Version for `go install pkg@version`, vcs.revision and
// vcs.time for a VCS checkout. Stamped values always win.
func fromBuildInfo(version, commit, date string, info *debug.BuildInfo) (string, string, string) {
	if info == nil {
		return version, commit, date
	}
	if version == "dev" {
		// A local `go build` records "(devel)", which is not a version.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
	}
	if commit == "none" {
		if rev := setting(info, "vcs.revision"); rev != "" {
			commit = rev
		}
	}
	if date == "unknown" {
		if t := setting(info, "vcs.time"); t != "" {
			date = t
		}
	}
	return version, commit, date
}

func setting(info *debug.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// String renders the build metadata as a single human-readable line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
