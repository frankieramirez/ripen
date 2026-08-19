package notifier

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

// destination is a webhook receiver that records what it was sent and
// answers however the test tells it to.
type destination struct {
	mutex     sync.Mutex
	received  []event.Envelope
	status    int
	failFirst int
	requests  int
	tokens    []string
}

func (d *destination) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		d.mutex.Lock()
		defer d.mutex.Unlock()
		d.requests++
		d.tokens = append(d.tokens, request.Header.Get("Authorization"))
		if d.requests <= d.failFirst {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		if d.status != 0 && d.status != http.StatusOK {
			writer.WriteHeader(d.status)
			return
		}
		var envelope event.Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		d.received = append(d.received, envelope)
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server
}

func (d *destination) delivered() []event.Envelope {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return append([]event.Envelope(nil), d.received...)
}

func (d *destination) attempts() int {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.requests
}

// recordingStream collects the stream-only Events the Notifier reports.
type recordingStream struct {
	mutex  sync.Mutex
	events []event.Envelope
}

func (r *recordingStream) Emit(envelope event.Envelope) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.events = append(r.events, envelope)
}

func (r *recordingStream) saw(name event.Name) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, envelope := range r.events {
		if envelope.Event == string(name) {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state", "ripen.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type harness struct {
	webhook *Webhook
	store   *state.Store
	sent    *destination
	stream  *recordingStream
}

func newHarness(t *testing.T, store *state.Store) *harness {
	t.Helper()
	sent := &destination{}
	server := sent.server(t)
	directory := t.TempDir()

	webhookSettings := config.WebhookSettings{
		URLFile:        writeFile(t, directory, "url", server.URL+"\n"),
		TokenFile:      writeFile(t, directory, "token", "hook-token\n"),
		Events:         []event.Name{event.BreakerOpened, event.StackError, event.StackRecovered, event.RunFinished},
		TimeoutSeconds: 2,
	}
	stream := &recordingStream{}
	events := event.NewStream(domain.ActorDaemon, stream)
	webhook, err := New(Options{
		Settings: webhookSettings,
		Store:    store,
		Stream:   events,
		Backoff:  time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = webhook.Close() })
	return &harness{webhook: webhook, store: store, sent: sent, stream: stream}
}

func envelopeFor(name event.Name, stack string, data event.Data) event.Envelope {
	subject := stack
	return event.Envelope{
		SchemaVersion: event.SchemaVersion,
		Event:         string(name),
		OccurredAt:    time.Now().UTC().Format(time.RFC3339),
		Stack:         &subject,
		Actor:         string(domain.ActorDaemon),
		Data:          data,
	}
}

// deliverAndDrain emits envelopes and waits for the queue to empty.
func (h *harness) deliverAndDrain(t *testing.T, envelopes ...event.Envelope) {
	t.Helper()
	for _, envelope := range envelopes {
		h.webhook.Emit(envelope)
	}
	if err := h.webhook.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTheConfiguredEventsAreDelivered(t *testing.T) {
	harness := newHarness(t, openStore(t))

	harness.deliverAndDrain(t,
		envelopeFor(event.BreakerOpened, "media", event.Data{Reason: "rollback failed"}),
		envelopeFor(event.CandidateObserved, "media", event.Data{Digest: "sha256:abc"}),
	)

	delivered := harness.sent.delivered()
	if len(delivered) != 1 || delivered[0].Event != string(event.BreakerOpened) {
		t.Errorf("delivered = %+v, want only the configured event", delivered)
	}
}

func TestTheSameNewsIsNotDeliveredTwice(t *testing.T) {
	harness := newHarness(t, openStore(t))
	same := event.Data{Reason: "media/web: rollback failed"}

	harness.deliverAndDrain(t,
		envelopeFor(event.BreakerOpened, "media", same),
		envelopeFor(event.BreakerOpened, "media", same),
	)

	if delivered := harness.sent.delivered(); len(delivered) != 1 {
		t.Errorf("delivered = %d, want one: paging is on changes, not on continued existence", len(delivered))
	}
}

func TestRecoveryReArmsTheFailureEvent(t *testing.T) {
	store := openStore(t)
	harness := newHarness(t, store)
	failure := event.Data{Detail: "the backend refused"}

	harness.webhook.Emit(envelopeFor(event.StackError, "media", failure))
	harness.webhook.Emit(envelopeFor(event.StackRecovered, "media", event.Data{Digest: "sha256:abc"}))
	harness.webhook.Emit(envelopeFor(event.StackError, "media", failure))
	if err := harness.webhook.Close(); err != nil {
		t.Fatal(err)
	}

	delivered := harness.sent.delivered()
	if len(delivered) != 3 {
		t.Fatalf("delivered = %d (%v), want the failure to page again after recovery",
			len(delivered), names(delivered))
	}
	if delivered[2].Event != string(event.StackError) {
		t.Errorf("last delivery = %q, want the re-armed failure", delivered[2].Event)
	}
}

func TestChangingTheDestinationResetsSuppression(t *testing.T) {
	store := openStore(t)
	first := newHarness(t, store)
	reason := event.Data{Reason: "media/web: rollback failed"}
	first.deliverAndDrain(t, envelopeFor(event.BreakerOpened, "media", reason))

	// A second Notifier pointing somewhere else knows nothing about what
	// the first destination was told.
	second := newHarness(t, store)
	second.deliverAndDrain(t, envelopeFor(event.BreakerOpened, "media", reason))

	if delivered := second.sent.delivered(); len(delivered) != 1 {
		t.Errorf("second destination received %d events, want current state paged once", len(delivered))
	}
}

func TestDeliveryRetriesAndThenReportsOnTheStreamOnly(t *testing.T) {
	harness := newHarness(t, openStore(t))
	harness.sent.failFirst = 99

	harness.deliverAndDrain(t, envelopeFor(event.BreakerOpened, "media", event.Data{Reason: "down"}))

	if attempts := harness.sent.attempts(); attempts != deliveryAttempts {
		t.Errorf("attempts = %d, want %d — one try plus two retries", attempts, deliveryAttempts)
	}
	if !harness.stream.saw(event.NotifierDeliveryFailed) {
		t.Error("a failed delivery must be reported on the stream")
	}
	for _, envelope := range harness.stream.events {
		if strings.Contains(envelope.Data.Detail, "http://") {
			t.Errorf("the failure report names the destination: %q", envelope.Data.Detail)
		}
	}
	health, err := harness.store.NotifierHealth()
	if err != nil {
		t.Fatal(err)
	}
	if health.ConsecutiveFailures == 0 {
		t.Error("a failed delivery must be recorded in the persisted notifier health")
	}
}

func TestARefusedDeliveryIsNotRetried(t *testing.T) {
	harness := newHarness(t, openStore(t))
	harness.sent.status = http.StatusForbidden

	harness.deliverAndDrain(t, envelopeFor(event.BreakerOpened, "media", event.Data{Reason: "down"}))

	if attempts := harness.sent.attempts(); attempts != 1 {
		t.Errorf("attempts = %d, want 1: the destination understood and refused", attempts)
	}
}

func TestASuccessfulDeliveryRecordsNotifierHealthAndCarriesTheToken(t *testing.T) {
	harness := newHarness(t, openStore(t))

	harness.deliverAndDrain(t, envelopeFor(event.BreakerOpened, "media", event.Data{Reason: "down"}))

	health, err := harness.store.NotifierHealth()
	if err != nil {
		t.Fatal(err)
	}
	if health.LastSuccessAt == nil || health.ConsecutiveFailures != 0 {
		t.Errorf("health = %+v, want a recorded success", health)
	}
	if len(harness.sent.tokens) == 0 || harness.sent.tokens[0] != "Bearer hook-token" {
		t.Errorf("authorization = %v, want the configured bearer token", harness.sent.tokens)
	}
}

func TestNotifyTestSendsARealEventThroughTheRealPath(t *testing.T) {
	harness := newHarness(t, openStore(t))

	if err := harness.webhook.Test(); err != nil {
		t.Fatal(err)
	}

	delivered := harness.sent.delivered()
	if len(delivered) != 1 || delivered[0].Event != string(event.NotifierTest) {
		t.Errorf("delivered = %v, want one notifier.test", names(delivered))
	}
}

func TestAFullQueueDropsEventsRatherThanBlockingARun(t *testing.T) {
	store := openStore(t)
	sent := &destination{}
	server := sent.server(t)
	directory := t.TempDir()
	blocked := make(chan struct{})
	webhook, err := New(Options{
		Settings: config.WebhookSettings{
			URLFile:        writeFile(t, directory, "url", server.URL),
			Events:         []event.Name{event.StackError},
			TimeoutSeconds: 2,
		},
		Store:      store,
		QueueSize:  1,
		Backoff:    time.Millisecond,
		HTTPClient: &http.Client{Transport: blockingTransport{blocked: blocked}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The worker is stuck on the first delivery and the queue holds one,
	// so everything after that has nowhere to go.
	for range 20 {
		webhook.Emit(envelopeFor(event.StackError, "media", event.Data{Detail: "down"}))
	}
	dropped := webhook.Dropped()
	close(blocked)
	if err := webhook.Close(); err != nil {
		t.Fatal(err)
	}

	if dropped == 0 {
		t.Error("a full queue must drop events, never block the run that emitted them")
	}
}

// blockingTransport holds every request until the channel closes.
type blockingTransport struct {
	blocked chan struct{}
}

func (b blockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	<-b.blocked
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func TestTheHeartbeatDeliversAnOtherwiseSuppressedRun(t *testing.T) {
	store := openStore(t)
	sent := &destination{}
	server := sent.server(t)
	directory := t.TempDir()
	settings := config.WebhookSettings{
		URLFile:        writeFile(t, directory, "url", server.URL),
		Events:         []event.Name{event.RunFinished},
		TimeoutSeconds: 2,
	}
	moment := time.Now().UTC()
	same := event.Data{Mode: "monitor"}

	quiet, err := New(Options{Settings: settings, Heartbeat: time.Hour, Store: store,
		Backoff: time.Millisecond, Clock: func() time.Time { return moment }})
	if err != nil {
		t.Fatal(err)
	}
	quiet.Emit(envelopeFor(event.RunFinished, "", same))
	quiet.Emit(envelopeFor(event.RunFinished, "", same))
	if err := quiet.Close(); err != nil {
		t.Fatal(err)
	}
	if delivered := sent.delivered(); len(delivered) != 1 {
		t.Fatalf("delivered = %d, want the repeat suppressed", len(delivered))
	}

	// Time passes with nothing new to say. The heartbeat still speaks, so
	// silence stays distinguishable from a dead notifier.
	later := moment.Add(2 * time.Hour)
	beating, err := New(Options{Settings: settings, Heartbeat: time.Hour, Store: store,
		Backoff: time.Millisecond, Clock: func() time.Time { return later }})
	if err != nil {
		t.Fatal(err)
	}
	beating.Emit(envelopeFor(event.RunFinished, "", same))
	if err := beating.Close(); err != nil {
		t.Fatal(err)
	}

	if delivered := sent.delivered(); len(delivered) != 2 {
		t.Errorf("delivered = %d (%v), want the first and the heartbeat", len(delivered), names(delivered))
	}
}

func TestAPlaintextDestinationIsRefusedUnlessItIsThisHost(t *testing.T) {
	store := openStore(t)
	directory := t.TempDir()

	_, err := New(Options{
		Settings: config.WebhookSettings{URLFile: writeFile(t, directory, "url", "http://hooks.example.com/x")},
		Store:    store,
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("error = %v, want the plaintext refusal", err)
	}

	if _, err := New(Options{
		Settings: config.WebhookSettings{URLFile: writeFile(t, directory, "local", "http://127.0.0.1:8080/hook")},
		Store:    store,
	}); err != nil {
		t.Errorf("error = %v, want a loopback destination accepted", err)
	}
}

func names(envelopes []event.Envelope) []string {
	var list []string
	for _, envelope := range envelopes {
		list = append(list, envelope.Event)
	}
	return list
}
