// Package notifier is the webhook sink of the Event stream: the one
// outbound Notifier Ripen ships with. It is off by default, filtered,
// and deliberately unreliable in one direction only — delivery may be
// dropped, never a Transaction.
//
// Three rules shape it. Delivery is at-most-once and fail-open: a run
// never waits for a webhook and never fails because of one. Paging is
// on state *changes*, so a stack that has been broken for a week pages
// once, and pages again the moment it breaks after recovering. And the
// state database is the system of record: an Event can only report what
// is already written there.
package notifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/frankieramirez/ripen/internal/config"
	"github.com/frankieramirez/ripen/internal/event"
	"github.com/frankieramirez/ripen/internal/state"
)

// deliveryAttempts is one attempt plus two retries. More than that and
// the destination is down, which is a thing to report, not to hammer.
const deliveryAttempts = 3

// defaultQueueSize bounds the outbound queue. When it fills, Events are
// dropped and counted: the run keeps going.
const defaultQueueSize = 128

// Options configures a Webhook.
type Options struct {
	Settings   config.WebhookSettings
	Heartbeat  time.Duration
	Store      *state.Store
	Stream     *event.Stream
	HTTPClient *http.Client
	Clock      func() time.Time
	QueueSize  int
	// Backoff is the pause between delivery attempts.
	Backoff time.Duration
}

// Webhook is the outbound Notifier.
type Webhook struct {
	url        string
	token      string
	events     []event.Name
	timeout    time.Duration
	heartbeat  time.Duration
	backoff    time.Duration
	store      *state.Store
	stream     *event.Stream
	httpClient *http.Client
	clock      func() time.Time

	queue chan event.Envelope
	done  chan struct{}
	once  sync.Once

	mutex   sync.Mutex
	dropped int
}

// New reads the destination, resets suppression if it changed, and
// starts the delivery worker.
func New(options Options) (*Webhook, error) {
	destination, err := readSecret(options.Settings.URLFile)
	if err != nil {
		return nil, fmt.Errorf("reading the webhook url file: %w", err)
	}
	if err := usableDestination(destination); err != nil {
		return nil, err
	}
	token := ""
	if options.Settings.TokenFile != "" {
		if token, err = readSecret(options.Settings.TokenFile); err != nil {
			return nil, fmt.Errorf("reading the webhook token file: %w", err)
		}
	}
	if options.Store == nil {
		return nil, errors.New("a notifier needs the state store")
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{}
	}
	if options.QueueSize <= 0 {
		options.QueueSize = defaultQueueSize
	}
	if options.Backoff == 0 {
		options.Backoff = 500 * time.Millisecond
	}
	timeout := time.Duration(options.Settings.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	events := options.Settings.Events
	if len(events) == 0 {
		events = event.DefaultPaging
	}

	webhook := &Webhook{
		url:        destination,
		token:      token,
		events:     events,
		timeout:    timeout,
		heartbeat:  options.Heartbeat,
		backoff:    options.Backoff,
		store:      options.Store,
		stream:     options.Stream,
		httpClient: options.HTTPClient,
		clock:      options.Clock,
		queue:      make(chan event.Envelope, options.QueueSize),
		done:       make(chan struct{}),
	}

	// Suppression records what a *destination* has already been told.
	// Point Ripen at a new destination and it knows nothing, so the
	// table is cleared and current state pages once. That is correct.
	fingerprint := destinationFingerprint(destination)
	stored, err := options.Store.NotifierDestination()
	if err != nil {
		return nil, err
	}
	if stored != fingerprint {
		if err := options.Store.ResetSuppression(); err != nil {
			return nil, err
		}
		if err := options.Store.SetNotifierDestination(fingerprint); err != nil {
			return nil, err
		}
	}

	go webhook.work()
	return webhook, nil
}

// Emit queues one Event for delivery. It never blocks: a full queue
// drops the Event and counts it, because a run must not wait on a
// webhook.
func (w *Webhook) Emit(envelope event.Envelope) {
	if !w.wants(event.Name(envelope.Event)) {
		return
	}
	select {
	case w.queue <- envelope:
	default:
		w.mutex.Lock()
		w.dropped++
		dropped := w.dropped
		w.mutex.Unlock()
		w.report(envelope, fmt.Errorf("the notifier queue is full"), 0, dropped)
	}
}

// wants reports whether this Event is one the operator asked for. The
// delivery test always passes; the Notifier's own failures never do.
func (w *Webhook) wants(name event.Name) bool {
	if name == event.NotifierTest {
		return true
	}
	if name == event.NotifierDeliveryFailed {
		return false
	}
	return slices.Contains(w.events, name)
}

// Close drains the queue and stops the worker.
func (w *Webhook) Close() error {
	w.once.Do(func() {
		close(w.queue)
		<-w.done
	})
	return nil
}

// Dropped is how many Events this process could not queue.
func (w *Webhook) Dropped() int {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.dropped
}

// Test sends a real notifier.test through the real delivery path, and
// reports what happened. Nothing about it is a special case except that
// it is never filtered or suppressed.
func (w *Webhook) Test() error {
	envelope := event.Envelope{
		SchemaVersion: event.SchemaVersion,
		Event:         string(event.NotifierTest),
		OccurredAt:    w.clock().Format(time.RFC3339),
		Data:          event.Data{Detail: "a delivery test from ripen notify test"},
	}
	attempts, err := w.deliver(envelope)
	if err != nil {
		_ = w.store.RecordNotifierFailure()
		w.report(envelope, err, attempts, w.Dropped())
		return err
	}
	return w.store.RecordNotifierSuccess(w.clock())
}

func (w *Webhook) work() {
	defer close(w.done)
	for envelope := range w.queue {
		w.handle(envelope)
	}
}

func (w *Webhook) handle(envelope event.Envelope) {
	send, err := w.shouldSend(envelope)
	if err != nil || !send {
		return
	}
	attempts, err := w.deliver(envelope)
	if err != nil {
		_ = w.store.RecordNotifierFailure()
		w.report(envelope, err, attempts, w.Dropped())
		return
	}
	if err := w.store.RecordNotifierSuccess(w.clock()); err != nil {
		return
	}
	w.remember(envelope)
}

// shouldSend applies suppression: page on a change of state, not on the
// continued existence of one.
func (w *Webhook) shouldSend(envelope event.Envelope) (bool, error) {
	name := event.Name(envelope.Event)
	if name == event.NotifierTest {
		return true, nil
	}
	stack, service := subject(envelope)
	previous, found, err := w.store.SuppressionState(envelope.Event, stack, service)
	if err != nil {
		return false, err
	}
	if found && previous == suppressionState(envelope) {
		return w.heartbeatDue(name)
	}
	return true, nil
}

// heartbeatDue lets one otherwise-suppressed run.finished through when
// nothing has been delivered for the configured interval, so silence
// stays distinguishable from a dead Notifier.
func (w *Webhook) heartbeatDue(name event.Name) (bool, error) {
	if w.heartbeat <= 0 || name != event.RunFinished {
		return false, nil
	}
	health, err := w.store.NotifierHealth()
	if err != nil {
		return false, err
	}
	if health.LastSuccessAt == nil {
		return true, nil
	}
	return w.clock().Sub(*health.LastSuccessAt) >= w.heartbeat, nil
}

// remember records what the destination has now been told, and re-arms
// the paired failure Event when this one is a recovery.
func (w *Webhook) remember(envelope event.Envelope) {
	stack, service := subject(envelope)
	_ = w.store.SetSuppressionState(envelope.Event, stack, service,
		suppressionState(envelope), w.clock())
	switch event.Name(envelope.Event) {
	case event.StackRecovered:
		_ = w.store.ClearSuppression(string(event.StackError), stack, service)
	case event.BreakerCleared:
		_ = w.store.ClearSuppression(string(event.BreakerOpened), stack, service)
	}
}

// deliver posts the Event, retrying only what retrying can fix.
func (w *Webhook) deliver(envelope event.Envelope) (int, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return 0, err
	}
	var failure error
	for attempt := 1; attempt <= deliveryAttempts; attempt++ {
		status, err := w.post(body)
		switch {
		case err == nil:
			return attempt, nil
		case status >= 400 && status < 500:
			// The destination understood and refused. Retrying that is
			// just noise on someone else's server.
			return attempt, err
		default:
			failure = err
		}
		if attempt < deliveryAttempts {
			time.Sleep(w.backoff * time.Duration(attempt))
		}
	}
	return deliveryAttempts, failure
}

