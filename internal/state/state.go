// Package state is the SQLite state store — schema v1. The store is the
// system of record: every paging Event corresponds to a durable state
// change written here first. There is no migration path from the Python
// schema; existing deployments start cold and re-baseline.
package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0 builds working

	"github.com/frankieramirez/ripen/internal/domain"
)

// Key identifies one Service's state: backend, stack, and service, with
// service empty for a stack-level policy. The Python schema's state_key
// column pretended this was one string; schema v1 stores the three parts.
type Key struct {
	Backend domain.Backend
	Stack   string
	Service string
}

// CandidateObservation is one observed Candidate digest and its history.
type CandidateObservation struct {
	Digest    string
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

// PendingProposal is an open Proposal (PR) awaiting review.
type PendingProposal struct {
	Digest     string
	URL        string
	ProposedAt time.Time
}

// Attempt is one recorded Transaction attempt.
type Attempt struct {
	// ID is the audit trail's own order. It is the cursor `ripen audit`
	// pages by, and it never appears in the JSON surface.
	ID          int64
	Key         Key
	RunID       string
	Actor       domain.Actor
	OldDigest   string
	NewDigest   string
	Result      domain.ResultCode
	Detail      string
	AttemptedAt time.Time
}

// CandidateRecord is one observed Candidate, with its Key.
type CandidateRecord struct {
	Key       Key
	Digest    string
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

// AuditFilter narrows and pages the audit trail. Every field is
// optional; an empty filter reads the newest attempts.
type AuditFilter struct {
	Limit   int
	Cursor  int64
	RunID   string
	Backend domain.Backend
	Stack   string
	Service string
	Result  domain.ResultCode
}

// AcceptedDigest pairs a Key with its accepted Baseline digest.
type AcceptedDigest struct {
	Key    Key
	Digest string
}

// ProposalRecord pairs a Key with its pending Proposal for Status.
type ProposalRecord struct {
	Key    Key
	Digest string
	URL    string
}

// Status is the durable state snapshot behind `ripen status`.
type Status struct {
	BreakerOpen      bool
	BreakerReason    string
	AcceptedDigests  []AcceptedDigest
	LeaseActive      bool
	PendingProposals []ProposalRecord
}

// NotifierHealth is the persisted Notifier delivery health.
type NotifierHealth struct {
	LastSuccessAt       *time.Time
	ConsecutiveFailures int
}

// Store is the SQLite-backed state store.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS accepted_digests (
    backend TEXT NOT NULL,
    stack TEXT NOT NULL,
    service TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    PRIMARY KEY (backend, stack, service)
);
CREATE TABLE IF NOT EXISTS candidates (
    backend TEXT NOT NULL,
    stack TEXT NOT NULL,
    service TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    observation_count INTEGER NOT NULL,
    PRIMARY KEY (backend, stack, service, digest)
);
CREATE TABLE IF NOT EXISTS pending_proposals (
    backend TEXT NOT NULL,
    stack TEXT NOT NULL,
    service TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    url TEXT NOT NULL,
    proposed_at TEXT NOT NULL,
    PRIMARY KEY (backend, stack, service)
);
CREATE TABLE IF NOT EXISTS attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    actor TEXT NOT NULL,
    backend TEXT NOT NULL,
    stack TEXT NOT NULL,
    service TEXT NOT NULL DEFAULT '',
    old_digest TEXT NOT NULL,
    new_digest TEXT NOT NULL,
    result TEXT NOT NULL,
    attempted_at TEXT NOT NULL,
    detail TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS breaker (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    is_open INTEGER NOT NULL,
    reason TEXT,
    changed_at TEXT NOT NULL,
    clear_reason TEXT
);
CREATE TABLE IF NOT EXISTS lease (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    acquired_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    owner_token TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS notifier_health (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_success_at TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS notifier_destination (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    fingerprint TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_suppression (
    event TEXT NOT NULL,
    stack TEXT NOT NULL,
    service TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    notified_at TEXT NOT NULL,
    PRIMARY KEY (event, stack, service)
);
`

// Open opens (creating if needed) the state database at path, creating
// the parent directory when missing.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// AcceptedDigest returns the accepted Baseline digest for a Key.
func (s *Store) AcceptedDigest(key Key) (string, bool, error) {
	var digest string
	err := s.db.QueryRow(
		"SELECT digest FROM accepted_digests WHERE backend = ? AND stack = ? AND service = ?",
		key.Backend, key.Stack, key.Service).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return digest, true, nil
}

// SetAcceptedDigest records a digest as the accepted Baseline and clears
// the Key's Candidates and pending Proposal: accepting is the terminal
// state of both.
func (s *Store) SetAcceptedDigest(key Key, digest string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		INSERT INTO accepted_digests(backend, stack, service, digest, accepted_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(backend, stack, service) DO UPDATE SET
			digest=excluded.digest, accepted_at=excluded.accepted_at`,
		key.Backend, key.Stack, key.Service, digest, stamp(now)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"DELETE FROM candidates WHERE backend = ? AND stack = ? AND service = ?",
		key.Backend, key.Stack, key.Service); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"DELETE FROM pending_proposals WHERE backend = ? AND stack = ? AND service = ?",
		key.Backend, key.Stack, key.Service); err != nil {
		return err
	}
	return tx.Commit()
}

// ObserveCandidate records one observation of a Candidate digest. A new
// digest replaces any previous Candidate for the Key; re-observing the
// same digest increments its count, preserving first_seen.
func (s *Store) ObserveCandidate(key Key, digest string, now time.Time) (CandidateObservation, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return CandidateObservation{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var firstSeen string
	var count int
	err = tx.QueryRow(`
		SELECT first_seen, observation_count FROM candidates
		WHERE backend = ? AND stack = ? AND service = ? AND digest = ?`,
		key.Backend, key.Stack, key.Service, digest).Scan(&firstSeen, &count)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			"DELETE FROM candidates WHERE backend = ? AND stack = ? AND service = ?",
			key.Backend, key.Stack, key.Service); err != nil {
			return CandidateObservation{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO candidates(backend, stack, service, digest, first_seen, last_seen, observation_count)
			VALUES(?, ?, ?, ?, ?, ?, 1)`,
			key.Backend, key.Stack, key.Service, digest, stamp(now), stamp(now)); err != nil {
			return CandidateObservation{}, err
		}
		if err := tx.Commit(); err != nil {
			return CandidateObservation{}, err
		}
		return CandidateObservation{Digest: digest, FirstSeen: now, LastSeen: now, Count: 1}, nil
	case err != nil:
		return CandidateObservation{}, err
	}

	count++
	if _, err := tx.Exec(`
		UPDATE candidates SET last_seen = ?, observation_count = ?
		WHERE backend = ? AND stack = ? AND service = ? AND digest = ?`,
		stamp(now), count, key.Backend, key.Stack, key.Service, digest); err != nil {
		return CandidateObservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return CandidateObservation{}, err
	}
	first, err := parseStamp(firstSeen)
	if err != nil {
		return CandidateObservation{}, err
	}
	return CandidateObservation{Digest: digest, FirstSeen: first, LastSeen: now, Count: count}, nil
}

// PendingProposal returns the Key's open Proposal, or nil.
func (s *Store) PendingProposal(key Key) (*PendingProposal, error) {
	var digest, proposalURL, proposedAt string
	err := s.db.QueryRow(
		"SELECT digest, url, proposed_at FROM pending_proposals WHERE backend = ? AND stack = ? AND service = ?",
		key.Backend, key.Stack, key.Service).Scan(&digest, &proposalURL, &proposedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at, err := parseStamp(proposedAt)
	if err != nil {
		return nil, err
	}
	return &PendingProposal{Digest: digest, URL: proposalURL, ProposedAt: at}, nil
}

// SetPendingProposal records an open Proposal for a Key.
func (s *Store) SetPendingProposal(key Key, digest, proposalURL string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO pending_proposals(backend, stack, service, digest, url, proposed_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(backend, stack, service) DO UPDATE SET
			digest=excluded.digest, url=excluded.url, proposed_at=excluded.proposed_at`,
		key.Backend, key.Stack, key.Service, digest, proposalURL, stamp(now))
	return err
}

// ClearPendingProposal removes a Key's Proposal record, reporting whether
// one existed.
func (s *Store) ClearPendingProposal(key Key) (bool, error) {
	result, err := s.db.Exec(
		"DELETE FROM pending_proposals WHERE backend = ? AND stack = ? AND service = ?",
		key.Backend, key.Stack, key.Service)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// RecordAttempt appends one Transaction attempt to the audit trail.
func (s *Store) RecordAttempt(attempt Attempt, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO attempts(run_id, actor, backend, stack, service, old_digest, new_digest, result, attempted_at, detail)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.RunID, attempt.Actor, attempt.Key.Backend, attempt.Key.Stack, attempt.Key.Service,
		attempt.OldDigest, attempt.NewDigest, attempt.Result, stamp(now), attempt.Detail)
	return err
}

// Attempts returns the newest attempts, most recent first.
func (s *Store) Attempts(limit int) ([]Attempt, error) {
	return s.AuditPage(AuditFilter{Limit: limit})
}

// AuditPage reads the audit trail newest first, narrowed by the filter
// and paged by attempt id. The audit trail is the attempts table — never
// the Event stream, which is a notification channel and not a record.
func (s *Store) AuditPage(filter AuditFilter) ([]Attempt, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, run_id, actor, backend, stack, service, old_digest, new_digest,
		       result, attempted_at, detail
		FROM attempts WHERE 1 = 1`)
	var arguments []any
	for _, condition := range []struct{ column, value string }{
		{"run_id", filter.RunID},
		{"backend", string(filter.Backend)},
		{"stack", filter.Stack},
		{"service", filter.Service},
		{"result", string(filter.Result)},
	} {
		if condition.value != "" {
			query.WriteString(" AND " + condition.column + " = ?")
			arguments = append(arguments, condition.value)
		}
	}
	if filter.Cursor > 0 {
		query.WriteString(" AND id < ?")
		arguments = append(arguments, filter.Cursor)
	}
	query.WriteString(" ORDER BY id DESC LIMIT ?")
	arguments = append(arguments, filter.Limit)

	rows, err := s.db.Query(query.String(), arguments...) // #nosec G202 -- column names are literals above, values are bound
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var attempts []Attempt
	for rows.Next() {
		var attempt Attempt
		var backend, actor, result, attemptedAt string
		if err := rows.Scan(&attempt.ID, &attempt.RunID, &actor, &backend, &attempt.Key.Stack,
			&attempt.Key.Service, &attempt.OldDigest, &attempt.NewDigest, &result,
			&attemptedAt, &attempt.Detail); err != nil {
			return nil, err
		}
		attempt.Actor = domain.Actor(actor)
		attempt.Key.Backend = domain.Backend(backend)
		attempt.Result = domain.ResultCode(result)
		if attempt.AttemptedAt, err = parseStamp(attemptedAt); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

// Candidates returns every observed Candidate, oldest Key first.
func (s *Store) Candidates() ([]CandidateRecord, error) {
	rows, err := s.db.Query(`
		SELECT backend, stack, service, digest, first_seen, last_seen, observation_count
		FROM candidates ORDER BY backend, stack, service`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []CandidateRecord
	for rows.Next() {
		var record CandidateRecord
		var backend, firstSeen, lastSeen string
		if err := rows.Scan(&backend, &record.Key.Stack, &record.Key.Service, &record.Digest,
			&firstSeen, &lastSeen, &record.Count); err != nil {
			return nil, err
		}
		record.Key.Backend = domain.Backend(backend)
		if record.FirstSeen, err = parseStamp(firstSeen); err != nil {
			return nil, err
		}
		if record.LastSeen, err = parseStamp(lastSeen); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// Candidate returns one Key's observed Candidate, or nil.
func (s *Store) Candidate(key Key) (*CandidateRecord, error) {
	record := CandidateRecord{Key: key}
	var firstSeen, lastSeen string
	err := s.db.QueryRow(`
		SELECT digest, first_seen, last_seen, observation_count FROM candidates
		WHERE backend = ? AND stack = ? AND service = ?`,
		key.Backend, key.Stack, key.Service).Scan(&record.Digest, &firstSeen, &lastSeen, &record.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.FirstSeen, err = parseStamp(firstSeen); err != nil {
		return nil, err
	}
	if record.LastSeen, err = parseStamp(lastSeen); err != nil {
		return nil, err
	}
	return &record, nil
}

// LastAttempt returns the newest audit entry for one Key, or nil. The
// Key is matched exactly, empty service included, which is why this does
// not go through the audit filter.
func (s *Store) LastAttempt(key Key) (*Attempt, error) {
	var attempt Attempt
	var backend, actor, result, attemptedAt string
	err := s.db.QueryRow(`
		SELECT id, run_id, actor, backend, stack, service, old_digest, new_digest,
		       result, attempted_at, detail
		FROM attempts WHERE backend = ? AND stack = ? AND service = ?
		ORDER BY id DESC LIMIT 1`, key.Backend, key.Stack, key.Service).
		Scan(&attempt.ID, &attempt.RunID, &actor, &backend, &attempt.Key.Stack,
			&attempt.Key.Service, &attempt.OldDigest, &attempt.NewDigest, &result,
			&attemptedAt, &attempt.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	attempt.Actor = domain.Actor(actor)
	attempt.Key.Backend = domain.Backend(backend)
	attempt.Result = domain.ResultCode(result)
	if attempt.AttemptedAt, err = parseStamp(attemptedAt); err != nil {
		return nil, err
	}
	return &attempt, nil
}

// AcquireLease takes the run lease when no unexpired lease exists,
// returning an owner token. BEGIN IMMEDIATE serializes contenders.
func (s *Store) AcquireLease(now time.Time, ttlSeconds int) (string, bool, error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", false, err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	var expiresAt string
	err = conn.QueryRowContext(ctx, "SELECT expires_at FROM lease WHERE singleton = 1").Scan(&expiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if err == nil {
		expires, err := parseStamp(expiresAt)
		if err != nil {
			return "", false, err
		}
		if expires.After(now) {
			return "", false, nil
		}
	}

	token, err := newToken()
	if err != nil {
		return "", false, err
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM lease WHERE singleton = 1"); err != nil {
		return "", false, err
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO lease(singleton, acquired_at, expires_at, owner_token) VALUES(1, ?, ?, ?)",
		stamp(now), stamp(now.Add(time.Duration(ttlSeconds)*time.Second)), token); err != nil {
		return "", false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", false, err
	}
	commit = true
	return token, true, nil
}

// ReleaseLease releases the lease if the token still owns it; releasing a
// superseded token is a no-op.
func (s *Store) ReleaseLease(token string) error {
	_, err := s.db.Exec("DELETE FROM lease WHERE singleton = 1 AND owner_token = ?", token)
	return err
}

// OpenBreaker opens the Circuit breaker with a reason.
func (s *Store) OpenBreaker(reason string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO breaker(singleton, is_open, reason, changed_at, clear_reason)
		VALUES(1, 1, ?, ?, NULL)
		ON CONFLICT(singleton) DO UPDATE SET
			is_open=1, reason=excluded.reason, changed_at=excluded.changed_at, clear_reason=NULL`,
		reason, stamp(now))
	return err
}

// ClearBreaker closes the Circuit breaker; the operator's reason is
// mandatory and recorded.
func (s *Store) ClearBreaker(reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("a clear-breaker reason is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO breaker(singleton, is_open, reason, changed_at, clear_reason)
		VALUES(1, 0, NULL, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			is_open=0, reason=NULL, changed_at=excluded.changed_at, clear_reason=excluded.clear_reason`,
		stamp(now), reason)
	return err
}

// Status reads the durable state snapshot.
func (s *Store) Status(now time.Time) (Status, error) {
	var status Status

	var isOpen int
	var reason sql.NullString
	err := s.db.QueryRow("SELECT is_open, reason FROM breaker WHERE singleton = 1").Scan(&isOpen, &reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	if err == nil && isOpen == 1 {
		status.BreakerOpen = true
		status.BreakerReason = reason.String
	}

	rows, err := s.db.Query(
		"SELECT backend, stack, service, digest FROM accepted_digests ORDER BY backend, stack, service")
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record AcceptedDigest
		var backend string
		if err := rows.Scan(&backend, &record.Key.Stack, &record.Key.Service, &record.Digest); err != nil {
			return Status{}, err
		}
		record.Key.Backend = domain.Backend(backend)
		status.AcceptedDigests = append(status.AcceptedDigests, record)
	}
	if err := rows.Err(); err != nil {
		return Status{}, err
	}

	var expiresAt string
	err = s.db.QueryRow("SELECT expires_at FROM lease WHERE singleton = 1").Scan(&expiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	if err == nil {
		expires, err := parseStamp(expiresAt)
		if err != nil {
			return Status{}, err
		}
		status.LeaseActive = expires.After(now)
	}

	proposals, err := s.db.Query(
		"SELECT backend, stack, service, digest, url FROM pending_proposals ORDER BY backend, stack, service")
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = proposals.Close() }()
	for proposals.Next() {
		var record ProposalRecord
		var backend string
		if err := proposals.Scan(&backend, &record.Key.Stack, &record.Key.Service, &record.Digest, &record.URL); err != nil {
			return Status{}, err
		}
		record.Key.Backend = domain.Backend(backend)
		status.PendingProposals = append(status.PendingProposals, record)
	}
	return status, proposals.Err()
}

// NotifierHealth reads the persisted Notifier delivery health.
func (s *Store) NotifierHealth() (NotifierHealth, error) {
	var lastSuccess sql.NullString
	var failures int
	err := s.db.QueryRow(
		"SELECT last_success_at, consecutive_failures FROM notifier_health WHERE singleton = 1").
		Scan(&lastSuccess, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return NotifierHealth{}, nil
	}
	if err != nil {
		return NotifierHealth{}, err
	}
	health := NotifierHealth{ConsecutiveFailures: failures}
	if lastSuccess.Valid {
		at, err := parseStamp(lastSuccess.String)
		if err != nil {
			return NotifierHealth{}, err
		}
		health.LastSuccessAt = &at
	}
	return health, nil
}

// RecordNotifierSuccess records a successful delivery, resetting the
// failure streak.
func (s *Store) RecordNotifierSuccess(now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO notifier_health(singleton, last_success_at, consecutive_failures)
		VALUES(1, ?, 0)
		ON CONFLICT(singleton) DO UPDATE SET
			last_success_at=excluded.last_success_at, consecutive_failures=0`,
		stamp(now))
	return err
}

// RecordNotifierFailure increments the persisted failure streak.
func (s *Store) RecordNotifierFailure() error {
	_, err := s.db.Exec(`
		INSERT INTO notifier_health(singleton, last_success_at, consecutive_failures)
		VALUES(1, NULL, 1)
		ON CONFLICT(singleton) DO UPDATE SET
			consecutive_failures = consecutive_failures + 1`)
	return err
}

// NotifierDestination reads the fingerprint of the destination the
// suppression table was built against.
func (s *Store) NotifierDestination() (string, error) {
	var fingerprint string
	err := s.db.QueryRow("SELECT fingerprint FROM notifier_destination WHERE singleton = 1").
		Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fingerprint, err
}

// SetNotifierDestination records which destination the suppression table
// belongs to.
func (s *Store) SetNotifierDestination(fingerprint string) error {
	_, err := s.db.Exec(`
		INSERT INTO notifier_destination(singleton, fingerprint) VALUES(1, ?)
		ON CONFLICT(singleton) DO UPDATE SET fingerprint = excluded.fingerprint`, fingerprint)
	return err
}

// ClearSuppression drops one suppression key, re-arming it. Recovery
// uses this: a stack that failed, recovered, and fails again must page
// the second time.
func (s *Store) ClearSuppression(event, stack, service string) error {
	_, err := s.db.Exec(
		"DELETE FROM notification_suppression WHERE event = ? AND stack = ? AND service = ?",
		event, stack, service)
	return err
}

// SuppressionState reads the last notified state for a suppression key.
func (s *Store) SuppressionState(event, stack, service string) (string, bool, error) {
	var state string
	err := s.db.QueryRow(
		"SELECT state FROM notification_suppression WHERE event = ? AND stack = ? AND service = ?",
		event, stack, service).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return state, true, nil
}

// SetSuppressionState records the state a notification was last sent for.
func (s *Store) SetSuppressionState(event, stack, service, state string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO notification_suppression(event, stack, service, state, notified_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(event, stack, service) DO UPDATE SET
			state=excluded.state, notified_at=excluded.notified_at`,
		event, stack, service, state, stamp(now))
	return err
}

// ResetSuppression clears every suppression record — used when the
// webhook destination changes, so the new destination pages current
// state once (which is correct).
func (s *Store) ResetSuppression() error {
	_, err := s.db.Exec("DELETE FROM notification_suppression")
	return err
}

func stamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseStamp(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
