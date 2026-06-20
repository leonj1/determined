package models_test

import (
	"testing"

	"determined/src/models"
)

func TestOnlyCompletionExitsCleanly(t *testing.T) {
	cases := []struct {
		outcome  models.Outcome
		wantCode int
	}{
		{models.OutcomeStopped, 0},
		{models.OutcomeDroidFailed, 1},
		{models.OutcomeBudgetExceeded, 1},
		{models.OutcomeInterrupted, 1},
	}
	for _, c := range cases {
		if got := c.outcome.ExitCode(); got != c.wantCode {
			t.Errorf("%q: expected exit %d, got %d", c.outcome, c.wantCode, got)
		}
		if c.outcome.String() == "" {
			t.Errorf("outcome %d should describe itself for the user", int(c.outcome))
		}
	}
}
