package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
)

const coordinationSchema = `
CREATE TABLE IF NOT EXISTS active_transaction (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), owner_token TEXT NOT NULL,
 backend TEXT NOT NULL, stack TEXT NOT NULL, service TEXT NOT NULL, run_id TEXT NOT NULL,
 phase TEXT NOT NULL, started_at TEXT NOT NULL, phase_started_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stack_checks (
 backend TEXT NOT NULL, stack TEXT NOT NULL, run_id TEXT NOT NULL,
 started_at TEXT NOT NULL, completed_at TEXT, outcome TEXT NOT NULL, owner_token TEXT NOT NULL,
 PRIMARY KEY(backend,stack)
);
CREATE TABLE IF NOT EXISTS evaluations (
 backend TEXT NOT NULL, stack TEXT NOT NULL, service TEXT NOT NULL,
 run_id TEXT NOT NULL, result TEXT NOT NULL, detail TEXT NOT NULL, evaluated_at TEXT NOT NULL,
 PRIMARY KEY(backend,stack,service)
);
CREATE TABLE IF NOT EXISTS scheduler_progress (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), last_completed_at TEXT, next_tick_at TEXT
);
`

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > domain.StateSchemaVersion {
		return fmt.Errorf("state schema version %d is newer than supported version %d", version, domain.StateSchemaVersion)
	}
	if version == domain.StateSchemaVersion {
		return tx.Commit()
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	if _, err := tx.Exec(coordinationSchema); err != nil {
		return err
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrLeaseLost indicates missing, expired, or superseded ownership.
var ErrLeaseLost = errors.New("the run lease is no longer owned")

func checkLease(tx *sql.Tx, token string, now time.Time) error {
	var expires string
	if err := tx.QueryRow("SELECT expires_at FROM lease WHERE singleton=1 AND owner_token=?", token).Scan(&expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	expiry, err := parseStamp(expires)
	if err != nil {
		return err
	}
	if !expiry.After(now) {
		return ErrLeaseLost
	}
	return nil
}

// CheckLease verifies that the token still owns an unexpired lease.
func (s *Store) CheckLease(token string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return checkLease(tx, token, now)
}

// RenewLease extends an unexpired lease only for its current owner.
func (s *Store) RenewLease(token string, now time.Time, ttlSeconds int) error {
	if ttlSeconds <= 0 {
		return errors.New("lease ttl must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE lease SET expires_at=? WHERE singleton=1 AND owner_token=?", stamp(now.Add(time.Duration(ttlSeconds)*time.Second)), token); err != nil {
		return err
	}
	return tx.Commit()
}

// TransactionProgress is durable ownership of an unfinished Transaction.
type TransactionProgress struct {
	OwnerToken     string
	Key            Key
	RunID          string
	Phase          string
	StartedAt      time.Time
	PhaseStartedAt time.Time
}

// BeginTransaction reserves deployment ownership while the breaker is closed.
func (s *Store) BeginTransaction(token string, key Key, runID string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return err
	}
	var blocked int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM breaker WHERE is_open=1) OR EXISTS(SELECT 1 FROM active_transaction)").Scan(&blocked); err != nil {
		return err
	}
	if blocked != 0 {
		return errors.New("deployment refused: breaker open or an unfinished transaction requires reconciliation")
	}
	if _, err := tx.Exec("INSERT INTO active_transaction VALUES(1,?,?,?,?,?,?,?,?)", token, key.Backend, key.Stack, key.Service, runID, "deploying", stamp(now), stamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// SetTransactionPhase persists the owner's current Transaction phase.
func (s *Store) SetTransactionPhase(token, phase string, now time.Time) error {
	r, err := s.db.Exec("UPDATE active_transaction SET phase=?,phase_started_at=? WHERE singleton=1 AND owner_token=?", phase, stamp(now), token)
	return requireOwner(r, err)
}

func requireOwner(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}

// FinishTransaction removes only the matching owner's completed Transaction.
func (s *Store) FinishTransaction(token string) error {
	r, err := s.db.Exec("DELETE FROM active_transaction WHERE singleton=1 AND owner_token=?", token)
	return requireOwner(r, err)
}

// ActiveTransaction returns the unfinished Transaction, even after lease expiry.
func (s *Store) ActiveTransaction() (*TransactionProgress, error) {
	var p TransactionProgress
	var start, phase string
	err := s.db.QueryRow("SELECT owner_token,backend,stack,service,run_id,phase,started_at,phase_started_at FROM active_transaction WHERE singleton=1").Scan(&p.OwnerToken, &p.Key.Backend, &p.Key.Stack, &p.Key.Service, &p.RunID, &p.Phase, &start, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.StartedAt, err = parseStamp(start)
	if err != nil {
		return nil, err
	}
	p.PhaseStartedAt, err = parseStamp(phase)
	return &p, err
}

// AcceptPendingProposal accepts only the exact Proposal that was evaluated.
func (s *Store) AcceptPendingProposal(token string, key Key, expected PendingProposal, now time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return false, err
	}
	r, err := tx.Exec("DELETE FROM pending_proposals WHERE backend=? AND stack=? AND service=? AND digest=? AND url=? AND proposed_at=?", key.Backend, key.Stack, key.Service, expected.Digest, expected.URL, stamp(expected.ProposedAt))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO accepted_digests VALUES(?,?,?,?,?) ON CONFLICT(backend,stack,service) DO UPDATE SET digest=excluded.digest,accepted_at=excluded.accepted_at`, key.Backend, key.Stack, key.Service, expected.Digest, stamp(now)); err != nil {
		return false, err
	}
	if _, err := tx.Exec("DELETE FROM candidates WHERE backend=? AND stack=? AND service=?", key.Backend, key.Stack, key.Service); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// StackCheck records the most recent observation attempt for a stack.
type StackCheck struct {
	OwnerToken  string
	Key         Key
	RunID       string
	StartedAt   time.Time
	CompletedAt *time.Time
	Outcome     string
}

// Evaluation records an outcome independently of Candidate history.
type Evaluation struct {
	Key         Key
	RunID       string
	Result      domain.ResultCode
	Detail      string
	EvaluatedAt time.Time
}

// SchedulerProgress records scheduled and completed observation times.
type SchedulerProgress struct {
	LastCompletedAt *time.Time
	NextTickAt      *time.Time
}

func (s *Store) ownedWrite(token string, now time.Time, query string, args ...any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return err
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// StartStackCheck persists an observation start before work is dispatched.
func (s *Store) StartStackCheck(token string, key Key, runID string, now time.Time) error {
	return s.ownedWrite(token, now, `INSERT INTO stack_checks VALUES(?,?,?,?,NULL,'running',?) ON CONFLICT(backend,stack) DO UPDATE SET run_id=excluded.run_id,started_at=excluded.started_at,completed_at=NULL,outcome='running',owner_token=excluded.owner_token`, key.Backend, key.Stack, runID, stamp(now), token)
}

// FinishStackCheck persists an observation's terminal outcome.
func (s *Store) FinishStackCheck(token string, key Key, runID, outcome string, now time.Time) error {
	return s.ownedWrite(token, now, "UPDATE stack_checks SET completed_at=?,outcome=? WHERE backend=? AND stack=? AND run_id=?", stamp(now), outcome, key.Backend, key.Stack, runID)
}

// RecordEvaluation persists the latest service check, including refusals.
func (s *Store) RecordEvaluation(token string, key Key, runID string, result domain.ResultCode, detail string, now time.Time) error {
	return s.ownedWrite(token, now, `INSERT INTO evaluations VALUES(?,?,?,?,?,?,?) ON CONFLICT(backend,stack,service) DO UPDATE SET run_id=excluded.run_id,result=excluded.result,detail=excluded.detail,evaluated_at=excluded.evaluated_at`, key.Backend, key.Stack, key.Service, runID, result, detail, stamp(now))
}

// SetSchedulerProgress persists the next tick and last completed observation.
func (s *Store) SetSchedulerProgress(token string, p SchedulerProgress, now time.Time) error {
	return s.ownedWrite(token, now, `INSERT INTO scheduler_progress VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET last_completed_at=excluded.last_completed_at,next_tick_at=excluded.next_tick_at`, optionalStamp(p.LastCompletedAt), optionalStamp(p.NextTickAt))
}
func optionalStamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
func optionalTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseStamp(v.String)
	return &t, err
}

// Scheduler returns durable scheduling timestamps.
func (s *Store) Scheduler() (SchedulerProgress, error) {
	var p SchedulerProgress
	var completed, next sql.NullString
	err := s.db.QueryRow("SELECT last_completed_at,next_tick_at FROM scheduler_progress WHERE singleton=1").Scan(&completed, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.LastCompletedAt, err = optionalTime(completed)
	if err != nil {
		return p, err
	}
	p.NextTickAt, err = optionalTime(next)
	return p, err
}

// StackChecks returns latest stack observation progress.
func (s *Store) StackChecks() ([]StackCheck, error) {
	rows, err := s.db.Query("SELECT backend,stack,run_id,started_at,completed_at,outcome,owner_token FROM stack_checks ORDER BY backend,stack")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var checks []StackCheck
	for rows.Next() {
		var p StackCheck
		var started string
		var completed sql.NullString
		if err := rows.Scan(&p.Key.Backend, &p.Key.Stack, &p.RunID, &started, &completed, &p.Outcome, &p.OwnerToken); err != nil {
			return nil, err
		}
		p.StartedAt, err = parseStamp(started)
		if err != nil {
			return nil, err
		}
		p.CompletedAt, err = optionalTime(completed)
		if err != nil {
			return nil, err
		}
		checks = append(checks, p)
	}
	return checks, rows.Err()
}

// Evaluations returns latest service evaluations.
func (s *Store) Evaluations() ([]Evaluation, error) {
	rows, err := s.db.Query("SELECT backend,stack,service,run_id,result,detail,evaluated_at FROM evaluations ORDER BY backend,stack,service")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var evaluations []Evaluation
	for rows.Next() {
		var p Evaluation
		var at string
		if err := rows.Scan(&p.Key.Backend, &p.Key.Stack, &p.Key.Service, &p.RunID, &p.Result, &p.Detail, &at); err != nil {
			return nil, err
		}
		p.EvaluatedAt, err = parseStamp(at)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, p)
	}
	return evaluations, rows.Err()
}

// WithLease returns a store whose reconciliation writes require current lease ownership.
func (s *Store) WithLease(token string, clock func() time.Time) *Store {
	return &Store{db: s.db, token: token, clock: clock}
}
func (s *Store) checkMutation(tx *sql.Tx) error {
	if s.token == "" {
		return nil
	}
	return checkLease(tx, s.token, s.clock())
}
func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	if s.token == "" {
		return s.db.Exec(query, args...)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkMutation(tx); err != nil {
		return nil, err
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// CompleteTransaction atomically records the owner's outcome and any terminal state changes.
func (s *Store) CompleteTransaction(token string, attempt Attempt, acceptedDigest, breakerReason string, clearMarker bool, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return err
	}
	var matches int
	if err := tx.QueryRow("SELECT COUNT(*) FROM active_transaction WHERE singleton=1 AND owner_token=? AND run_id=? AND backend=? AND stack=? AND service=?", token, attempt.RunID, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service).Scan(&matches); err != nil {
		return err
	}
	if matches != 1 {
		return ErrLeaseLost
	}
	if acceptedDigest != "" {
		if _, err := tx.Exec(`INSERT INTO accepted_digests VALUES(?,?,?,?,?) ON CONFLICT(backend,stack,service) DO UPDATE SET digest=excluded.digest,accepted_at=excluded.accepted_at`, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service, acceptedDigest, stamp(now)); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM candidates WHERE backend=? AND stack=? AND service=?", attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM pending_proposals WHERE backend=? AND stack=? AND service=?", attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service); err != nil {
			return err
		}
	}
	if breakerReason != "" {
		if _, err := tx.Exec(`INSERT INTO breaker VALUES(1,1,?,?,NULL) ON CONFLICT(singleton) DO UPDATE SET is_open=1,reason=excluded.reason,changed_at=excluded.changed_at,clear_reason=NULL`, breakerReason, stamp(now)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO attempts(run_id,actor,backend,stack,service,old_digest,new_digest,result,attempted_at,detail) VALUES(?,?,?,?,?,?,?,?,?,?)`, attempt.RunID, attempt.Actor, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service, attempt.OldDigest, attempt.NewDigest, attempt.Result, stamp(now), attempt.Detail); err != nil {
		return err
	}
	if clearMarker {
		if _, err := tx.Exec("DELETE FROM active_transaction WHERE singleton=1 AND owner_token=?", token); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReconcileTransaction records an explicitly verified interrupted Transaction and clears its breaker atomically.
func (s *Store) ReconcileTransaction(token string, attempt Attempt, reason string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkLease(tx, token, now); err != nil {
		return err
	}
	result, err := tx.Exec("DELETE FROM active_transaction WHERE singleton=1 AND run_id=? AND backend=? AND stack=? AND service=? AND phase!='proposing'", attempt.RunID, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service)
	if err := requireOwner(result, err); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO attempts(run_id,actor,backend,stack,service,old_digest,new_digest,result,attempted_at,detail) VALUES(?,?,?,?,?,?,?,?,?,?)`, attempt.RunID, attempt.Actor, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service, attempt.OldDigest, attempt.NewDigest, attempt.Result, stamp(now), attempt.Detail); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO breaker VALUES(1,0,NULL,?,?) ON CONFLICT(singleton) DO UPDATE SET is_open=0,reason=NULL,changed_at=excluded.changed_at,clear_reason=excluded.clear_reason`, stamp(now), reason); err != nil {
		return err
	}
	return tx.Commit()
}
