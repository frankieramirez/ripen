package cli

import (
	"io"
	"strconv"
	"strings"

	"github.com/frankieramirez/ripen/internal/response"
)

func writePretty(writer io.Writer, envelope response.Envelope) error {
	var printed printer
	if !envelope.OK {
		prettyFailure(&printed, envelope)
	} else if !prettyData(&printed, envelope.Data) {
		return response.Write(writer, envelope)
	}
	_, err := io.WriteString(writer, printed.b.String())
	return err
}

type printer struct {
	b strings.Builder
}

func (p *printer) kv(indent int, key, value string) {
	p.b.WriteString(strings.Repeat("  ", indent))
	p.b.WriteString(key)
	p.b.WriteString(": ")
	p.b.WriteString(value)
	p.b.WriteByte('\n')
}

func (p *printer) section(indent int, key string) {
	p.b.WriteString(strings.Repeat("  ", indent))
	p.b.WriteString(key)
	p.b.WriteString(":\n")
}

func (p *printer) item(indent int, value string) {
	p.b.WriteString(strings.Repeat("  ", indent))
	p.b.WriteString("- ")
	p.b.WriteString(value)
	p.b.WriteByte('\n')
}

func prettyFailure(p *printer, envelope response.Envelope) {
	p.kv(0, "ok", "false")
	p.kv(0, "command", envelope.Command)
	if envelope.Error == nil {
		return
	}
	p.kv(0, "error", string(envelope.Error.Code))
	p.kv(1, "message", envelope.Error.Message)
	p.kv(1, "retryable", boolText(envelope.Error.Retryable))
}

func prettyData(p *printer, data any) bool {
	switch typed := data.(type) {
	case response.Status:
		prettyStatus(p, typed)
	case response.Candidates:
		prettyCandidates(p, typed)
	case response.Audit:
		prettyAudit(p, typed)
	case response.Explain:
		prettyExplain(p, typed)
	default:
		return false
	}
	return true
}

func prettyStatus(p *printer, status response.Status) {
	p.kv(0, "mode", status.EffectivePolicy.Mode)
	prettyBreaker(p, 0, status.Breaker)
	p.kv(0, "lease", active(status.Lease.Active))
	p.section(0, "notifier")
	p.kv(1, "last success", orNone(status.Notifier.LastSuccessAt))
	p.kv(1, "consecutive failures", strconv.Itoa(status.Notifier.ConsecutiveFailures))
	p.kv(1, "dropped since start", strconv.Itoa(status.Notifier.DroppedSinceStart))
	prettyServices(p, status.Services)
	prettyEffectivePolicy(p, status.EffectivePolicy)
	prettyVersions(p, status.Versions)
}

func prettyServices(p *printer, services []response.Service) {
	if len(services) == 0 {
		p.kv(0, "services", "none")
		return
	}
	p.section(0, "services")
	for _, service := range services {
		p.section(1, identityName(service.Identity))
		p.kv(2, "enabled", boolText(service.Enabled))
		p.kv(2, "auto apply", boolText(service.AutoApply))
		p.kv(2, "baseline", orNone(service.Baseline))
		prettyObservation(p, 2, service.Candidate)
		prettyProposal(p, 2, service.PendingProposal)
		prettyLastResult(p, 2, service.LastResult)
	}
}

func prettyEffectivePolicy(p *printer, policy response.EffectivePolicy) {
	p.section(0, "effective policy")
	p.kv(1, "max updates per run", strconv.Itoa(policy.MaxUpdatesPerRun))
	p.kv(1, "candidate min age", seconds(policy.CandidateMinAgeSeconds))
	p.kv(1, "verification timeout", seconds(policy.VerificationTimeoutSeconds))
	p.kv(1, "lease ttl", seconds(policy.LeaseTTLSeconds))
	p.kv(1, "check interval", seconds(policy.CheckIntervalSeconds))
	p.kv(1, "state file", policy.StateFile)
	p.kv(1, "backends", joinWords(policy.Backends))
	p.kv(1, "stacks", strconv.Itoa(policy.StackCount))
	p.kv(1, "proposals", boolText(policy.ProposalsConfigured))
	p.kv(1, "notifier", boolText(policy.NotifierConfigured))
}

func prettyVersions(p *printer, versions response.Versions) {
	p.section(0, "versions")
	p.kv(1, "ripen", versions.Ripen)
	p.kv(1, "commit", versions.Commit)
	p.kv(1, "built", versions.BuiltAt)
	p.kv(1, "response schema", strconv.Itoa(versions.ResponseSchema))
	p.kv(1, "event schema", strconv.Itoa(versions.EventSchema))
	p.kv(1, "state schema", strconv.Itoa(versions.StateSchema))
}

