package version

import "testing"

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
	if got, want := String(), "dev (commit none, built unknown)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
