package version

import (
	"runtime/debug"
	"testing"
)

var (
	versionAtInit string
	commitAtInit  string
	dateAtInit    string
)

func init() {
	versionAtInit, commitAtInit, dateAtInit = Version, Commit, Date
}

func TestStringCarriesVersionCommitAndDate(t *testing.T) {
	t.Cleanup(func() {
		Version, Commit, Date = "dev", "none", "unknown"
	})

	Version, Commit, Date = "1.2.3", "abc1234", "2026-08-19"
	if got, want := String(), "1.2.3 (commit abc1234, built 2026-08-19)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringDefaultsForDevBuilds(t *testing.T) {
	t.Cleanup(func() {
		Version, Commit, Date = "dev", "none", "unknown"
	})

	Version, Commit, Date = "dev", "none", "unknown"
	if got, want := String(), "dev (commit none, built unknown)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestInitAppliesBuildInfoToUnstampedVars(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("ReadBuildInfo returned no info")
	}

	if v := info.Main.Version; v != "" && v != "(devel)" && versionAtInit == "dev" {
		t.Errorf("Version still %q after init, though Main.Version is %q", versionAtInit, v)
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" && commitAtInit != s.Value {
				t.Errorf("Commit = %q after init, want vcs.revision %q", commitAtInit, s.Value)
			}
		case "vcs.time":
			if s.Value != "" && dateAtInit != s.Value {
				t.Errorf("Date = %q after init, want vcs.time %q", dateAtInit, s.Value)
			}
		}
	}
}

func TestBuildInfoFillsVersionWhenLdflagsAreUnset(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.0.0"}}

	version, commit, date := fromBuildInfo("dev", "none", "unknown", info)

	if version != "v1.0.0" {
		t.Errorf("version = %q, want v1.0.0 from Main.Version", version)
	}
	if commit != "none" {
		t.Errorf("commit = %q, want none: go install records no VCS revision", commit)
	}
	if date != "unknown" {
		t.Errorf("date = %q, want unknown: go install records no VCS time", date)
	}
}

func TestBuildInfoFillsCommitAndDateFromVCSWhenLdflagsAreUnset(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "31dfb79deadbeef"},
			{Key: "vcs.time", Value: "2026-08-24T00:00:00Z"},
		},
	}

	version, commit, date := fromBuildInfo("dev", "none", "unknown", info)

	if version != "dev" {
		t.Errorf("version = %q, want dev: (devel) is an unstamped local build", version)
	}
	if commit != "31dfb79deadbeef" {
		t.Errorf("commit = %q, want the vcs.revision", commit)
	}
	if date != "2026-08-24T00:00:00Z" {
		t.Errorf("date = %q, want the vcs.time", date)
	}
}

func TestLdflagsWinOverBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "ffffffffffff"},
			{Key: "vcs.time", Value: "1999-01-01T00:00:00Z"},
		},
	}

	version, commit, date := fromBuildInfo("1.0.0", "31dfb79", "2026-08-25T00:00:00Z", info)

	if version != "1.0.0" {
		t.Errorf("version = %q, want the ldflag value", version)
	}
	if commit != "31dfb79" {
		t.Errorf("commit = %q, want the ldflag value", commit)
	}
	if date != "2026-08-25T00:00:00Z" {
		t.Errorf("date = %q, want the ldflag value", date)
	}
}

func TestMissingBuildInfoKeepsTheDefaults(t *testing.T) {
	version, commit, date := fromBuildInfo("dev", "none", "unknown", nil)

	if version != "dev" || commit != "none" || date != "unknown" {
		t.Errorf("fromBuildInfo(defaults, nil) = %q, %q, %q, want the defaults", version, commit, date)
	}
}

func TestEachLdflagFallsBackIndependently(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.0.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "31dfb79deadbeef"},
			{Key: "vcs.time", Value: "2026-08-24T00:00:00Z"},
		},
	}

	version, commit, date := fromBuildInfo("1.0.0", "none", "unknown", info)

	if version != "1.0.0" {
		t.Errorf("version = %q, want the stamped ldflag", version)
	}
	if commit != "31dfb79deadbeef" {
		t.Errorf("commit = %q, want vcs.revision for the unstamped field", commit)
	}
	if date != "2026-08-24T00:00:00Z" {
		t.Errorf("date = %q, want vcs.time for the unstamped field", date)
	}
}
