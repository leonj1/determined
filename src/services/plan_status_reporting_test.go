package services_test

import (
	"context"
	"io"
	"sync"
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
	demo      string
	tests     string
	taskSteps []models.TaskStep
	logOutput string

	// mu guards annotations: ServeAnnotations drains them from another
	// goroutine while a test appends late arrivals.
	mu          sync.Mutex
	annotations []models.Annotation
	signal      chan struct{}
	implement   chan struct{}
}

func (r *fakeStatusReporter) Progress(message string) {
	r.events = append(r.events, "progress: "+message)
}
func (r *fakeStatusReporter) Start() { r.events = append(r.events, "start") }
func (r *fakeStatusReporter) BeginLogEntry(message string) {
	r.events = append(r.events, "log-entry: "+message)
}
func (r *fakeStatusReporter) AppendLogOutput(text string) { r.logOutput += text }
func (r *fakeStatusReporter) SetGoal(goal string) {
	r.goal = goal
	r.events = append(r.events, "goal")
}
func (r *fakeStatusReporter) SetPlan(plan string) {
	r.plan = plan
	r.events = append(r.events, "plan")
}
func (r *fakeStatusReporter) SetDemo(demo string) {
	r.demo = demo
	r.events = append(r.events, "demo")
}
func (r *fakeStatusReporter) SetTests(tests string) {
	r.tests = tests
	r.events = append(r.events, "tests")
}
func (r *fakeStatusReporter) SetTaskSteps(steps []models.TaskStep) {
	r.taskSteps = steps
	r.events = append(r.events, "task-steps")
}
func (r *fakeStatusReporter) WaitForInput() { r.events = append(r.events, "wait-for-input") }
func (r *fakeStatusReporter) TakeAnnotation() (models.Annotation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.annotations) == 0 {
		return models.Annotation{}, false
	}
	taken := r.annotations[0]
	r.annotations = r.annotations[1:]
	return taken, true
}
func (r *fakeStatusReporter) AnnotationSignal() <-chan struct{} {
	if r.signal == nil {
		r.signal = make(chan struct{}, 1)
	}
	return r.signal
}
func (r *fakeStatusReporter) ImplementSignal() <-chan struct{} {
	return r.implement // nil unless a test arms it; a nil channel never fires
}
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
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1:
			io.WriteString(out, "asking about storage\n")
			fs.Write("QUESTIONS.md", "1. What database?\n")
		case 2:
			io.WriteString(out, "plan written\n")
			fs.Write("PLAN.md", "the plan")
			fs.Write("TESTS.md", validTestsDoc)
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
		"log-entry: planning project",
		"wait-for-input",
		"progress: planning project",
		"log-entry: planning project",
		"plan",       // refine entry publishes the finished plan
		"tests",      // ...the recommended TESTS.md
		"task-steps", // ...and the parsed STEPS.md items
		"plan",       // reportFinish re-publishes the final plan text
		"tests",      // ...the final recommended tests
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
	if reporter.tests != validTestsDoc {
		t.Errorf("reported tests = %q, want TESTS.md contents", reporter.tests)
	}
	if reporter.logOutput != "asking about storage\nplan written\n" {
		t.Errorf("log output = %q, want both invocations' streamed output", reporter.logOutput)
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

func TestToolOutputWithoutTrailingNewlineIsFlushedToLog(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(_ int, out io.Writer) error {
		io.WriteString(out, "partial line without newline")
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	reporter := &fakeStatusReporter{}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	if outcome := o.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready", outcome)
	}
	if reporter.logOutput != "partial line without newline" {
		t.Errorf("log output = %q, want the flushed partial line", reporter.logOutput)
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
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	if outcome := o.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready with nil reporter", outcome)
	}
}

func TestInteractivePlanGeneratesEligibleDemoAfterPlan(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "add a compact theme toggle")
	fs.Write("STEPS.md", "- [ ] add the toggle\n")
	fs.Write("TESTS.md", validTestsDoc)
	cfg := planConfig(0)
	cfg.DemoFile = "DEMO.html"
	cfg.DemoInvocation = models.Invocation{Binary: "claude", Args: []string{"-p", "demo"}}
	runner := &fakeRunner{script: func(_ int, _ io.Writer) error {
		return fs.Write("DEMO.html", "<button>Theme</button>")
	}}
	reporter := &fakeStatusReporter{}
	orchestrator := services.NewPlanOrchestrator(
		runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg,
	).WithStatusReporter(reporter)

	if outcome := orchestrator.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready", outcome)
	}
	if runner.calls != 1 || reporter.demo != "<button>Theme</button>" {
		t.Fatalf("calls = %d, demo = %q", runner.calls, reporter.demo)
	}
	assertDemoFollowsPlan(t, reporter.events)
}

func assertDemoFollowsPlan(t *testing.T, events []string) {
	t.Helper()
	planIndex, demoIndex := -1, -1
	for i, event := range events {
		if planIndex < 0 && event == "plan" {
			planIndex = i
		}
		if event == "progress: generating UI demo" {
			demoIndex = i
		}
	}
	if planIndex < 0 || demoIndex <= planIndex {
		t.Fatalf("demo generation must follow plan publication: %v", events)
	}
}

func TestIneligibleDemoCheckClearsPreviousArtifact(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "replace the database engine")
	fs.Write("STEPS.md", "- [ ] migrate the database\n")
	fs.Write("TESTS.md", validTestsDoc)
	fs.Write("DEMO.html", "<p>stale demo</p>")
	cfg := planConfig(0)
	cfg.DemoFile = "DEMO.html"
	cfg.DemoInvocation = models.Invocation{Binary: "claude", Args: []string{"-p", "demo"}}
	reporter := &fakeStatusReporter{}
	orchestrator := services.NewPlanOrchestrator(
		&fakeRunner{}, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg,
	).WithStatusReporter(reporter)

	if outcome := orchestrator.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready", outcome)
	}
	if fs.Exists("DEMO.html") || reporter.demo != "" {
		t.Fatalf("stale demo survived: file=%v status=%q", fs.Exists("DEMO.html"), reporter.demo)
	}
}

func TestHeadlessPlanClearsStaleDemoWithoutGeneratingOne(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] the step\n")
	fs.Write("TESTS.md", validTestsDoc)
	fs.Write("DEMO.html", "<p>stale demo</p>")
	cfg := planConfig(0)
	cfg.DemoFile = "DEMO.html"
	cfg.DemoInvocation = models.Invocation{Binary: "claude", Args: []string{"-p", "demo"}}
	runner := &fakeRunner{}
	orchestrator := services.NewPlanOrchestrator(
		runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg,
	)

	if outcome := orchestrator.Run(context.Background()); outcome != models.OutcomePlanReady {
		t.Fatalf("outcome = %v, want plan ready", outcome)
	}
	if fs.Exists("DEMO.html") || runner.calls != 0 {
		t.Fatalf("headless demo state: file=%v calls=%d", fs.Exists("DEMO.html"), runner.calls)
	}
}
