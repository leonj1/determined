package tests

import (
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// steppingClock is a controllable clock that advances on demand.
type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time               { return c.now }
func (c *steppingClock) advance(d time.Duration)      { c.now = c.now.Add(d) }
func newSteppingClock(start time.Time) *steppingClock { return &steppingClock{now: start} }

func planStart() time.Time { return time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC) }

func TestPlanStatusServiceInitialSnapshotCarriesGitContext(t *testing.T) {
	git := models.GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"}
	service := services.NewPlanStatusService(newSteppingClock(planStart()), git)

	snapshot := service.Snapshot()
	if snapshot.Git != git {
		t.Errorf("git = %+v, want %+v", snapshot.Git, git)
	}
	if snapshot.Phase != models.PlanPhaseRunning {
		t.Errorf("phase = %q, want running", snapshot.Phase)
	}
	if len(snapshot.Steps) != 0 {
		t.Errorf("steps = %+v, want empty", snapshot.Steps)
	}
}

func TestPlanStatusServiceStepsAreOrderedAndTimestamped(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{Remote: "no remote", Branch: "master"})

	service.AddStep("writing planning goal")
	clock.advance(time.Minute)
	service.AddStep("planning project")

	steps := service.Snapshot().Steps
	if len(steps) != 2 {
		t.Fatalf("steps = %+v, want 2 entries", steps)
	}
	if steps[0].Message != "writing planning goal" || steps[1].Message != "planning project" {
		t.Errorf("step order wrong: %+v", steps)
	}
	if !steps[0].At.Equal(planStart()) || !steps[1].At.Equal(planStart().Add(time.Minute)) {
		t.Errorf("timestamps wrong: %+v", steps)
	}
}

func TestPlanStatusServiceLateSubscriberReceivesCurrentState(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.SetGoal("build a todo CLI")
	service.AddStep("planning project")

	snapshots, cancel := service.Subscribe()
	defer cancel()

	first := <-snapshots
	if first.Goal != "build a todo CLI" {
		t.Errorf("late subscriber goal = %q, want the current goal", first.Goal)
	}
	if len(first.Steps) != 1 {
		t.Errorf("late subscriber steps = %+v, want the step so far", first.Steps)
	}
}

func TestPlanStatusServiceBroadcastsUpdatesToSubscribers(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	service.SetPlan("# Plan")

	updated := <-snapshots
	if updated.Plan != "# Plan" {
		t.Errorf("broadcast plan = %q, want %q", updated.Plan, "# Plan")
	}
}

func TestPlanStatusServiceBroadcastsTests(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	service.SetTests("### Test 1: journey")

	updated := <-snapshots
	if updated.Tests != "### Test 1: journey" {
		t.Errorf("broadcast tests = %q, want %q", updated.Tests, "### Test 1: journey")
	}
}

func TestPlanStatusServiceBroadcastsTaskSteps(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	steps := []models.TaskStep{
		{Text: "scaffold the CLI", DoneWhen: "`go build` passes.", Completed: true},
		{Text: "add the todo store"},
	}
	service.SetTaskSteps(steps)

	updated := <-snapshots
	if len(updated.TaskSteps) != 2 {
		t.Fatalf("task steps = %+v, want 2 entries", updated.TaskSteps)
	}
	for i, want := range steps {
		if updated.TaskSteps[i] != want {
			t.Errorf("task steps[%d] = %+v, want %+v", i, updated.TaskSteps[i], want)
		}
	}
}

func TestPlanStatusServiceFinishRecordsTimingAndPhase(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{})
	service.Start()
	clock.advance(5 * time.Minute)
	service.Finish(true)

	snapshot := service.Snapshot()
	if snapshot.Phase != models.PlanPhaseSucceeded {
		t.Errorf("phase = %q, want succeeded", snapshot.Phase)
	}
	if !snapshot.StartedAt.Equal(planStart()) {
		t.Errorf("startedAt = %v, want %v", snapshot.StartedAt, planStart())
	}
	if !snapshot.EndedAt.Equal(planStart().Add(5 * time.Minute)) {
		t.Errorf("endedAt = %v, want start+5m", snapshot.EndedAt)
	}
	if got := snapshot.Duration(clock.Now()); got != 5*time.Minute {
		t.Errorf("duration = %v, want 5m", got)
	}
}

func TestPlanStatusServiceFinishFailureMarksFailedPhase(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.Start()
	service.Finish(false)

	if phase := service.Snapshot().Phase; phase != models.PlanPhaseFailed {
		t.Errorf("phase = %q, want failed", phase)
	}
}

func TestPlanStatusServiceLogEntriesAccumulateStreamedOutput(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{})

	service.BeginLogEntry("planning project")
	service.AppendLogOutput("first line\n")
	service.AppendLogOutput("second line\n")
	clock.advance(time.Minute)
	service.BeginLogEntry("assessing plan")
	service.AppendLogOutput("findings written\n")

	log := service.Snapshot().Log
	if len(log) != 2 {
		t.Fatalf("log = %+v, want 2 entries", log)
	}
	first := models.LogEntry{At: planStart(), Message: "planning project", Body: "first line\nsecond line\n"}
	second := models.LogEntry{At: planStart().Add(time.Minute), Message: "assessing plan", Body: "findings written\n"}
	if log[0] != first {
		t.Errorf("log[0] = %+v, want %+v", log[0], first)
	}
	if log[1] != second {
		t.Errorf("log[1] = %+v, want %+v", log[1], second)
	}
}

func TestPlanStatusServiceLogOutputWithoutEntryIsDropped(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})

	service.AppendLogOutput("stray output\n")

	if log := service.Snapshot().Log; len(log) != 0 {
		t.Errorf("log = %+v, want empty when no entry is open", log)
	}
}

func TestPlanStatusServiceWaitForInputSetsFlagAndVisibleStep(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.WaitForInput()

	snapshot := service.Snapshot()
	if !snapshot.WaitingForInput {
		t.Error("waitingForInput = false, want true")
	}
	if len(snapshot.Steps) != 1 || snapshot.Steps[0].Message != "waiting for input on the terminal" {
		t.Errorf("steps = %+v, want a waiting step", snapshot.Steps)
	}

	service.AddStep("planning project")
	if service.Snapshot().WaitingForInput {
		t.Error("waitingForInput still true after next step")
	}
}
