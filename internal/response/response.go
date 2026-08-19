// Package response is Ripen's wire surface: the Response envelope every
// verb answers in, and the typed payloads inside it. JSON is not a mode
// here, it is the output — there is no --json flag and no second,
// human-only shape that could drift from this one.
//
// Two rules hold everywhere in this package. Identity is always the
// three parts backend/stack/service, never the state store's internal
// key. And a value that can be absent is null, never an empty string
// pretending to be a value.
package response

import (
	"encoding/json"
	"io"
	"time"
)

// SchemaVersion is the Response envelope's version. It moves
// independently of the Event envelope's, and additive changes — a new
// field, a new result code — do not bump it.
const SchemaVersion = 1

// Envelope wraps every answer, success or failure.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	OccurredAt    string `json:"occurred_at"`
	OK            bool   `json:"ok"`
	Data          any    `json:"data,omitempty"`
	Error         *Error `json:"error,omitempty"`
}

// Code is the closed set of failure codes. Receivers must ignore codes
// they do not know.
type Code string

// The v1 error codes.
const (
	CodeUsage               Code = "usage"
	CodeConfigInvalid       Code = "config_invalid"
	CodeNotFound            Code = "not_found"
	CodePreconditionFailed  Code = "precondition_failed"
	CodeBreakerOpen         Code = "breaker_open"
	CodeStateLocked         Code = "state_locked"
	CodeBackendUnavailable  Code = "backend_unavailable"
	CodeRegistryUnavailable Code = "registry_unavailable"
	CodeInternal            Code = "internal"
)

// retryable marks the codes where trying the same call again later can
// succeed without anyone changing anything.
var retryable = map[Code]bool{
	CodeStateLocked:         true,
	CodeBackendUnavailable:  true,
	CodeRegistryUnavailable: true,
}

// Error is a failure in the same envelope as a success.
type Error struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Fail builds an error envelope.
func Fail(command string, at time.Time, code Code, message string) Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Command:       command,
		OccurredAt:    Stamp(at),
		OK:            false,
		Error:         &Error{Code: code, Message: message, Retryable: retryable[code]},
	}
}

// Succeed builds a success envelope around one payload.
func Succeed(command string, at time.Time, data any) Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Command:       command,
		OccurredAt:    Stamp(at),
		OK:            true,
		Data:          data,
	}
}

// Write encodes an envelope as one line of JSON.
func Write(writer io.Writer, envelope Envelope) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

// Stamp renders a time the way every timestamp on the wire is rendered.
func Stamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

// Optional renders a value that may be absent: empty means null.
func Optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// --- payloads ---

// Identity names one Service. Service is null for a stack-level policy.
type Identity struct {
	Backend string  `json:"backend"`
	Stack   string  `json:"stack"`
	Service *string `json:"service"`
}

// Observation is a Candidate as the state store holds it.
type Observation struct {
	Digest       string `json:"digest"`
	FirstSeen    string `json:"first_seen"`
	LastSeen     string `json:"last_seen"`
	Observations int    `json:"observations"`
	Mature       bool   `json:"mature"`
	MatureAt     string `json:"mature_at"`
}

// Proposal is an open digest-pin Proposal.
type Proposal struct {
	Digest     string `json:"digest"`
	URL        string `json:"url"`
	ProposedAt string `json:"proposed_at"`
}

// Health is one configured functional health check.
type Health struct {
	Type           string `json:"type"`
	Target         string `json:"target"`
	AcceptedStatus []int  `json:"accepted_status"`
}

// AttemptSummary is the last thing that happened to a Service.
type AttemptSummary struct {
	RunID       string `json:"run_id"`
	Actor       string `json:"actor"`
	Result      string `json:"result"`
	Detail      string `json:"detail"`
	AttemptedAt string `json:"attempted_at"`
}

// Service is one configured Service and everything durable about it.
// A configured Service that has never been observed still appears, with
// a null baseline: status is driven by the policy, not by the state.
type Service struct {
	Identity
	Enabled         bool            `json:"enabled"`
	AutoApply       bool            `json:"auto_apply"`
	Baseline        *string         `json:"baseline"`
	Candidate       *Observation    `json:"candidate"`
	PendingProposal *Proposal       `json:"pending_proposal"`
	LastResult      *AttemptSummary `json:"last_result"`
}

// Breaker is the Circuit breaker's state.
type Breaker struct {
	Open   bool    `json:"open"`
	Reason *string `json:"reason"`
}

// Lease says whether a run is in flight.
type Lease struct {
	Active bool `json:"active"`
}

// Versions carries every version a caller might need to reason about.
type Versions struct {
	Ripen          string `json:"ripen"`
	Commit         string `json:"commit"`
	BuiltAt        string `json:"built_at"`
	ResponseSchema int    `json:"response_schema"`
	EventSchema    int    `json:"event_schema"`
	StateSchema    int    `json:"state_schema"`
}

