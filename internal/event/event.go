// Package event is Ripen's Event stream: one stream, many sinks. Every
// Event goes to the structured stderr sink, always; the webhook Notifier
// is a second sink that takes a filtered subset and is off by default.
//
// The payload is a single closed struct, not a free map. That is what
// replaces the Python redaction scrubber: a secret cannot reach an Event
// because there is no field to put one in, and a test walks the payload's
// field names to keep it that way.
package event

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/frankieramirez/ripen/internal/domain"
)

// SchemaVersion is the Event envelope's version. It moves independently
// of the Response envelope's, and additive changes do not bump it.
const SchemaVersion = domain.EventSchemaVersion

// Name is an Event's name: noun first, dotted.
type Name string

// The Event catalogue. These seventeen are the paging-eligible Events —
// every name a `notifier.events` list may contain.
const (
	RunFinished Name = "run.finished"
	RunFailed   Name = "run.failed"

	BaselineRecorded Name = "baseline.recorded"
	BaselineBlocked  Name = "baseline.blocked"

	CandidateObserved Name = "candidate.observed"
	CandidateMatured  Name = "candidate.matured"

	TransactionStarted        Name = "transaction.started"
	TransactionSucceeded      Name = "transaction.succeeded"
	TransactionRolledBack     Name = "transaction.rolled_back"
	TransactionRollbackFailed Name = "transaction.rollback_failed"

	BreakerOpened  Name = "breaker.opened"
	BreakerCleared Name = "breaker.cleared"

	ProposalCreated  Name = "proposal.created"
	ProposalDeployed Name = "proposal.deployed"
	ProposalCleared  Name = "proposal.cleared"

	StackError     Name = "stack.error"
	StackRecovered Name = "stack.recovered"
)

// Two Events exist outside the paging catalogue. NotifierTest is what
// `ripen notify test` sends, and it bypasses filtering by definition —
// the point is to prove the real path works. NotifierDeliveryFailed
// never leaves the stream: a Notifier cannot page about its own inability
// to page.
const (
	NotifierTest           Name = "notifier.test"
	NotifierDeliveryFailed Name = "notifier.delivery_failed"
)

// Catalogue is every paging-eligible Event name, in catalogue order.
var Catalogue = []Name{
	RunFinished, RunFailed,
	BaselineRecorded, BaselineBlocked,
	CandidateObserved, CandidateMatured,
	TransactionStarted, TransactionSucceeded, TransactionRolledBack, TransactionRollbackFailed,
	BreakerOpened, BreakerCleared,
	ProposalCreated, ProposalDeployed, ProposalCleared,
	StackError, StackRecovered,
}

// DefaultPaging is the subset a Notifier delivers when the operator does
// not name their own: the Events that mean something is wrong, has been
// fixed, or has changed what is running.
var DefaultPaging = []Name{
	RunFailed,
	TransactionSucceeded, TransactionRolledBack, TransactionRollbackFailed,
	BreakerOpened, BreakerCleared,
	ProposalCreated,
	StackError, StackRecovered,
}

// Known reports whether a name is in the paging catalogue. An unknown
// name in configuration is a config-load error, never a silent no-op.
func Known(name Name) bool {
	for _, known := range Catalogue {
		if known == name {
			return true
		}
	}
	return false
}

// Data is every field any Event payload may carry. One closed struct,
// deliberately: an Event cannot carry a field nobody reviewed, so no
// secret can travel in one.
type Data struct {
	Mode           string `json:"mode,omitempty"`
	Result         string `json:"result,omitempty"`
	Digest         string `json:"digest,omitempty"`
	OldDigest      string `json:"old_digest,omitempty"`
	NewDigest      string `json:"new_digest,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ProposalURL    string `json:"proposal_url,omitempty"`
	Observations   int    `json:"observations,omitempty"`
	UpdatesApplied int    `json:"updates_applied,omitempty"`
	ResultCount    int    `json:"result_count,omitempty"`
	BreakerOpen    bool   `json:"breaker_open,omitempty"`
	Created        bool   `json:"created,omitempty"`
	Attempts       int    `json:"attempts,omitempty"`
	Dropped        int    `json:"dropped,omitempty"`
}

// Envelope is one Event on the wire.
type Envelope struct {
	SchemaVersion int     `json:"schema_version"`
	Event         string  `json:"event"`
	OccurredAt    string  `json:"occurred_at"`
	RunID         *string `json:"run_id"`
	Backend       *string `json:"backend"`
	Stack         *string `json:"stack"`
	Service       *string `json:"service"`
	Actor         string  `json:"actor"`
	Data          Data    `json:"data"`
}

// Sink receives Events. Sinks must not fail a run: a sink that errors
// swallows it, and one that panics is contained by the stream.
type Sink interface {
	Emit(envelope Envelope)
}

// Subject is who an Event is about.
type Subject struct {
	RunID   string
	Backend domain.Backend
	Stack   string
	Service string
}

// Stream fans one Event out to every sink, in order.
type Stream struct {
	actor domain.Actor
	sinks []Sink
	clock func() time.Time
	mutex sync.Mutex
}

// NewStream builds a stream for one surface. The actor is the surface
// itself and is stamped on every Event; it is never a parameter a caller
// can supply.
func NewStream(actor domain.Actor, sinks ...Sink) *Stream {
	return &Stream{actor: actor, sinks: sinks, clock: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the stream's clock, for tests.
func (s *Stream) WithClock(clock func() time.Time) *Stream {
	s.clock = clock
	return s
}

// Emit stamps and fans out one Event.
func (s *Stream) Emit(name Name, subject Subject, data Data) {
	envelope := Envelope{
		SchemaVersion: SchemaVersion,
		Event:         string(name),
		OccurredAt:    s.clock().Format(time.RFC3339),
		RunID:         optional(subject.RunID),
		Backend:       optional(string(subject.Backend)),
		Stack:         optional(subject.Stack),
		Service:       optional(subject.Service),
		Actor:         string(s.actor),
		Data:          data,
	}
	s.mutex.Lock()
	sinks := s.sinks
	s.mutex.Unlock()
	for _, sink := range sinks {
		deliver(sink, envelope)
	}
}

func deliver(sink Sink, envelope Envelope) {
	defer func() { _ = recover() }()
	sink.Emit(envelope)
}

// Add attaches another sink.
func (s *Stream) Add(sink Sink) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.sinks = append(s.sinks, sink)
}

// WriterSink writes every Event as one line of JSON. This is the sink
// that is always on: on stderr, so stdout stays the Response envelope's
// alone.
type WriterSink struct {
	writer io.Writer
	mutex  sync.Mutex
}

// NewWriterSink builds the structured stream sink.
func NewWriterSink(writer io.Writer) *WriterSink {
	return &WriterSink{writer: writer}
}

// Emit writes one Event.
func (w *WriterSink) Emit(envelope Envelope) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	encoder := json.NewEncoder(w.writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(envelope)
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
