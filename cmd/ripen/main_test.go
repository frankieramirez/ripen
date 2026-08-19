package main

import (
	"strings"
	"testing"
)

func TestVersionCommandPrintsBuildMetadata(t *testing.T) {
	var stdout, stderr strings.Builder

	code := run([]string{"version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got, want := stdout.String(), "ripen dev (commit none, built unknown)\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestNoArgumentsIsAUsageError(t *testing.T) {
	var stdout, stderr strings.Builder

	code := run(nil, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	var stdout, stderr strings.Builder

	code := run([]string{"frobnicate"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "frobnicate") {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr.String())
	}
}