func prettyCandidates(p *printer, payload response.Candidates) {
	if len(payload.Candidates) == 0 {
		p.kv(0, "candidates", "none")
		return
	}
	p.section(0, "candidates")
	for _, candidate := range payload.Candidates {
		p.kv(1, identityName(candidate.Identity), candidate.Digest)
		p.kv(2, "first seen", candidate.FirstSeen)
		p.kv(2, "last seen", candidate.LastSeen)
		p.kv(2, "observations", strconv.Itoa(candidate.Observations))
		p.kv(2, "mature", boolText(candidate.Mature))
		p.kv(2, "mature at", candidate.MatureAt)
	}
}

func prettyAudit(p *printer, payload response.Audit) {
	if len(payload.Attempts) == 0 {
		p.kv(0, "attempts", "none")
	} else {
		p.section(0, "attempts")
		for _, attempt := range payload.Attempts {
			p.kv(1, identityName(attempt.Identity), attempt.Result)
			p.kv(2, "attempted at", attempt.AttemptedAt)
			p.kv(2, "actor", attempt.Actor)
			p.kv(2, "run id", attempt.RunID)
			p.kv(2, "detail", attempt.Detail)
			p.kv(2, "old digest", orNone(attempt.OldDigest))
			p.kv(2, "new digest", orNone(attempt.NewDigest))
		}
	}
	p.kv(0, "next cursor", orNone(payload.NextCursor))
}

func prettyExplain(p *printer, payload response.Explain) {
	p.kv(0, "stack", payload.Stack)
	p.kv(0, "backend", payload.Backend)
	p.kv(0, "enabled", boolText(payload.Enabled))
	p.kv(0, "excluded", boolText(payload.Excluded))
	p.kv(0, "mode", payload.Mode)
	prettyBreaker(p, 0, payload.Breaker)
	p.kv(0, "git path", orNone(payload.GitPath))
	p.kv(0, "expected services", joinWords(payload.ExpectedServices))
	if len(payload.Services) == 0 {
		p.kv(0, "services", "none")
		return
	}
	p.section(0, "services")
	for _, service := range payload.Services {
		p.section(1, identityName(service.Identity))
		p.kv(2, "enabled", boolText(service.Enabled))
		p.kv(2, "auto apply", boolText(service.AutoApply))
		p.kv(2, "health", service.Health.Type)
		p.kv(3, "target", service.Health.Target)
		p.kv(3, "accepted status", joinInts(service.Health.AcceptedStatus))
		p.kv(2, "baseline", orNone(service.Baseline))
		prettyObservation(p, 2, service.Candidate)
		prettyProposal(p, 2, service.PendingProposal)
		prettyBlockers(p, 2, service.Blockers)
	}
}

func prettyBlockers(p *printer, indent int, blockers []string) {
	if len(blockers) == 0 {
		p.kv(indent, "blockers", "none")
		return
	}
	p.section(indent, "blockers")
	for _, blocker := range blockers {
		p.item(indent+1, blocker)
	}
}

func prettyBreaker(p *printer, indent int, breaker response.Breaker) {
	state := "closed"
	if breaker.Open {
		state = "open"
	}
	p.kv(indent, "circuit breaker", state)
	if breaker.Reason != nil {
		p.kv(indent+1, "reason", *breaker.Reason)
	}
}

func prettyObservation(p *printer, indent int, observed *response.Observation) {
	if observed == nil {
		p.kv(indent, "candidate", "none")
		return
	}
	p.kv(indent, "candidate", observed.Digest)
	p.kv(indent+1, "first seen", observed.FirstSeen)
	p.kv(indent+1, "last seen", observed.LastSeen)
	p.kv(indent+1, "observations", strconv.Itoa(observed.Observations))
	p.kv(indent+1, "mature", boolText(observed.Mature))
	p.kv(indent+1, "mature at", observed.MatureAt)
}

func prettyProposal(p *printer, indent int, proposal *response.Proposal) {
	if proposal == nil {
		p.kv(indent, "pending proposal", "none")
		return
	}
	p.kv(indent, "pending proposal", proposal.URL)
	p.kv(indent+1, "digest", proposal.Digest)
	p.kv(indent+1, "proposed at", proposal.ProposedAt)
}

func prettyLastResult(p *printer, indent int, result *response.AttemptSummary) {
	if result == nil {
		p.kv(indent, "last result", "none")
		return
	}
	p.kv(indent, "last result", result.Result)
	p.kv(indent+1, "run id", result.RunID)
	p.kv(indent+1, "actor", result.Actor)
	p.kv(indent+1, "detail", result.Detail)
	p.kv(indent+1, "attempted at", result.AttemptedAt)
}

func identityName(id response.Identity) string {
	if id.Service == nil || *id.Service == "" {
		return id.Backend + "/" + id.Stack
	}
	return id.Backend + "/" + id.Stack + "/" + *id.Service
}

func orNone(value *string) string {
	if value == nil || *value == "" {
		return "none"
	}
	return *value
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func active(value bool) string {
	if value {
		return "active"
	}
	return "inactive"
}

func seconds(value int) string {
	return strconv.Itoa(value) + "s"
}

func joinWords(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}
