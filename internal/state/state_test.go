package state

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
)

var (
	now       = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	oldDigest = "sha256:" + strings.Repeat("1", 64)
	newDigest = "sha256:" + strings.Repeat("2", 64)

	exampleApp = Key{Backend: domain.BackendPortainer, Stack: "example-app"}
	radarr     = Key{Backend: domain.BackendPortainer, Stack: "arr", Service: "radarr"}
	sonarr     = Key{Backend: domain.BackendPortainer, Stack: "arr", Service: "sonarr"}
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(filepath.Join(dir, "state", "updater.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStatePersistsBaselineAndCandidateObservations(t *testing.T) {
	dir := t.TempDir()
	first := open(t, dir)

	if err := first.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	observation, err := first.ObserveCandidate(exampleApp, newDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := first.ObserveCandidate(exampleApp, newDigest, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if observation.Count != 1 {
		t.Errorf("first observation Count = %d, want 1", observation.Count)
	}
	if second.Count != 2 {
		t.Errorf("second observation Count = %d, want 2", second.Count)
	}
	if !second.FirstSeen.Equal(now) {
		t.Errorf("second observation FirstSeen = %v, want %v", second.FirstSeen, now)
	}
	if !second.LastSeen.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("second observation LastSeen = %v", second.LastSeen)
	}

	digest, ok, err := open(t, dir).AcceptedDigest(exampleApp)
	if err != nil || !ok || digest != oldDigest {
		t.Errorf("reopened AcceptedDigest = %q, %v, %v; want %q", digest, ok, err, oldDigest)
	}
}

func TestStateCreatesParentDirectoryIfMissing(t *testing.T) {
	open(t, t.TempDir())
}

func TestStatePersistsIndependentServiceDigestsForOneStack(t *testing.T) {
	dir := t.TempDir()
	store := open(t, dir)

	if err := store.SetAcceptedDigest(radarr, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAcceptedDigest(sonarr, newDigest, now); err != nil {
		t.Fatal(err)
	}

	reopened := open(t, dir)
	if digest, ok, _ := reopened.AcceptedDigest(radarr); !ok || digest != oldDigest {
		t.Errorf("radarr digest = %q, %v; want %q", digest, ok, oldDigest)
	}
	if digest, ok, _ := reopened.AcceptedDigest(sonarr); !ok || digest != newDigest {
		t.Errorf("sonarr digest = %q, %v; want %q", digest, ok, newDigest)
	}
	status, err := reopened.Status(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.AcceptedDigests) != 2 {
		t.Fatalf("AcceptedDigests = %+v, want 2 records", status.AcceptedDigests)
	}
	if status.AcceptedDigests[0].Key != radarr || status.AcceptedDigests[0].Digest != oldDigest {
		t.Errorf("AcceptedDigests[0] = %+v", status.AcceptedDigests[0])
	}
	if status.AcceptedDigests[1].Key != sonarr || status.AcceptedDigests[1].Digest != newDigest {
		t.Errorf("AcceptedDigests[1] = %+v", status.AcceptedDigests[1])
	}
}

func TestStatePersistsAndClearsPendingGitProposal(t *testing.T) {
	dir := t.TempDir()
	store := open(t, dir)

	if err := store.SetAcceptedDigest(radarr, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingProposal(radarr, newDigest, "https://github.com/example/nas/pull/42", now); err != nil {
		t.Fatal(err)
	}

	pending, err := open(t, dir).PendingProposal(radarr)
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Digest != newDigest || !strings.HasSuffix(pending.URL, "/pull/42") {
		t.Fatalf("PendingProposal = %+v", pending)
	}
	status, err := open(t, dir).Status(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.PendingProposals) != 1 || status.PendingProposals[0].Key != radarr ||
		status.PendingProposals[0].Digest != newDigest || status.PendingProposals[0].URL != pending.URL {
		t.Fatalf("Status PendingProposals = %+v", status.PendingProposals)
	}

	if err := store.SetAcceptedDigest(radarr, newDigest, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingProposal(radarr); err != nil || pending != nil {
		t.Errorf("PendingProposal after accept = %+v, %v; want nil", pending, err)
	}
}

func TestClearPendingProposalReportsWhetherRecordExisted(t *testing.T) {
	store := open(t, t.TempDir())

	if existed, err := store.ClearPendingProposal(radarr); err != nil || existed {
		t.Errorf("clear with nothing pending = %v, %v; want false", existed, err)
	}
	if err := store.SetPendingProposal(radarr, newDigest, "https://github.com/example/nas/pull/42", now); err != nil {
		t.Fatal(err)
	}
	if existed, err := store.ClearPendingProposal(radarr); err != nil || !existed {
		t.Errorf("clear with pending record = %v, %v; want true", existed, err)
	}
	if existed, err := store.ClearPendingProposal(radarr); err != nil || existed {
		t.Errorf("clear after removal = %v, %v; want false", existed, err)
	}
}

func TestLeaseExcludesConcurrentRunAndExpires(t *testing.T) {
	store := open(t, t.TempDir())

	firstToken, ok, err := store.AcquireLease(now, 60)
	if err != nil || !ok {
		t.Fatalf("first acquire = %v, %v; want acquired", ok, err)
	}
	if _, ok, err := store.AcquireLease(now.Add(30*time.Second), 60); err != nil || ok {
		t.Fatalf("concurrent acquire = %v, %v; want refused", ok, err)
	}
	secondToken, ok, err := store.AcquireLease(now.Add(61*time.Second), 60)
	if err != nil || !ok {
		t.Fatalf("post-expiry acquire = %v, %v; want acquired", ok, err)
	}

	if status, _ := store.Status(now.Add(70 * time.Second)); !status.LeaseActive {
		t.Error("LeaseActive = false while current lease unexpired, want true")
	}
	if err := store.ReleaseLease(firstToken); err != nil {
		t.Fatal(err)
	}
	if status, _ := store.Status(now.Add(70 * time.Second)); !status.LeaseActive {
		t.Error("LeaseActive = false after releasing a stale token, want true")
	}
	if err := store.ReleaseLease(secondToken); err != nil {
		t.Fatal(err)
	}
	if status, _ := store.Status(now.Add(70 * time.Second)); status.LeaseActive {
		t.Error("LeaseActive = true after releasing the current token, want false")
	}
}

func TestBreakerRequiresExplicitClearReason(t *testing.T) {
	store := open(t, t.TempDir())

	if err := store.OpenBreaker("rollback failed", now); err != nil {
		t.Fatal(err)
	}
	status, _ := store.Status(now)
	if !status.BreakerOpen || status.BreakerReason != "rollback failed" {
		t.Fatalf("Status = %+v, want open breaker with reason", status)
	}

	err := store.ClearBreaker("   ", now)
	if err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("ClearBreaker(blank) error = %v, want 'reason is required'", err)
	}
	if status, _ := store.Status(now); !status.BreakerOpen {
		t.Error("BreakerOpen = false after refused clear, want true")
	}

	if err := store.ClearBreaker("manually verified example-app", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _ := store.Status(now); status.BreakerOpen {
		t.Error("BreakerOpen = true after clear, want false")
	}
}

func TestAttemptsCarryRunIDAndActor(t *testing.T) {
	store := open(t, t.TempDir())

	attempt := Attempt{
		Key:       radarr,
		RunID:     "0198b3a0-0000-7000-8000-000000000001",
		Actor:     domain.ActorDaemon,
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Result:    domain.ResultUpdated,
		Detail:    "applied",
	}
	if err := store.RecordAttempt(attempt, now); err != nil {
		t.Fatal(err)
	}

	attempts, err := store.Attempts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("Attempts = %d records, want 1", len(attempts))
	}
	got := attempts[0]
	if got.Key != radarr || got.RunID != attempt.RunID || got.Actor != domain.ActorDaemon ||
		got.Result != domain.ResultUpdated || got.OldDigest != oldDigest || got.NewDigest != newDigest {
		t.Errorf("attempt = %+v", got)
	}
	if !got.AttemptedAt.Equal(now) {
		t.Errorf("AttemptedAt = %v, want %v", got.AttemptedAt, now)
	}
}

func TestNotifierHealthPersistsAcrossReopens(t *testing.T) {
	dir := t.TempDir()
	store := open(t, dir)

	health, err := store.NotifierHealth()
	if err != nil {
		t.Fatal(err)
	}
	if health.LastSuccessAt != nil || health.ConsecutiveFailures != 0 {
		t.Fatalf("cold NotifierHealth = %+v, want zero", health)
	}

	if err := store.RecordNotifierFailure(); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordNotifierFailure(); err != nil {
		t.Fatal(err)
	}
	if health, _ = store.NotifierHealth(); health.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", health.ConsecutiveFailures)
	}

	if err := store.RecordNotifierSuccess(now); err != nil {
		t.Fatal(err)
	}
	health, _ = open(t, dir).NotifierHealth()
	if health.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures after success = %d, want 0", health.ConsecutiveFailures)
	}
	if health.LastSuccessAt == nil || !health.LastSuccessAt.Equal(now) {
		t.Errorf("LastSuccessAt = %v, want %v", health.LastSuccessAt, now)
	}
}

func TestSuppressionKeyedByEventStackServiceAndResettable(t *testing.T) {
	dir := t.TempDir()
	store := open(t, dir)

	if state, ok, _ := store.SuppressionState("breaker.opened", "arr", "radarr"); ok || state != "" {
		t.Errorf("cold SuppressionState = %q, %v; want absent", state, ok)
	}
	if err := store.SetSuppressionState("breaker.opened", "arr", "radarr", "open", now); err != nil {
		t.Fatal(err)
	}
	if state, ok, _ := store.SuppressionState("breaker.opened", "arr", "radarr"); !ok || state != "open" {
		t.Errorf("SuppressionState = %q, %v; want open", state, ok)
	}
	if _, ok, _ := store.SuppressionState("breaker.opened", "arr", "sonarr"); ok {
		t.Error("sibling service shares suppression record, want independent keys")
	}
	if _, ok, _ := open(t, dir).SuppressionState("breaker.opened", "arr", "radarr"); !ok {
		t.Error("suppression record lost across reopen")
	}

	if err := store.ResetSuppression(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.SuppressionState("breaker.opened", "arr", "radarr"); ok {
		t.Error("SuppressionState survives ResetSuppression, want cleared")
	}
}
