package services_test

import (
	"context"
	"io"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func annotation(section models.AnnotationSection, target, comment string) models.Annotation {
	return models.Annotation{
		At:      time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		Section: section,
		Target:  target,
		Comment: comment,
	}
}

// closedChannel returns an already-dismissed channel so ServeAnnotations
// drains the queue once and exits.
func closedChannel() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestServeAnnotationsAppliesQueuedFeedbackInOrder(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] first step\n")
	fs.Write("TESTS.md", validTestsDoc)
	fs.Write("GOAL.md", "the goal")
	var staged []string
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		content, _ := fs.Read("ANNOTATION.md")
		staged = append(staged, content)
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionPlan, "", "tighten the scope"),
		annotation(models.AnnotationSectionSteps, "Step 1: first step", "split into two steps"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if runner.calls != 2 {
		t.Fatalf("annotate invocations = %d, want 2", runner.calls)
	}
	wantFirst := "# Annotation\n\n**Section:** plan (PLAN.md)\n\n**Requested adjustment:**\n\ntighten the scope\n"
	if staged[0] != wantFirst {
		t.Errorf("first ANNOTATION.md = %q, want %q", staged[0], wantFirst)
	}
	wantSecond := "# Annotation\n\n**Section:** steps (STEPS.md)\n\n**Target:** Step 1: first step\n\n" +
		"**Requested adjustment:**\n\nsplit into two steps\n"
	if staged[1] != wantSecond {
		t.Errorf("second ANNOTATION.md = %q, want %q", staged[1], wantSecond)
	}
	if fs.Exists("ANNOTATION.md") {
		t.Error("ANNOTATION.md should be removed after applying")
	}
	if reporter.plan != "the plan" || reporter.goal != "the goal" {
		t.Errorf("plan/goal not republished: plan=%q goal=%q", reporter.plan, reporter.goal)
	}
}

func TestServeAnnotationsRepublishesAfterEachApplication(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "original plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		fs.Write("PLAN.md", "revised plan")
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionPlan, "", "reword the summary"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if reporter.plan != "revised plan" {
		t.Errorf("republished plan = %q, want the tool's revision", reporter.plan)
	}
}

// invocationPrompts lists the second argument of each recorded invocation, the
// slot planConfig uses to name what the tool was asked to do.
func invocationPrompts(runner *fakeRunner) []string {
	prompts := make([]string, 0, len(runner.invocations))
	for _, inv := range runner.invocations {
		prompts = append(prompts, inv.Args[1])
	}
	return prompts
}

func TestGoalAnnotationRebuildsPlanStepsAndTests(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "the goal")
	fs.Write("PLAN.md", "stale plan")
	fs.Write("STEPS.md", "- [ ] stale step\n")
	fs.Write("TESTS.md", validTestsDoc)
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // annotate: the tool revises the goal
			fs.Write("GOAL.md", "the revised goal")
		case 2: // replan: the tool rebuilds plan and steps from the new goal
			fs.Write("PLAN.md", "rebuilt plan")
			fs.Write("STEPS.md", "- [ ] rebuilt step\n")
		case 3: // tests: rebuilt for the new plan
			fs.Write("TESTS.md", validTestsDoc)
		}
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionGoal, "", "target teams, not individuals"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	wantPrompts := []string{"annotate", "plan", "tests"}
	got := invocationPrompts(runner)
	if len(got) != len(wantPrompts) {
		t.Fatalf("invocations = %v, want %v", got, wantPrompts)
	}
	for i, want := range wantPrompts {
		if got[i] != want {
			t.Errorf("invocation %d = %q, want %q (full order %v)", i+1, got[i], want, got)
		}
	}
	if reporter.goal != "the revised goal" {
		t.Errorf("republished goal = %q, want the revision", reporter.goal)
	}
	if reporter.plan != "rebuilt plan" {
		t.Errorf("republished plan = %q, want the rebuilt plan", reporter.plan)
	}
	if len(reporter.taskSteps) != 1 || reporter.taskSteps[0].Text != "rebuilt step" {
		t.Errorf("republished steps = %+v, want the rebuilt step", reporter.taskSteps)
	}
}

func TestGoalAnnotationDiscardsStaleDocumentsBeforeReplanning(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "the goal")
	fs.Write("PLAN.md", "stale plan")
	fs.Write("STEPS.md", "- [ ] stale step\n")
	fs.Write("TESTS.md", validTestsDoc)
	var atReplan struct{ plan, steps, tests bool }
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 { // the replan invocation must not see the previous goal's output
			atReplan.plan = fs.Exists("PLAN.md")
			atReplan.steps = fs.Exists("STEPS.md")
			atReplan.tests = fs.Exists("TESTS.md")
			fs.Write("PLAN.md", "rebuilt plan")
			fs.Write("STEPS.md", "- [ ] rebuilt step\n")
		}
		if call == 3 {
			fs.Write("TESTS.md", validTestsDoc)
		}
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionGoal, "", "widen the scope"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if atReplan.plan || atReplan.steps || atReplan.tests {
		t.Errorf("stale documents survived into the replan: plan=%v steps=%v tests=%v",
			atReplan.plan, atReplan.steps, atReplan.tests)
	}
}

