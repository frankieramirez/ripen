package event

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
)

var secretMarkers = []string{
	"secret", "token", "password", "passwd", "credential",
	"authorization", "auth", "bearer", "cookie", "api_key", "apikey", "private",
}

func TestSuccessReportSinkOnlyReportsSuccessfulTransactions(t *testing.T) {
	var output bytes.Buffer
	sink := NewSuccessReportSink(&output)
	sink.Emit(Envelope{Event: string(TransactionRolledBack)})
	sink.Emit(Envelope{
		Event:      string(TransactionSucceeded),
		OccurredAt: "2026-09-11T15:04:05Z",
		RunID:      stringPointer("run-7"),
		Backend:    stringPointer("portainer"),
		Stack:      stringPointer("media"),
		Service:    stringPointer("web"),
		Data:       Data{OldDigest: "sha256:old", NewDigest: "sha256:new", Detail: "updated and healthy"},
	})

	want := "========= RIPEN UPDATE SUCCEEDED =========\n  stack: media\n  service: web\n  time: 2026-09-11T15:04:05Z\n  run: run-7\n  backend: portainer\n  digest: sha256:old -> sha256:new\n  detail: updated and healthy\n===========================================\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	t.Logf("sample output:\n%s", output.String())
}

func TestSuccessReportSinkEscapesControlCharactersAndKeepsReportsIntact(t *testing.T) {
	var output bytes.Buffer
	sink := NewSuccessReportSink(&output)
	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sink.Emit(Envelope{
				Event:      string(TransactionSucceeded),
				OccurredAt: "2026-09-11T15:04:05Z",
				Stack:      stringPointer("media\nstack"),
				Data:       Data{Detail: "healthy\r\nready"},
			})
		}()
	}
	workers.Wait()

	text := output.String()
	if strings.Count(text, "========= RIPEN UPDATE SUCCEEDED =========\n") != 20 {
		t.Fatalf("report count = %d, want 20; output = %q", strings.Count(text, "========= RIPEN UPDATE SUCCEEDED =========\n"), text)
	}
	if strings.Contains(text, "media\nstack") || strings.Contains(text, "healthy\r\nready") {
		t.Fatalf("control characters were not escaped: %q", text)
	}
	if !strings.Contains(text, `stack: media\x0astack`) || !strings.Contains(text, `detail: healthy\x0d\x0aready`) {
		t.Fatalf("escaped values missing: %q", text)
	}
}

func stringPointer(value string) *string { return &value }

func TestStreamKeepsStructuredEventsAndSuccessReportsAsWholeRecords(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(domain.ActorDaemon, NewWriterSink(&output))
	stream.AddSuccessReport()
	var workers sync.WaitGroup
	for index := 0; index < 20; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			stream.Emit(TransactionSucceeded, Subject{RunID: "run", Stack: "stack", Service: "service"}, Data{Detail: strings.Repeat("x", index+1)})
			stream.Emit(TransactionRolledBack, Subject{RunID: "run"}, Data{})
		}(index)
	}
	workers.Wait()

	lines := strings.Split(output.String(), "\n")
	inReport := false
	structured := 0
	for _, line := range lines {
		if line == "========= RIPEN UPDATE SUCCEEDED =========" {
			if inReport {
				t.Fatal("a report started inside another report")
			}
			inReport = true
			continue
		}
		if line == "===========================================" {
			if !inReport {
				t.Fatal("a report ended without a header")
			}
			inReport = false
			continue
		}
		if inReport {
			if !strings.HasPrefix(line, "  ") {
				t.Fatalf("another record interrupted a report: %q", line)
			}
			continue
		}
		if line == "" {
			continue
		}
		var envelope Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("interleaved structured line %q: %v", line, err)
		}
		structured++
	}
	if inReport || structured != 40 {
		t.Fatalf("incomplete output: inReport=%v structured=%d", inReport, structured)
	}
	if strings.Count(output.String(), "========= RIPEN UPDATE SUCCEEDED =========\n") != 20 {
		t.Fatalf("success reports = %d, want 20", strings.Count(output.String(), "========= RIPEN UPDATE SUCCEEDED =========\n"))
	}
}

