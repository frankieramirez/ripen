package app

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/frankieramirez/ripen/internal/domain"
	"github.com/frankieramirez/ripen/internal/response"
	"github.com/frankieramirez/ripen/internal/state"
)

// ErrInvalidAuditRequest identifies invalid audit pagination parameters.
var ErrInvalidAuditRequest = errors.New("invalid audit request")

// AuditRequest selects and pages recorded attempts using transport input values.
// Empty pagination values select the newest 50 attempts.
type AuditRequest struct {
	Limit   string
	Cursor  string
	RunID   string
	Backend string
	Stack   string
	Service string
	Result  string
}

// Audit answers `ripen audit` from the attempts table — the record of
// what Ripen did, never the Event stream.
func (a *App) Audit(request AuditRequest) (response.Audit, error) {
	filter, err := request.filter()
	if err != nil {
		return response.Audit{}, err
	}
	filter.Limit++
	attempts, err := a.Store.AuditPage(filter)
	if err != nil {
		return response.Audit{}, err
	}
	audit := response.Audit{Attempts: []response.Attempt{}}
	if len(attempts) == filter.Limit {
		attempts = attempts[:filter.Limit-1]
		cursor := fmt.Sprintf("%d", attempts[len(attempts)-1].ID)
		audit.NextCursor = &cursor
	}
	for _, attempt := range attempts {
		audit.Attempts = append(audit.Attempts, response.Attempt{
			Identity:    identity(attempt.Key),
			RunID:       attempt.RunID,
			Actor:       string(attempt.Actor),
			Result:      string(attempt.Result),
			Detail:      attempt.Detail,
			OldDigest:   response.Optional(attempt.OldDigest),
			NewDigest:   response.Optional(attempt.NewDigest),
			AttemptedAt: response.Stamp(attempt.AttemptedAt),
		})
	}
	return audit, nil
}

func (request AuditRequest) filter() (state.AuditFilter, error) {
	limit := 50
	if request.Limit != "" {
		parsed, err := strconv.Atoi(request.Limit)
		if err != nil || parsed == math.MaxInt {
			return state.AuditFilter{}, fmt.Errorf("%w: limit must be a decimal integer with room for pagination", ErrInvalidAuditRequest)
		}
		if parsed > 0 {
			limit = parsed
		}
	}
	var cursor int64
	if request.Cursor != "" {
		parsed, err := strconv.ParseUint(request.Cursor, 10, 63)
		if err != nil || parsed == 0 {
			return state.AuditFilter{}, fmt.Errorf("%w: cursor must be a positive decimal integer from next_cursor", ErrInvalidAuditRequest)
		}
		cursor = int64(parsed)
	}
	return state.AuditFilter{
		Limit:   limit,
		Cursor:  cursor,
		RunID:   request.RunID,
		Backend: domain.Backend(request.Backend),
		Stack:   request.Stack,
		Service: request.Service,
		Result:  domain.ResultCode(request.Result),
	}, nil
}
