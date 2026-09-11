package updater

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

func schedulerWriter(t *testing.T, u *Updater) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(u.policy.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_, _ = db.Exec("ROLLBACK")
		_ = db.Close()
	})
	return db
}

type unlockOnRunFailure struct {
	sink   scheduleSink
	writer *sql.DB
}

func (s unlockOnRunFailure) Emit(name event.Name, subject event.Subject, data event.Data) {
	if name == event.RunFailed {
		_, _ = s.writer.Exec("ROLLBACK")
	}
	s.sink.Emit(name, subject, data)
}

func TestBusyCompletionProgressDiscardsCandidatesBeforeApplyAdmission(t *testing.T) {
	u, _, _, sink := scheduleFixture(t, 1, 1)
	writer := schedulerWriter(t, u)
	u.events = unlockOnRunFailure{sink: sink, writer: writer}
	c := &coordinator{
		root: u, busy: []bool{true}, running: []bool{true}, due: []bool{false},
		latest: make([][]Result, 1), observations: 1, results: make(chan stackCompletion, 1),
	}
	if !c.acquire(context.Background()) {
		t.Fatal("could not acquire scheduler lease")
	}
	defer c.release()
	if _, err := writer.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = writer.Exec("ROLLBACK") }()
	pass := &observationPass{report: Report{RunID: "completed-observation", Mode: domain.ModeMonitor}, remaining: 1}
	key := state.Key{Backend: domain.BackendPortainer, Stack: "stack-00"}

	c.complete(domain.ModeApply, stackCompletion{
		work:    stackWork{index: 0, pass: pass},
		results: []Result{{Key: key, Code: domain.ResultCandidate, Digest: newDigest}},
	})

	failed := waitScheduleEvent(t, sink, event.RunFailed, "monitor")
	if !strings.Contains(failed.data.Detail, "SQLITE_BUSY") {
		t.Fatalf("expected database contention: %s", failed.data.Detail)
	}
	if c.applying {
		select {
		case <-c.results:
		case <-time.After(5 * time.Second):
			t.Fatal("unexpected admitted Transaction did not finish")
		}
		t.Fatal("a Candidate was admitted after completion progress failed")
	}
	marker, err := u.state.ActiveTransaction()
	if err != nil || marker != nil {
		t.Fatalf("unexpected Transaction: marker=%+v err=%v", marker, err)
	}
}

func sendScheduleTick(t *testing.T, ticks chan<- time.Time, now time.Time, done <-chan error) {
	t.Helper()
	select {
	case ticks <- now:
	case err := <-done:
		t.Fatalf("scheduler exited instead of retrying: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not accept the next tick")
	}
}

func TestSchedulerRetriesBusyProgressOnTheNextTickWithoutStartingWork(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 1, 1)
	writer := schedulerWriter(t, u)
	var once sync.Once
	port.preflight = func() (err error) {
		once.Do(func() { _, err = writer.Exec("BEGIN IMMEDIATE") })
		return err
	}

	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	failed := waitScheduleEvent(t, sink, event.RunFailed, "monitor")

	if !strings.Contains(failed.data.Detail, "SQLITE_BUSY") {
		t.Fatalf("expected database contention: %s", failed.data.Detail)
	}
	if port.requests.Load() != 0 {
		t.Fatal("observation started without durable scheduler progress")
	}
	if _, err := writer.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	waitScheduleEvent(t, sink, event.TransactionSucceeded, "")
	finishSchedule(t, cancel, done)
	if port.count("stack-00") == 0 {
		t.Fatal("scheduler did not resume observation")
	}
}

func TestBusyProgressDrainsObservationsAndDiscardsQueuedAdmissionsBeforeRetry(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 3, 1)
	gate := make(chan struct{})
	port.gate = gate
	port.started = make(chan string, 20)
	writer := schedulerWriter(t, u)
	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	select {
	case <-port.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first observation did not start")
	}
	if _, err := writer.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	failed := waitScheduleEvent(t, sink, event.RunFailed, "monitor")

	if !strings.Contains(failed.data.Detail, "SQLITE_BUSY") {
		t.Fatalf("expected database contention: %s", failed.data.Detail)
	}
	if _, err := writer.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	drained := false
	for !drained {
		select {
		case e := <-sink:
			drained = e.name == event.RunFailed || e.name == event.RunFinished
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled observation pass did not finish")
		}
	}
	if port.active.Load() != 0 || port.requests.Load() != 1 {
		t.Fatalf("observations were not drained: active=%d requests=%d", port.active.Load(), port.requests.Load())
	}
	port.mu.Lock()
	deployments := len(port.deployments)
	port.mu.Unlock()
	if deployments != 0 {
		t.Fatal("deployment was admitted during contention")
	}
	close(gate)
	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	waitScheduleEvent(t, sink, event.TransactionSucceeded, "")
	finishSchedule(t, cancel, done)
	if port.count("stack-02") == 0 {
		t.Fatal("queued stack was not observed on retry")
	}
}

func TestSchedulerStillStopsWhenProgressCannotBeSavedForANonBusyError(t *testing.T) {
	u, port, _, _ := scheduleFixture(t, 1, 1)
	writer := schedulerWriter(t, u)
	port.preflight = func() error {
		_, err := writer.Exec("DROP TABLE scheduler_progress")
		return err
	}

	_, cancel, done := startSchedule(t, u, domain.ModeApply)
	defer cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Fatalf("expected missing table failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler ignored a non-busy database error")
	}
	if port.requests.Load() != 0 {
		t.Fatal("work started after a non-busy database error")
	}
}

func TestBusyProgressLetsAnActiveTransactionFinishBeforeRetrying(t *testing.T) {
	u, port, clock, sink := scheduleFixture(t, 2, 2)
	gate := make(chan struct{})
	port.deployGate = gate
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	writer := schedulerWriter(t, u)
	ticks, cancel, done := startSchedule(t, u, domain.ModeApply)
	waitScheduleEvent(t, sink, event.TransactionStarted, "")
	if _, err := writer.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	failed := waitScheduleEvent(t, sink, event.RunFailed, "monitor")

	if !strings.Contains(failed.data.Detail, "SQLITE_BUSY") {
		t.Fatalf("expected database contention: %s", failed.data.Detail)
	}
	if _, err := writer.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	marker, err := u.state.ActiveTransaction()
	if err != nil || marker == nil || marker.Key.Stack != "stack-00" {
		t.Fatalf("active Transaction ownership was lost: marker=%+v err=%v", marker, err)
	}
	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	release.Do(func() { close(gate) })
	waitScheduleEvent(t, sink, event.TransactionSucceeded, "")
	waitScheduleEvent(t, sink, event.RunFinished, "apply")
	port.mu.Lock()
	deployments := len(port.deployments)
	port.mu.Unlock()
	if deployments != 1 {
		t.Fatalf("new Transaction was admitted while paused: deployments=%d", deployments)
	}
	clock.Sleep(time.Minute)
	sendScheduleTick(t, ticks, clock.Now(), done)
	succeeded := waitScheduleEvent(t, sink, event.TransactionSucceeded, "")
	if succeeded.subject.Stack != "stack-01" {
		t.Fatalf("unexpected next Transaction: %+v", succeeded.subject)
	}
	finishSchedule(t, cancel, done)
}