func TestNoEventPayloadFieldCanCarryASecret(t *testing.T) {
	payload := reflect.TypeOf(Data{})

	for index := range payload.NumField() {
		field := payload.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		for _, marker := range secretMarkers {
			if strings.Contains(name, marker) {
				t.Errorf("event payload field %q looks like it carries a secret", name)
			}
		}
		if field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Interface {
			t.Errorf("event payload field %q is open-ended; payloads are closed by design", name)
		}
	}
}

func TestTheCatalogueIsClosedAndKnowsItsOwnNames(t *testing.T) {
	if len(Catalogue) != 17 {
		t.Errorf("catalogue = %d events, want the seventeen paging events", len(Catalogue))
	}
	seen := map[Name]bool{}
	for _, name := range Catalogue {
		if seen[name] {
			t.Errorf("%q appears twice in the catalogue", name)
		}
		seen[name] = true
		if !Known(name) {
			t.Errorf("%q is in the catalogue but not Known", name)
		}
	}
	for _, name := range []Name{"run.exploded", NotifierTest, NotifierDeliveryFailed} {
		if Known(name) {
			t.Errorf("%q must not be configurable as a paging event", name)
		}
	}
	for _, name := range DefaultPaging {
		if !Known(name) {
			t.Errorf("default paging event %q is not in the catalogue", name)
		}
	}
}

func TestTheStreamStampsEveryEventWithItsSurface(t *testing.T) {
	var written bytes.Buffer
	stream := NewStream(domain.ActorDaemon, NewWriterSink(&written)).
		WithClock(func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) })

	stream.Emit(BreakerOpened, Subject{
		RunID:   "01920000-0000-7000-8000-000000000000",
		Backend: domain.BackendDockerCompose,
		Stack:   "media",
	}, Data{Reason: "media/web: rollback failed"})

	var envelope Envelope
	if err := json.Unmarshal(written.Bytes(), &envelope); err != nil {
		t.Fatalf("the stream did not write one JSON object: %s", written.String())
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.Event != string(BreakerOpened) {
		t.Errorf("envelope = %+v, want a versioned breaker.opened", envelope)
	}
	if envelope.Actor != string(domain.ActorDaemon) {
		t.Errorf("actor = %q, want the surface that emitted it", envelope.Actor)
	}
	if envelope.OccurredAt != "2026-08-19T12:00:00Z" {
		t.Errorf("occurred_at = %q, want RFC 3339", envelope.OccurredAt)
	}
	if envelope.Service != nil {
		t.Errorf("service = %v, want null for a stack-level event", envelope.Service)
	}
	if envelope.Stack == nil || *envelope.Stack != "media" {
		t.Errorf("stack = %v, want the stack it is about", envelope.Stack)
	}
}

type countingSink struct {
	events int
}

func (c *countingSink) Emit(Envelope) { c.events++ }

type panickingSink struct{}

func (panickingSink) Emit(Envelope) { panic("this sink is broken") }

func TestASinkThatFallsOverDoesNotStopTheOthers(t *testing.T) {
	counter := &countingSink{}
	stream := NewStream(domain.ActorCLI, panickingSink{}, counter)

	stream.Emit(RunFinished, Subject{}, Data{Mode: "monitor"})

	if counter.events != 1 {
		t.Errorf("later sink saw %d events, want 1: a broken reporter must not stop the report",
			counter.events)
	}
}

func TestEachEventIsOneLineOfJSON(t *testing.T) {
	var written bytes.Buffer
	stream := NewStream(domain.ActorCLI, NewWriterSink(&written))

	stream.Emit(RunFinished, Subject{RunID: "run"}, Data{Mode: "monitor", ResultCount: 2})
	stream.Emit(StackError, Subject{Stack: "media"}, Data{Detail: "the backend refused"})

	lines := strings.Split(strings.TrimSpace(written.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want one per event:\n%s", len(lines), written.String())
	}
	for _, line := range lines {
		var envelope Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Errorf("line is not an envelope: %s", line)
		}
	}
}

func TestTransactionProgressDoesNotEnterThePagingCatalogue(t *testing.T) {
	if Known(TransactionProgress) {
		t.Fatal("transaction progress can be configured for paging")
	}
	for _, name := range DefaultPaging {
		if name == TransactionProgress {
			t.Fatal("transaction progress pages by default")
		}
	}
}
