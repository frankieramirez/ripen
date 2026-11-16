package domain

import "testing"

func TestAPhaseOnlyMovesForwardAlongTheTransaction(t *testing.T) {
	allowed := map[[2]Phase]bool{
		{PhaseDeploying, PhaseVerifying}:   true,
		{PhaseDeploying, PhaseRollingBack}: true,
		{PhaseVerifying, PhaseRollingBack}: true,
	}
	phases := []Phase{PhaseDeploying, PhaseVerifying, PhaseRollingBack, PhaseProposing, "paused"}

	for _, from := range phases {
		for _, to := range phases {
			moved := from.MayBecome(to)

			if moved != allowed[[2]Phase{from, to}] {
				t.Errorf("%q.MayBecome(%q) = %v, want %v", from, to, moved, !moved)
			}
		}
	}
}

func TestThePersistedPhaseNamesAreTheOnesAlreadyOnTheWire(t *testing.T) {
	for phase, want := range map[Phase]string{
		PhaseDeploying:   "deploying",
		PhaseVerifying:   "verifying",
		PhaseRollingBack: "rolling_back",
		PhaseProposing:   "proposing",
	} {
		if string(phase) != want {
			t.Errorf("phase = %q, want %q", phase, want)
		}
	}
}
