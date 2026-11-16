package domain

import "slices"

// Phase is where an unfinished Transaction stands. A direct apply begins
// deploying and a Proposal begins proposing; the only moves after that
// are the ones MayBecome allows.
type Phase string

// The persisted Transaction phases.
const (
	PhaseDeploying   Phase = "deploying"
	PhaseVerifying   Phase = "verifying"
	PhaseRollingBack Phase = "rolling_back"
	PhaseProposing   Phase = "proposing"
)

var phaseSuccessors = map[Phase][]Phase{
	PhaseDeploying: {PhaseVerifying, PhaseRollingBack},
	PhaseVerifying: {PhaseRollingBack},
}

// MayBecome reports whether a Transaction in this phase may move to next.
// A failed deploy rolls back without verifying; rolling back and proposing
// are where a Transaction ends.
func (p Phase) MayBecome(next Phase) bool {
	return slices.Contains(phaseSuccessors[p], next)
}
