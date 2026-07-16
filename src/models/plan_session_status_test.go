package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPlanSessionStatusJSONRoundTrip(t *testing.T) {
	start := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	status := PlanSessionStatus{
		Git:       GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"},
		Goal:      "build a todo CLI",
		Plan:      "# Plan\n\n1. do things",
		Phase:     PlanPhaseRunning,
		Steps:     []PlanStep{{At: start, Message: "writing planning goal"}},
		StartedAt: start,
	}

	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded PlanSessionStatus
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Git != status.Git {
		t.Errorf("git = %+v, want %+v", decoded.Git, status.Git)
	}
	if decoded.Goal != status.Goal || decoded.Plan != status.Plan {
		t.Errorf("goal/plan lost in round trip: %+v", decoded)
	}
	if decoded.Phase != PlanPhaseRunning {
		t.Errorf("phase = %q, want %q", decoded.Phase, PlanPhaseRunning)
	}
	if len(decoded.Steps) != 1 || decoded.Steps[0].Message != "writing planning goal" {
		t.Errorf("steps = %+v, want one 'writing planning goal' step", decoded.Steps)
	}
	if !decoded.StartedAt.Equal(start) {
		t.Errorf("startedAt = %v, want %v", decoded.StartedAt, start)
	}
}

func TestPlanSessionStatusDuration(t *testing.T) {
	start := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	now := start.Add(3 * time.Minute)
	end := start.Add(5 * time.Minute)

	unstarted := PlanSessionStatus{}
	if got := unstarted.Duration(now); got != 0 {
		t.Errorf("unstarted duration = %v, want 0", got)
	}

	running := PlanSessionStatus{StartedAt: start}
	if got := running.Duration(now); got != 3*time.Minute {
		t.Errorf("running duration = %v, want 3m", got)
	}

	ended := PlanSessionStatus{StartedAt: start, EndedAt: end}
	if got := ended.Duration(now.Add(time.Hour)); got != 5*time.Minute {
		t.Errorf("ended duration = %v, want 5m", got)
	}
}

func TestPlanSessionStatusWithStepDoesNotMutate(t *testing.T) {
	at := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	original := PlanSessionStatus{Steps: []PlanStep{{At: at, Message: "first"}}}

	updated := original.WithStep(PlanStep{At: at.Add(time.Minute), Message: "second"})

	if len(original.Steps) != 1 {
		t.Errorf("original mutated: %+v", original.Steps)
	}
	if len(updated.Steps) != 2 || updated.Steps[1].Message != "second" {
		t.Errorf("updated steps = %+v, want [first second]", updated.Steps)
	}
}