func TestPlanAnnotationDoesNotRebuildFromGoal(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "the goal")
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	fs.Write("TESTS.md", validTestsDoc)
	runner := &fakeRunner{}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionPlan, "", "tighten the scope"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if got := invocationPrompts(runner); len(got) != 1 || got[0] != "annotate" {
		t.Errorf("invocations = %v, want just the annotate pass", got)
	}
	if !fs.Exists("PLAN.md") || !fs.Exists("STEPS.md") || !fs.Exists("TESTS.md") {
		t.Error("a non-goal annotation must not discard the plan documents")
	}
}

func TestServeAnnotationsAppliesLateArrivalsUntilDismissed(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	invoked := make(chan struct{}, 1)
	runner := &fakeRunner{script: func(int, io.Writer) error {
		invoked <- struct{}{}
		return nil
	}}
	dismissed := make(chan struct{})
	reporter := &fakeStatusReporter{signal: make(chan struct{}, 1)}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	done := make(chan struct{})
	go func() {
		o.ServeAnnotations(context.Background(), dismissed)
		close(done)
	}()

	// Submit an annotation after the loop is already waiting, the way the
	// status page does during the hold window. A plan annotation keeps this
	// test on delivery alone; the goal section's extra rebuild passes are
	// covered by TestGoalAnnotationRebuildsPlanStepsAndTests.
	reporter.mu.Lock()
	reporter.annotations = append(reporter.annotations,
		annotation(models.AnnotationSectionPlan, "", "clarify the audience"))
	reporter.mu.Unlock()
	reporter.signal <- struct{}{}

	select {
	case <-invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("annotation did not trigger an annotate invocation")
	}
	close(dismissed)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeAnnotations did not exit on dismissal")
	}
	if runner.calls != 1 {
		t.Errorf("annotate invocations = %d, want 1", runner.calls)
	}
	if fs.Exists("ANNOTATION.md") {
		t.Error("ANNOTATION.md should be removed after applying")
	}
}

func TestServeAnnotationsExitsOnContextCancel(t *testing.T) {
	fs := newFakeFileStore()
	reporter := &fakeStatusReporter{signal: make(chan struct{}, 1)}
	o := services.NewPlanOrchestrator(&fakeRunner{}, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		o.ServeAnnotations(ctx, make(chan struct{}))
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeAnnotations did not exit on context cancel")
	}
}

func TestServeAnnotationsWithoutReporterReturnsImmediately(t *testing.T) {
	o := services.NewPlanOrchestrator(&fakeRunner{}, newFakeFileStore(), &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))
	o.ServeAnnotations(context.Background(), make(chan struct{}))
}

func TestServeFeedbackReturnsTrueOnImplementRequest(t *testing.T) {
	fs := newFakeFileStore()
	reporter := &fakeStatusReporter{
		signal:    make(chan struct{}, 1),
		implement: make(chan struct{}, 1),
	}
	o := services.NewPlanOrchestrator(&fakeRunner{}, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	result := make(chan bool, 1)
	go func() { result <- o.ServeFeedback(context.Background(), make(chan struct{})) }()
	reporter.implement <- struct{}{}

	select {
	case implement := <-result:
		if !implement {
			t.Fatal("ServeFeedback = false on implement request, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeFeedback did not return on implement request")
	}
}

func TestServeFeedbackAppliesAnnotationsAndReturnsFalseOnDismissal(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	runner := &fakeRunner{}
	reporter := &fakeStatusReporter{
		signal:    make(chan struct{}, 1),
		implement: make(chan struct{}, 1),
		annotations: []models.Annotation{
			annotation(models.AnnotationSectionPlan, "", "tighten the scope"),
		},
	}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	if o.ServeFeedback(context.Background(), closedChannel()) {
		t.Fatal("ServeFeedback = true on dismissal, want false")
	}
	if runner.calls != 1 {
		t.Errorf("annotate invocations = %d, want the queued annotation applied", runner.calls)
	}
}

func TestServeFeedbackWithoutReporterReturnsFalse(t *testing.T) {
	o := services.NewPlanOrchestrator(&fakeRunner{}, newFakeFileStore(), &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))
	if o.ServeFeedback(context.Background(), make(chan struct{})) {
		t.Fatal("ServeFeedback without a reporter = true, want false")
	}
}