func (w *Webhook) post(body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "ripen")
	if w.token != "" {
		request.Header.Set("Authorization", "Bearer "+w.token)
	}
	response, err := w.httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("the webhook answered HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

// report announces a delivery failure on the Event stream only. A
// Notifier that cannot deliver cannot deliver news of that either, and
// the destination URL never appears in the message.
func (w *Webhook) report(envelope event.Envelope, err error, attempts, dropped int) {
	if w.stream == nil {
		return
	}
	w.stream.Emit(event.NotifierDeliveryFailed, event.Subject{
		Stack:   value(envelope.Stack),
		Service: value(envelope.Service),
	}, event.Data{
		Detail:   fmt.Sprintf("could not deliver %s: %v", envelope.Event, err),
		Attempts: attempts,
		Dropped:  dropped,
	})
}

// suppressionState is what "the same news" means: the parts of a payload
// that decide whether an operator would want telling again.
func suppressionState(envelope event.Envelope) string {
	return strings.Join([]string{
		envelope.Data.Result,
		envelope.Data.Digest,
		envelope.Data.NewDigest,
		envelope.Data.Reason,
	}, "|")
}

func subject(envelope event.Envelope) (string, string) {
	return value(envelope.Stack), value(envelope.Service)
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

// destinationFingerprint identifies a destination without storing it.
func destinationFingerprint(destination string) string {
	sum := sha256.Sum256([]byte(destination))
	return hex.EncodeToString(sum[:])
}

// usableDestination refuses plaintext to anywhere but this host: a
// webhook carries what Ripen is doing to your infrastructure.
func usableDestination(destination string) error {
	parsed, err := url.Parse(destination)
	if err != nil || parsed.Host == "" {
		return errors.New("the webhook url is not a URL")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if address := net.ParseIP(host); address != nil && address.IsLoopback() {
			return nil
		}
		return errors.New("the webhook url must use https unless it points at this host")
	default:
		return errors.New("the webhook url must use https")
	}
}

func readSecret(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the secret path is operator-supplied by design
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("the file is empty")
	}
	return value, nil
}
