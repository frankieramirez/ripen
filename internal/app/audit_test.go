package app

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/state"
)

func openAuditStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func recordAuditAttempt(t *testing.T, store *state.Store, attempt state.Attempt, offset int) {
	t.Helper()
	if err := store.RecordAttempt(attempt, time.Date(2026, 8, 1, 0, 0, offset, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRejectsMalformedRequestsBeforeReadingState(t *testing.T) {
	cases := []AuditRequest{
		{Limit: "12junk"},
		{Limit: " 12"},
		{Limit: "x"},
		{Limit: "999999999999999999999999999999"},
		{Cursor: "+1"},
		{Cursor: " 1"},
		{Cursor: "1 "},
		{Cursor: "abc"},
		{Cursor: "12junk"},
		{Cursor: "１２"},
		{Cursor: "0"},
		{Cursor: "-1"},
		{Cursor: "999999999999999999999999999999"},
	}

	for _, request := range cases {
		_, err := (&App{}).Audit(request)
		if !errors.Is(err, ErrInvalidAuditRequest) {
			t.Errorf("Audit(%+v) error = %v, want ErrInvalidAuditRequest", request, err)
		}
	}
}

func TestAuditUsesFiftyAsTheDefaultLimit(t *testing.T) {
	store := openAuditStore(t)
	for i := 0; i < 51; i++ {
		recordAuditAttempt(t, store, state.Attempt{
			Key:   state.Key{Backend: domain.BackendPortainer, Stack: "stack"},
			RunID: fmt.Sprintf("run-%d", i), Result: domain.ResultUpdated,
		}, i)
	}

	for _, limit := range []string{"", "0", "-1"} {
		trail, err := (&App{Store: store}).Audit(AuditRequest{Limit: limit})
		if err != nil {
			t.Fatalf("Audit(limit %q) error = %v", limit, err)
		}
		if len(trail.Attempts) != 50 || trail.NextCursor == nil {
			t.Errorf("Audit(limit %q) = %d attempts, cursor %v; want 50 and a next cursor", limit, len(trail.Attempts), trail.NextCursor)
		}
	}
}

func TestAuditMapsEveryFilterAndReturnsOnlyMatchingAttempts(t *testing.T) {
	store := openAuditStore(t)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendDockerCompose, Stack: "media", Service: "radarr"},
		RunID: "wanted", Result: domain.ResultUpdated, Detail: "wanted-match",
	}, 1)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendPortainer, Stack: "media", Service: "radarr"},
		RunID: "wanted", Result: domain.ResultUpdated,
	}, 2)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendDockerCompose, Stack: "media", Service: "sonarr"},
		RunID: "wanted", Result: domain.ResultUpdated,
	}, 3)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendDockerCompose, Stack: "media", Service: "radarr"},
		RunID: "other", Result: domain.ResultUpdated,
	}, 4)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendDockerCompose, Stack: "other", Service: "radarr"},
		RunID: "wanted", Result: domain.ResultUpdated,
	}, 5)
	recordAuditAttempt(t, store, state.Attempt{
		Key:   state.Key{Backend: domain.BackendDockerCompose, Stack: "media", Service: "radarr"},
		RunID: "wanted", Result: domain.ResultError,
	}, 6)

	trail, err := (&App{Store: store}).Audit(AuditRequest{
		Backend: string(domain.BackendDockerCompose), Stack: "media", Service: "radarr",
		RunID: "wanted", Result: string(domain.ResultUpdated), Limit: "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(trail.Attempts) != 1 || trail.Attempts[0].RunID != "wanted" || trail.Attempts[0].Backend != string(domain.BackendDockerCompose) || trail.Attempts[0].Service == nil || *trail.Attempts[0].Service != "radarr" {
		t.Errorf("filtered attempts = %+v, want the one fully matching attempt", trail.Attempts)
	}
	if trail.NextCursor != nil {
		t.Errorf("next cursor = %q, want null", *trail.NextCursor)
	}
}

func TestAuditContinuesFilteredPagesAndReturnsNullOnTheLastPage(t *testing.T) {
	store := openAuditStore(t)
	for i, runID := range []string{"keep", "skip", "keep", "skip", "keep"} {
		recordAuditAttempt(t, store, state.Attempt{
			Key: state.Key{Backend: domain.BackendPortainer, Stack: "stack"}, RunID: runID, Result: domain.ResultUpdated,
			Detail: fmt.Sprintf("attempt-%d", i),
		}, i)
	}

	first, err := (&App{Store: store}).Audit(AuditRequest{RunID: "keep", Limit: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Attempts) != 2 || first.Attempts[0].Detail != "attempt-4" || first.Attempts[1].Detail != "attempt-2" || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want two records and a cursor", first)
	}

	second, err := (&App{Store: store}).Audit(AuditRequest{RunID: "keep", Limit: "2", Cursor: *first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Attempts) != 1 || second.Attempts[0].Detail != "attempt-0" || second.NextCursor != nil {
		t.Errorf("second page = %+v, want one record and null cursor", second)
	}

	empty, err := (&App{Store: store}).Audit(AuditRequest{RunID: "missing", Limit: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Attempts) != 0 || empty.Attempts == nil || empty.NextCursor != nil {
		t.Errorf("empty page = %+v, want empty attempts and null cursor", empty)
	}
}

func TestAuditAcceptsMaximumCursorAndMaximumSafeLimit(t *testing.T) {
	store := openAuditStore(t)
	recordAuditAttempt(t, store, state.Attempt{
		Key: state.Key{Backend: domain.BackendPortainer, Stack: "stack"}, RunID: "run", Result: domain.ResultUpdated,
	}, 1)

	for _, request := range []AuditRequest{
		{Cursor: strconv.FormatInt(math.MaxInt64, 10), Limit: "1"},
		{Limit: strconv.Itoa(math.MaxInt - 1)},
	} {
		trail, err := (&App{Store: store}).Audit(request)
		if err != nil {
			t.Errorf("Audit(%+v) error = %v", request, err)
		}
		if trail.Attempts == nil {
			t.Errorf("Audit(%+v) attempts = nil, want an initialized slice", request)
		}
	}
}
