package services_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// controlRunner drives task-control scenarios: its script can fire the status
// page's Skip or Stop against the service mid-invocation, then block until the
// resulting cancellation kills the "child".
type controlRunner struct {
	calls       int
	invocations []models.Invocation
	script      func(call int, ctx context.Context) error
}

func (r *controlRunner) Run(ctx context.Context, inv models.Invocation, _ io.Writer) error {
	r.calls++
	r.invocations = append(r.invocations, inv)
	return r.script(r.calls, ctx)
}

func (r *controlRunner) prompt(call int) string {
	return r.invocations[call-1].Args[1]
}

// abortAs simulates a real child killed by context cancellation: request the
// action, wait for the cancel, and surface the context error.
func abortAs(request func() bool, t *testing.T) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if !request() {
			t.Error("expected an active task to act on")
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func newControlledOrchestrator(runner services.CommandRunner, fs *fakeFileStore, cfg models.Config) (*services.Orchestrator, *services.PlanStatusService) {
	control := services.NewPlanStatusService(&fakeClock{now: time.Now()}, models.GitContext{}, models.ToolIdentity{})
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(control).
		WithTaskControl(control)
	return o, control
}

func TestSkipFromThePageMarksTheStepDoneAndTheRunMovesOn(t *testing.T) {
	fs := stepsFileStore()
	var control *services.PlanStatusService
	runner := &controlRunner{}
	runner.script = func(call int, ctx context.Context) error {
		switch call {
		case 1: // executing step 1: the user clicks Skip
			return abortAs(control.RequestSkipActiveTask, t)(ctx)
		case 2: // executing step 2 completes it
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the docs update
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	o, service := newControlledOrchestrator(runner, fs, config(0))
	control = service

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want clean completion", outcome)
	}
	steps, _ := fs.Read("STEPS.md")
	if strings.Contains(steps, "[ ]") {
		t.Fatalf("expected every step checked after the skip, got:\n%s", steps)
	}
	notes, err := fs.Read("NOTES.md")
	if err != nil || !strings.Contains(notes, "Step 1 was skipped by the user") {
		t.Fatalf("expected the skip recorded in NOTES.md, got %q (%v)", notes, err)
	}
	// Five invocations: skipped step 1, step 2, docs, audit, and the
	// explanation the attached status page requests after a clean run.
	if runner.calls != 5 {
		t.Fatalf("runner calls = %d, want 5 (skip must not burn retry attempts)", runner.calls)
	}
}

func TestSkippedStepIsNotVerified(t *testing.T) {
	fs := stepsFileStore()
	cfg := config(0)
	cfg.Verify = true
	var control *services.PlanStatusService
	runner := &controlRunner{}
	runner.script = func(call int, ctx context.Context) error {
		switch call {
		case 1: // executing step 1: the user clicks Skip
			return abortAs(control.RequestSkipActiveTask, t)(ctx)
		case 2: // executing step 2 completes it
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // docs after step 2's simplicity and correctness reviews
		case 6: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	o, service := newControlledOrchestrator(runner, fs, cfg)
	control = service

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want clean completion", outcome)
	}
	for call := 1; call <= runner.calls; call++ {
		if strings.Contains(runner.prompt(call), "Step 1 claims complete") {
			t.Fatalf("call %d reviewed the user-skipped step 1: %q", call, runner.prompt(call))
		}
	}
	reviewed := 0
	for call := 1; call <= runner.calls; call++ {
		if strings.Contains(runner.prompt(call), "Step 2 claims complete") {
			reviewed++
		}
	}
	if reviewed != 2 {
		t.Fatalf("step 2 review invocations = %d, want simplicity plus correctness", reviewed)
	}
}

func TestStopFromThePageEndsTheRun(t *testing.T) {
	fs := stepsFileStore()
	var control *services.PlanStatusService
	runner := &controlRunner{}
	runner.script = func(call int, ctx context.Context) error {
		return abortAs(control.RequestStopRun, t)(ctx)
	}
	o, service := newControlledOrchestrator(runner, fs, config(0))
	control = service

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeUserStopped {
		t.Fatalf("outcome = %v, want %v", outcome, models.OutcomeUserStopped)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1 (stop must not retry)", runner.calls)
	}
	steps, _ := fs.Read("STEPS.md")
	if !strings.Contains(steps, "[ ] 1.") {
		t.Fatalf("a stopped run must not check steps, got:\n%s", steps)
	}
	snapshot := service.Snapshot()
	if snapshot.ExecPhase != models.ExecPhaseFailed {
		t.Fatalf("exec phase = %q, want failed", snapshot.ExecPhase)
	}
	if !strings.Contains(snapshot.ExecStopReason, "status page") {
		t.Fatalf("stop reason = %q, want it to name the status page", snapshot.ExecStopReason)
	}
	if snapshot.TaskControlAvailable {
		t.Fatal("task control must not stay advertised after the run ends")
	}
}

func TestSkipWaivesAReviewerInvocation(t *testing.T) {
	fs := stepsFileStore()
	cfg := config(0)
	cfg.Verify = true
	var control *services.PlanStatusService
	runner := &controlRunner{}
	runner.script = func(call int, ctx context.Context) error {
		switch call {
		case 1: // executing step 1 completes it
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // checking simplicity of step 1: the user clicks Skip
			return abortAs(control.RequestSkipActiveTask, t)(ctx)
		case 3: // executing step 2 completes it
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 6: // docs after step 2's reviews
		case 7: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	o, service := newControlledOrchestrator(runner, fs, cfg)
	control = service

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want clean completion", outcome)
	}
	steps, _ := fs.Read("STEPS.md")
	if strings.Contains(steps, "[ ]") {
		t.Fatalf("a skipped review must leave the step checked, got:\n%s", steps)
	}
}

func TestPlanningStopFromThePageEndsTheSession(t *testing.T) {
	fs := newFakeFileStore()
	var control *services.PlanStatusService
	runner := &controlRunner{}
	runner.script = func(call int, ctx context.Context) error {
		return abortAs(control.RequestStopRun, t)(ctx)
	}
	control = services.NewPlanStatusService(&fakeClock{now: time.Now()}, models.GitContext{}, models.ToolIdentity{})
	o := services.NewPlanOrchestrator(
		runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0),
	).WithStatusReporter(control).WithTaskControl(control)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeUserStopped {
		t.Fatalf("outcome = %v, want %v", outcome, models.OutcomeUserStopped)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1 (stop must not retry)", runner.calls)
	}
	if control.Snapshot().Phase != models.PlanPhaseFailed {
		t.Fatalf("phase = %q, want failed", control.Snapshot().Phase)
	}
}

func TestSkipStepChecksOnlyTheAddressedBox(t *testing.T) {
	content := "- [x] 1. Done already.\n\n" +
		"- [ ] 2. Pending with code.\n  ```\n  - [ ] looks like a step\n  ```\n  Done when: it works.\n\n" +
		"- [ ] 3. Also pending.\n"

	updated, ok := services.SkipStep(content, 1)
	if !ok {
		t.Fatal("expected the second step to be skippable")
	}
	steps := services.ParseSteps(updated)
	if !steps[1].Completed || steps[2].Completed {
		t.Fatalf("expected only step 2 checked, got %+v", steps)
	}
	if !strings.Contains(updated, "- [ ] looks like a step") {
		t.Fatal("the checkbox inside the fenced block must stay untouched")
	}

	if _, ok := services.SkipStep(content, 0); ok {
		t.Fatal("an already-checked step must not be skippable")
	}
	if _, ok := services.SkipStep(content, 9); ok {
		t.Fatal("an out-of-range index must not be skippable")
	}
}
