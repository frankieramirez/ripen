package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/app"
	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
)

type progressClock struct{ now time.Time }

func (c *progressClock) Now() time.Time        { return c.now }
func (c *progressClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func TestStatusSeparatesEvaluationTimeFromCandidateHistoryAndMarksInterruptedWork(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	clock := &progressClock{now: time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC)}
	token, acquired, err := store.AcquireLease(clock.now, 60)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %v", acquired, err)
	}
	key := state.Key{Backend: domain.BackendPortainer, Stack: "comicarr"}
	for _, err := range []error{
		store.StartStackCheck(token, key, "observation", clock.now),
		store.RecordEvaluation(token, key, "observation", domain.ResultIneligible, "image cannot be proven", clock.now),
		store.BeginTransaction(token, key, "apply", clock.now),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	next := clock.now.Add(time.Minute)
	if err := store.SetSchedulerProgress(token, state.SchedulerProgress{NextTickAt: &next}, clock.now); err != nil {
		t.Fatal(err)
	}
	application := &app.App{Store: store, Clock: clock, Policy: &config.Policy{ObservationConcurrency: 2}}

	status, err := application.Status()

	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveTransaction == nil || status.ActiveTransaction.Interrupted {
		t.Fatalf("transaction: %+v", status.ActiveTransaction)
	}
	if len(status.Evaluations) != 1 || status.Evaluations[0].Result != string(domain.ResultIneligible) {
		t.Fatalf("evaluations: %+v", status.Evaluations)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("status exposes ownership token")
	}
	clock.now = next.Add(time.Second)
	status, err = application.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.ActiveTransaction.Interrupted || !status.Scheduler.Stale || status.Checks[0].Outcome != "interrupted" {
		t.Fatalf("status does not expose interruption: %+v", status)
	}
	if _, acquired, err := store.AcquireLease(clock.now, 60); err != nil || !acquired {
		t.Fatalf("replacement lease: %v %v", acquired, err)
	}
	status, err = application.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Lease.Active || !status.ActiveTransaction.Interrupted || status.Checks[0].Outcome != "interrupted" {
		t.Fatalf("replacement lease revived old work: %+v", status)
	}
}

func TestPrettyProgressDerivesElapsedFromTheResponseTime(t *testing.T) {
	now := time.Date(2026, 9, 8, 5, 1, 0, 0, time.UTC)
	status := response.Status{ActiveTransaction: &response.TransactionProgress{Identity: response.Identity{Backend: "portainer", Stack: "comicarr"}, Phase: "verifying", PhaseStartedAt: response.Stamp(now.Add(-45 * time.Second))}}

	text := prettyText(t, response.Succeed("status", now, status))

	if !strings.Contains(text, "phase elapsed: 45s") || !strings.Contains(text, "phase: verifying") {
		t.Fatal(text)
	}
	status.ActiveTransaction.Interrupted = true
	text = prettyText(t, response.Succeed("status", now, status))
	if strings.Contains(text, "phase elapsed:") || !strings.Contains(text, "interrupted: true") {
		t.Fatal(text)
	}
}
