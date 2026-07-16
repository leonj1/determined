package services_test

import (
	"context"
	"io"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// fakeStatusReporter records every status event the orchestrator emits, in
// order, so tests can assert the exact reporting sequence.
type fakeStatusReporter struct {
	events    []string
	goal      string
	plan      string
	taskSteps []models.TaskStep
}

func (r *fakeStatusReporter) Progress(message string) {
	r.events = append(r.events, "progress: "+message)
}
func (r *fakeStatusReporter) Start() { r.events = append(r.events, "start") }
func (r *fakeStatusReporter) SetGoal(goal string) {
	r.goal = goal
	r.events = append(r.events, "goal")
}
func (r *fakeStatusReporter) SetPlan(plan string) {
	r.plan = plan
	r.events = append(r.events, "plan")
}
func (r *fakeStatusReporter) SetTaskSteps(steps []models.TaskStep) {
	r.taskSteps = steps
	r.events = append(r.events, "task-steps")
}
func (r *fakeStatusReporter) WaitForInput() { r.events = append(r.events, "wait-for-input") }
func (r *fakeStatusReporter) Finish(succeeded bool) {
	if succeeded {
		r.events = append(r.events, "finish: succeeded")
	} else {
		r.events = append(r.events, "finish: failed")
	}
}

func TestSuccessfulPlanReportsFullStatusSequence(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"SQLite"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("QUESTIONS.md", "1. What database?\n")
		case 2:
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "- [x] scaffold the CLI\n  Done when: `go build` passes.\n\n- [ ] add the todo store\n")
		}
		return nil
	}}
	reporter := &fakeStatusReporter{}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready", outcome)
	}
	want := []string{
		"start",
		"progress: writing planning goal",
		"goal",
		"progress: planning project",
		"wait-for-input",
		"progress: planning project",
		"plan",       // refine entry publishes the finished plan
		"task-steps", // ...and the parsed STEPS.md items
		"plan",       // reportFinish re-publishes the final plan text
		"task-steps", // ...and the final step list
		"finish: succeeded",
	}
	if len(reporter.events) != len(want) {
		t.Fatalf("events = %v, want %v", reporter.events, want)
	}
	for i, event := range want {
		if reporter.events[i] != event {
			t.Fatalf("events[%d] = %q, want %q (full: %v)", i, reporter.events[i], event, reporter.events)
		}
	}
	if reporter.goal != "build a todo CLI\n" {
		t.Errorf("reported goal = %q, want the seeded goal", reporter.goal)
	}
	if reporter.plan != "the plan" {
		t.Errorf("reported plan = %q, want PLAN.md contents", reporter.plan)
	}
	wantSteps := []models.TaskStep{
		{Text: "scaffold the CLI", DoneWhen: "`go build` passes.", Completed: true},
		{Text: "add the todo store"},
	}
	if len(reporter.taskSteps) != len(wantSteps) {
		t.Fatalf("task steps = %+v, want %+v", reporter.taskSteps, wantSteps)
	}
	for i, want := range wantSteps {
		if reporter.taskSteps[i] != want {
			t.Errorf("task steps[%d] = %+v, want %+v", i, reporter.taskSteps[i], want)
		}
	}
}

func TestStalledPlanReportsFailure(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{} // writes neither questions nor a plan
	reporter := &fakeStatusReporter{}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled {
		t.Fatalf("outcome = %v, want stalled", outcome)
	}
	last := reporter.events[len(reporter.events)-1]
	if last != "finish: failed" {
		t.Errorf("last event = %q, want finish: failed (all: %v)", last, reporter.events)
	}
}

func TestPlanWithoutReporterRunsTerminalOnly(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	if outcome := o.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready with nil reporter", outcome)
	}
}