// EffectivePolicy is what Ripen is actually running with, after
// defaults — not what the file said.
type EffectivePolicy struct {
	Mode                       string   `json:"mode"`
	MaxUpdatesPerRun           int      `json:"max_updates_per_run"`
	CandidateMinAgeSeconds     int      `json:"candidate_min_age_seconds"`
	VerificationTimeoutSeconds int      `json:"verification_timeout_seconds"`
	LeaseTTLSeconds            int      `json:"lease_ttl_seconds"`
	CheckIntervalSeconds       int      `json:"check_interval_seconds"`
	StateFile                  string   `json:"state_file"`
	Backends                   []string `json:"backends"`
	StackCount                 int      `json:"stack_count"`
	ProposalsConfigured        bool     `json:"proposals_configured"`
	NotifierConfigured         bool     `json:"notifier_configured"`
}

// NotifierHealth is how the outbound Notifier is doing. Delivery is
// at-most-once and fail-open, so this is the only way to know.
type NotifierHealth struct {
	LastSuccessAt       *string `json:"last_success_at"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	DroppedSinceStart   int     `json:"dropped_since_start"`
}

// NotifyTest is the answer to `ripen notify test`.
type NotifyTest struct {
	Delivered bool           `json:"delivered"`
	Detail    string         `json:"detail"`
	Health    NotifierHealth `json:"health"`
}

// Status is the answer to `ripen status`.
type Status struct {
	Breaker         Breaker         `json:"breaker"`
	Lease           Lease           `json:"lease"`
	Notifier        NotifierHealth  `json:"notifier"`
	Services        []Service       `json:"services"`
	Versions        Versions        `json:"versions"`
	EffectivePolicy EffectivePolicy `json:"effective_policy"`
}

// Candidate is one observed Candidate with its identity.
type Candidate struct {
	Identity
	Observation
}

// Candidates is the answer to `ripen candidates`.
type Candidates struct {
	Candidates []Candidate `json:"candidates"`
}

// Attempt is one audit-trail row.
type Attempt struct {
	Identity
	RunID       string  `json:"run_id"`
	Actor       string  `json:"actor"`
	Result      string  `json:"result"`
	Detail      string  `json:"detail"`
	OldDigest   *string `json:"old_digest"`
	NewDigest   *string `json:"new_digest"`
	AttemptedAt string  `json:"attempted_at"`
}

// Audit is the answer to `ripen audit`, newest first. NextCursor is null
// when the page is the last one.
type Audit struct {
	Attempts   []Attempt `json:"attempts"`
	NextCursor *string   `json:"next_cursor"`
}

// ExplainService is one Service's reasoning.
type ExplainService struct {
	Identity
	Enabled         bool         `json:"enabled"`
	AutoApply       bool         `json:"auto_apply"`
	Health          Health       `json:"health"`
	Baseline        *string      `json:"baseline"`
	Candidate       *Observation `json:"candidate"`
	PendingProposal *Proposal    `json:"pending_proposal"`
	// Blockers is what stands between this Service and an apply right
	// now, in the order Ripen would hit them. Empty means it would act.
	Blockers []string `json:"blockers"`
}

// Explain is the answer to `ripen explain <stack>`: why the next run
// would, or would not, act on this stack.
type Explain struct {
	Backend          string           `json:"backend"`
	Stack            string           `json:"stack"`
	Enabled          bool             `json:"enabled"`
	Excluded         bool             `json:"excluded"`
	GitPath          *string          `json:"git_path"`
	ExpectedServices []string         `json:"expected_services"`
	Breaker          Breaker          `json:"breaker"`
	Mode             string           `json:"mode"`
	Services         []ExplainService `json:"services"`
}

// RunResult is one Service's outcome in a run. Backend is null for a
// run-level result, where Stack is "*".
type RunResult struct {
	Backend *string `json:"backend"`
	Stack   string  `json:"stack"`
	Service *string `json:"service"`
	Result  string  `json:"result"`
	Detail  string  `json:"detail"`
	Digest  *string `json:"digest"`
}

// Run is the answer to `ripen run`.
type Run struct {
	RunID          string      `json:"run_id"`
	Mode           string      `json:"mode"`
	Actor          string      `json:"actor"`
	StartedAt      string      `json:"started_at"`
	FinishedAt     string      `json:"finished_at"`
	UpdatesApplied int         `json:"updates_applied"`
	BreakerOpen    bool        `json:"breaker_open"`
	Results        []RunResult `json:"results"`
}

// Proposed is the answer to `ripen propose <stack>`.
type Proposed struct {
	Identity
	Digest  string `json:"digest"`
	URL     string `json:"url"`
	Created bool   `json:"created"`
	RunID   string `json:"run_id"`
	Detail  string `json:"detail"`
}

// Acknowledged is the answer to the verbs that change one thing and
// report the state afterwards.
type Acknowledged struct {
	Changed bool    `json:"changed"`
	Reason  string  `json:"reason"`
	Breaker Breaker `json:"breaker"`
	Detail  string  `json:"detail"`
}

// SchemaSet is the answer to `ripen schema`.
type SchemaSet struct {
	SchemaVersion int            `json:"schema_version"`
	Schemas       map[string]any `json:"schemas"`
}

// Version is the answer to `ripen version`.
type Version struct {
	Versions
}
