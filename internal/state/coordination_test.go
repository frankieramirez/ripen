package state

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestOpeningCurrentStateDoesNotCompeteWithAnActiveWriter(t *testing.T) {
	dir := t.TempDir()
	writer := open(t, dir)
	if err := writer.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	tx, err := writer.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("UPDATE accepted_digests SET digest=?", newDigest); err != nil {
		t.Fatal(err)
	}

	reader, err := Open(filepath.Join(dir, "state", "updater.db"))
	if err != nil {
		t.Fatalf("opening current state competed with writer: %v", err)
	}
	defer func() { _ = reader.Close() }()
	digest, found, err := reader.AcceptedDigest(exampleApp)

	if err != nil || !found || digest != oldDigest {
		t.Fatalf("read did not preserve committed baseline: digest=%q found=%v err=%v", digest, found, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	digest, found, err = reader.AcceptedDigest(exampleApp)
	if err != nil || !found || digest != newDigest {
		t.Fatalf("writer could not complete: digest=%q found=%v err=%v", digest, found, err)
	}
}

func TestBusyClassificationRecognizesWrappedContentionAndRejectsOtherErrors(t *testing.T) {
	dir := t.TempDir()
	writer := open(t, dir)
	reader := open(t, dir)
	token, acquired, err := reader.AcquireLease(now, 30)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %v %v", acquired, err)
	}
	tx, err := writer.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}

	err = reader.SetSchedulerProgress(token, SchedulerProgress{}, now)

	if !IsBusy(fmt.Errorf("save scheduler progress: %w", err)) {
		t.Fatalf("contention was not classified as busy: %v", err)
	}
	if IsBusy(nil) || IsBusy(ErrLeaseLost) || IsBusy(errors.New("database is locked (5) (SQLITE_BUSY)")) {
		t.Fatal("a non-SQLite error was classified as contention")
	}
}

func TestMigrationPreservesVersionOneStateAndRefusesNewerDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO accepted_digests VALUES('portainer','example-app','',?,?)", oldDigest, stamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, found, err := store.AcceptedDigest(exampleApp)
	if err != nil || !found || digest != oldDigest {
		t.Fatalf("baseline lost: %q %v %v", digest, found, err)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err := store.db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	if unexpected, err := Open(path); err == nil {
		_ = unexpected.Close()
		t.Fatal("newer database accepted")
	}
}

func TestLeaseRenewalCannotReviveExpiredOrSupersededOwnership(t *testing.T) {
	store := open(t, t.TempDir())
	token, ok, err := store.AcquireLease(now, 30)
	if err != nil || !ok {
		t.Fatal(err)
	}

	if err := store.RenewLease(token, now.Add(10*time.Second), 30); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckLease(token, now.Add(35*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(token, now.Add(40*time.Second), 30); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	next, ok, err := store.AcquireLease(now.Add(40*time.Second), 30)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := store.RenewLease(token, now.Add(41*time.Second), 30); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("superseded renewal: %v", err)
	}
	if err := store.CheckLease(next, now.Add(41*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedTransactionSurvivesExpiredLeaseAndBlocksNewDeployment(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.BeginTransaction(token, exampleApp, "run-old", now); err != nil {
		t.Fatal(err)
	}
	next, ok, err := store.AcquireLease(now.Add(time.Minute), 30)
	if err != nil || !ok {
		t.Fatal("monitor could not acquire lease", err)
	}

	if err := store.BeginTransaction(next, radarr, "run-new", now.Add(time.Minute)); err == nil {
		t.Fatal("competing deployment admitted")
	}
	if err := store.FinishTransaction(next); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("new owner erased marker: %v", err)
	}
	if err := store.ClearBreaker("reviewed", now.Add(time.Minute)); err == nil {
		t.Fatal("clear breaker erased interrupted protection")
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker == nil || marker.RunID != "run-old" {
		t.Fatalf("interrupted ownership was lost: marker=%+v err=%v", marker, err)
	}
}

func TestOpenBreakerRefusesDurableTransactionAdmission(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.OpenBreaker("failed", now); err != nil {
		t.Fatal(err)
	}

	if err := store.BeginTransaction(token, exampleApp, "run", now); err == nil {
		t.Fatal("open breaker admitted deployment")
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker != nil {
		t.Fatalf("unexpected marker: %v %v", marker, err)
	}
}

func TestProposalReconciliationCannotUndoConcurrentOperatorClear(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetPendingProposal(exampleApp, newDigest, "https://example.test/1", now); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingProposal(exampleApp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearPendingProposal(exampleApp); err != nil {
		t.Fatal(err)
	}

	changed, err := store.AcceptPendingProposal(token, exampleApp, *pending, now)
	if err != nil || changed {
		t.Fatalf("cleared proposal accepted: %v %v", changed, err)
	}
	_, found, err := store.AcceptedDigest(exampleApp)
	if err != nil || found {
		t.Fatalf("unexpected baseline: %v %v", found, err)
	}
}

func TestGuardedReconciliationWritesRefuseLostOwnership(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	guarded := store.WithLease(token, func() time.Time { return now.Add(time.Minute) })

	if err := guarded.SetAcceptedDigest(exampleApp, newDigest, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("accepted after expiry: %v", err)
	}
	if _, err := guarded.ObserveCandidate(exampleApp, newDigest, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("observed after expiry: %v", err)
	}
	if err := guarded.OpenBreaker("stale", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("breaker written after expiry: %v", err)
	}
	if err := guarded.SetPendingProposal(exampleApp, newDigest, "https://example.test/1", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("proposal written after expiry: %v", err)
	}
}

func TestCheckProgressPersistsRefusalsWithoutChangingCandidateHistory(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if _, err := store.ObserveCandidate(exampleApp, newDigest, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)

	if err := store.StartStackCheck(token, exampleApp, "run", later); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvaluation(token, exampleApp, "run", "ineligible", "running digest changed", later); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishStackCheck(token, exampleApp, "run", "completed", later); err != nil {
		t.Fatal(err)
	}
	checks, err := store.StackChecks()
	if err != nil || len(checks) != 1 || checks[0].CompletedAt == nil {
		t.Fatalf("checks: %v %v", checks, err)
	}
	evaluations, err := store.Evaluations()
	if err != nil || len(evaluations) != 1 || !evaluations[0].EvaluatedAt.Equal(later) {
		t.Fatalf("evaluations: %v %v", evaluations, err)
	}
	candidate, err := store.Candidate(exampleApp)
	if err != nil || !candidate.LastSeen.Equal(now) {
		t.Fatalf("candidate history changed: %v %v", candidate, err)
	}
}

func TestProposalReconciliationAcceptsOnlyTheExactPendingProposal(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetPendingProposal(exampleApp, newDigest, "https://example.test/1", now); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingProposal(exampleApp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveCandidate(exampleApp, newDigest, now); err != nil {
		t.Fatal(err)
	}

	changed, err := store.AcceptPendingProposal(token, exampleApp, *pending, now)

	if err != nil || !changed {
		t.Fatalf("accept failed: %v %v", changed, err)
	}
	digest, found, err := store.AcceptedDigest(exampleApp)
	if err != nil || !found || digest != newDigest {
		t.Fatalf("baseline: %q %v %v", digest, found, err)
	}
	candidate, err := store.Candidate(exampleApp)
	if err != nil || candidate != nil {
		t.Fatalf("candidate: %v %v", candidate, err)
	}
	proposal, err := store.PendingProposal(exampleApp)
	if err != nil || proposal != nil {
		t.Fatalf("proposal: %v %v", proposal, err)
	}
}

func TestSchedulerProgressRoundTripsAndRejectsStaleOwner(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	next := now.Add(time.Minute)
	if err := store.SetSchedulerProgress(token, SchedulerProgress{LastCompletedAt: &now, NextTickAt: &next}, now); err != nil {
		t.Fatal(err)
	}

	p, err := store.Scheduler()

	if err != nil || p.LastCompletedAt == nil || !p.LastCompletedAt.Equal(now) || p.NextTickAt == nil || !p.NextTickAt.Equal(next) {
		t.Fatalf("progress: %+v %v", p, err)
	}
	if err := store.SetSchedulerProgress(token, SchedulerProgress{}, next); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale progress accepted: %v", err)
	}
}

func TestSuccessfulTransactionCompletionAtomicallyAcceptsAndRecordsTheOutcome(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveCandidate(exampleApp, newDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginTransaction(token, exampleApp, "run", now); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{Key: exampleApp, RunID: "run", Actor: "daemon", OldDigest: oldDigest, NewDigest: newDigest, Result: "updated", Detail: "healthy"}

	if err := store.CompleteTransaction(token, attempt, newDigest, "", true, now); err != nil {
		t.Fatal(err)
	}

	digest, _, err := store.AcceptedDigest(exampleApp)
	if err != nil || digest != newDigest {
		t.Fatalf("baseline: %q %v", digest, err)
	}
	candidate, err := store.Candidate(exampleApp)
	if err != nil || candidate != nil {
		t.Fatalf("candidate: %v %v", candidate, err)
	}
	recorded, err := store.LastAttempt(exampleApp)
	if err != nil || recorded == nil || recorded.Result != "updated" {
		t.Fatalf("attempt: %v %v", recorded, err)
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker != nil {
		t.Fatalf("marker: %v %v", marker, err)
	}
}

func TestFailedRollbackCompletionKeepsOwnershipMarkerAndOpensBreaker(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginTransaction(token, exampleApp, "run", now); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{Key: exampleApp, RunID: "run", Actor: "daemon", OldDigest: oldDigest, NewDigest: newDigest, Result: "rollback_failed", Detail: "unhealthy"}

	if err := store.CompleteTransaction(token, attempt, "", "rollback unhealthy", false, now); err != nil {
		t.Fatal(err)
	}

	status, err := store.Status(now)
	if err != nil || !status.BreakerOpen {
		t.Fatalf("breaker: %v %v", status, err)
	}
	digest, _, err := store.AcceptedDigest(exampleApp)
	if err != nil || digest != oldDigest {
		t.Fatalf("baseline: %q %v", digest, err)
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker == nil {
		t.Fatalf("marker: %v %v", marker, err)
	}
	recorded, err := store.LastAttempt(exampleApp)
	if err != nil || recorded == nil || recorded.Result != "rollback_failed" {
		t.Fatalf("attempt: %v %v", recorded, err)
	}
}

func TestLostLeasePreventsEveryTransactionCompletionWrite(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginTransaction(token, exampleApp, "run", now); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{Key: exampleApp, RunID: "run", Actor: "daemon", Result: "updated"}

	if err := store.CompleteTransaction(token, attempt, newDigest, "failure", true, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("completion: %v", err)
	}

	status, err := store.Status(now)
	if err != nil || status.BreakerOpen {
		t.Fatalf("breaker: %v %v", status, err)
	}
	digest, _, err := store.AcceptedDigest(exampleApp)
	if err != nil || digest != oldDigest {
		t.Fatalf("baseline: %q %v", digest, err)
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker == nil {
		t.Fatalf("marker: %v %v", marker, err)
	}
	recorded, err := store.LastAttempt(exampleApp)
	if err != nil || recorded != nil {
		t.Fatalf("attempt: %v %v", recorded, err)
	}
}

func TestHealthyRollbackCompletionClearsMarkerAndPreservesBaseline(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.SetAcceptedDigest(exampleApp, oldDigest, now); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginTransaction(token, exampleApp, "run", now); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{Key: exampleApp, RunID: "run", Actor: "daemon", OldDigest: oldDigest, NewDigest: newDigest, Result: "rolled_back", Detail: "restored"}

	if err := store.CompleteTransaction(token, attempt, "", "health timeout", true, now); err != nil {
		t.Fatal(err)
	}

	status, err := store.Status(now)
	if err != nil || !status.BreakerOpen {
		t.Fatalf("breaker: %v %v", status, err)
	}
	digest, _, err := store.AcceptedDigest(exampleApp)
	if err != nil || digest != oldDigest {
		t.Fatalf("baseline: %q %v", digest, err)
	}
	marker, err := store.ActiveTransaction()
	if err != nil || marker != nil {
		t.Fatalf("marker: %v %v", marker, err)
	}
	recorded, err := store.LastAttempt(exampleApp)
	if err != nil || recorded == nil || recorded.Result != "rolled_back" {
		t.Fatalf("attempt: %v %v", recorded, err)
	}
}

func TestStackCheckRetainsItsOwnerWhenAnotherLeaseIsAcquired(t *testing.T) {
	store := open(t, t.TempDir())
	token, _, _ := store.AcquireLease(now, 30)
	if err := store.StartStackCheck(token, exampleApp, "old-run", now); err != nil {
		t.Fatal(err)
	}

	next, ok, err := store.AcquireLease(now.Add(time.Minute), 30)

	if err != nil || !ok || next == token {
		t.Fatalf("lease: %v %v", ok, err)
	}
	checks, err := store.StackChecks()
	if err != nil || len(checks) != 1 || checks[0].OwnerToken != token {
		t.Fatalf("checks: %v %v", checks, err)
	}
}
