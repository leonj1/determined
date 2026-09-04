package main

// This file automates TESTS.md Test 1: the attended planning journey that
// exercises every alignment gate in one run, chained into the exec approval
// via cmd/determined's fake call-site mirror. The per-gate behaviours have
// their own unit tests under src/services; this single test proves their
// interaction — ordering, state carried between gates, and the exec decline
// tail — survives regressions the isolated tests would miss.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// --- Journey-local fakes: package main cannot reuse services' test fakes. ---

type journeyFileStore struct{ data map[string]string }

func newJourneyFileStore() *journeyFileStore {
	return &journeyFileStore{data: map[string]string{}}
}

func (f *journeyFileStore) Exists(path string) bool { _, ok := f.data[path]; return ok }

func (f *journeyFileStore) Read(path string) (string, error) {
	v, ok := f.data[path]
	if !ok {
		return "", errors.New("no such file: " + path)
	}
	return v, nil
}

func (f *journeyFileStore) Write(path, content string) error { f.data[path] = content; return nil }

func (f *journeyFileStore) Append(path, content string) error {
	f.data[path] += content
	return nil
}

func (f *journeyFileStore) Remove(path string) error { delete(f.data, path); return nil }

// journeyPrompter replays scripted answers in order and records every question,
// so the test can assert each gate asked in sequence and got its answer.
type journeyPrompter struct {
	answers []string
	asked   []string
	next    int
}

func (p *journeyPrompter) Ask(_ context.Context, prompt models.UserPrompt) (string, error) {
	p.asked = append(p.asked, prompt.Body)
	if p.next >= len(p.answers) {
		return "", io.EOF
	}
	a := p.answers[p.next]
	p.next++
	return a, nil
}

type journeyRunner struct {
	calls       int
	invocations []models.Invocation
	script      func(call int) error
}

func (r *journeyRunner) Run(_ context.Context, inv models.Invocation, _ io.Writer) error {
	r.calls++
	r.invocations = append(r.invocations, inv)
	return r.script(r.calls)
}

// prompt extracts the prompt marker embedded in the call-th invocation,
// relying on the {"-p", marker} argument shape of the journey config.
func (r *journeyRunner) prompt(call int) string {
	return r.invocations[call-1].Args[1]
}

type journeyLog struct{}

func (journeyLog) Write(p []byte) (int, error) { return len(p), nil }
func (journeyLog) Close() error                { return nil }

type journeyLogSink struct{}

func (journeyLogSink) OpenIteration(int) (io.WriteCloser, error) { return journeyLog{}, nil }

// journeyStatusReporter records the observable page events, so the test can
// assert the annotate republish and the published cap-gate findings.
type journeyStatusReporter struct {
	events      []string
	plan        string
	assumptions string
}

func (r *journeyStatusReporter) Progress(message string) {
	r.events = append(r.events, "progress: "+message)
}
func (r *journeyStatusReporter) Start()               { r.events = append(r.events, "start") }
func (r *journeyStatusReporter) BeginLogEntry(string) {}
func (r *journeyStatusReporter) AppendLogOutput(string) {
}
func (r *journeyStatusReporter) SetGoal(string) {}
func (r *journeyStatusReporter) SetPlan(plan string) {
	r.plan = plan
	r.events = append(r.events, "plan")
}
func (r *journeyStatusReporter) SetAssumptions(assumptions string) {
	r.assumptions = assumptions
	r.events = append(r.events, "assumptions")
}
func (r *journeyStatusReporter) SetDemo(string)                 {}
func (r *journeyStatusReporter) SetTests(string)                {}
func (r *journeyStatusReporter) SetTaskSteps([]models.TaskStep) {}
func (r *journeyStatusReporter) Finish(bool)                    { r.events = append(r.events, "finish") }
func (r *journeyStatusReporter) TakeAnnotation() (models.Annotation, bool) {
	return models.Annotation{}, false
}
func (r *journeyStatusReporter) AnnotationSignal() <-chan struct{} { return nil }
func (r *journeyStatusReporter) ImplementSignal() <-chan struct{}  { return nil }
func (r *journeyStatusReporter) ExplainSignal() <-chan struct{}    { return nil }

func journeyContainsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

// --- Fixtures mirroring TESTS.md Test 1 ---

const journeyDraftedPlan = "the plan\n\n## Assumptions\n\n" +
	"- 60 requests/minute limit\n- no auth\n"

const journeyCorrectedPlan = "the plan\n\n## Assumptions\n\n" +
	"- 100 requests/minute limit\n- no auth\n"

const journeyMisalignedTests = "### Test 1: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** misaligned\n**Alignment note:** proves logging, not the goal.\n\n" +
	"### Test 2: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n"

const journeyAlignedTests = "### Test 1: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n\n" +
	"### Test 2: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n"

func journeyPlanConfig() models.PlanConfig {
	return models.PlanConfig{
		Operation:          models.PlanOperationCreate,
		Goal:               "add rate limiting",
		Invocation:         models.Invocation{Binary: "claude", Args: []string{"-p", "plan"}},
		AssessInvocation:   models.Invocation{Binary: "claude", Args: []string{"-p", "assess"}},
		RefineInvocation:   models.Invocation{Binary: "claude", Args: []string{"-p", "refine"}},
		TestsInvocation:    models.Invocation{Binary: "claude", Args: []string{"-p", "tests"}},
		AlignInvocation:    models.Invocation{Binary: "claude", Args: []string{"-p", "align"}},
		RealignInvocation:  models.Invocation{Binary: "claude", Args: []string{"-p", "realign"}},
		AnnotateInvocation: models.Invocation{Binary: "claude", Args: []string{"-p", "annotate"}},
		MaxRefinePasses:    2,
		GoalFile:           "GOAL.md",
		QuestionsFile:      "QUESTIONS.md",
		AnswersFile:        "ANSWERS.md",
		PlanFile:           "PLAN.md",
		StepsFile:          "STEPS.md",
		TestsFile:          "TESTS.md",
		AssessmentFile:     "REFINEMENTS.md",
		AnnotationFile:     "ANNOTATION.md",
	}
}

// TestPlanJourneyExercisesEveryGateAndDeclinesExecution drives TESTS.md
// Test 1 in one run: clarifying question, drafted plan with assumptions, a
// correction annotated and republished, the automatic realign fixing the
// misaligned verdict, the preference answer recorded before the refiner runs,
// the exhausted refine cap accepted, and the chained-exec approval declined
// with bare Enter.
func TestPlanJourneyExercisesEveryGateAndDeclinesExecution(t *testing.T) {
	fs := newJourneyFileStore()
	prompter := &journeyPrompter{answers: []string{
		"use sensible defaults",              // clarifying question before drafting
		"cap at 100 requests/minute, not 60", // assumptions correction
		"sliding",                            // relayed assessor preference question
		"accept",                             // exhausted refine-cap gate
		"",                                   // exec approval: bare Enter declines
	}}
	var annotationStaged string
	var answersAtRefine string
	questionsClearedAtRefine := false
	runner := &journeyRunner{script: func(call int) error {
		switch call {
		case 1: // plan: asks before drafting
			fs.Write("QUESTIONS.md", "1. Which limits apply?\n")
		case 2: // plan: drafts with assumptions and one misaligned verdict
			fs.Write("PLAN.md", journeyDraftedPlan)
			fs.Write("STEPS.md", "1. Build everything")
			fs.Write("TESTS.md", journeyMisalignedTests)
		case 3: // automatic realign fixes the misaligned test
			fs.Write("TESTS.md", journeyAlignedTests)
		case 4: // annotate applies the assumptions correction
			annotationStaged = fs.data["ANNOTATION.md"]
			fs.Write("PLAN.md", journeyCorrectedPlan)
		case 5: // assess: two open findings plus one preference question
			fs.Write("REFINEMENTS.md", "- Finding A\n- Finding B\n")
			fs.Write("QUESTIONS.md", "1. Prefer a sliding or fixed window?\n")
		case 6: // refine: observes the state the gates left behind
			answersAtRefine = fs.data["ANSWERS.md"]
			questionsClearedAtRefine = !fs.Exists("QUESTIONS.md")
		case 7: // assess: the findings stay open, exhausting the cap
			fs.Write("REFINEMENTS.md", "- Finding A\n- Finding B\n")
		}
		return nil
	}}
	reporter := &journeyStatusReporter{}
	o := services.NewPlanOrchestrator(
		runner, fs, prompter, fixedClock{now: time.Now()},
		journeyLogSink{}, io.Discard, journeyPlanConfig(),
	).WithStatusReporter(reporter)

	planOutcome := o.Run(context.Background())

	if planOutcome != models.OutcomePlanReady {
		t.Fatalf("plan outcome = %v, want %v", planOutcome, models.OutcomePlanReady)
	}

	// The chained-exec tail, mirroring the headless call site.
	executor := &fakePlanExecutor{outcome: models.OutcomeStopped}
	outcome, executed := planOutcome, false
	if shouldExecuteAfterPlan(true, false, planOutcome) {
		outcome, executed = chainedExecution(prompter, executor)
	}

	// Every gate asked, in journey order, and nothing else.
	wantAsked := []string{
		"Which limits apply?",
		"The plan rests on these assumptions:",
		"Prefer a sliding or fixed window?",
		"accept (finish with the plan as-is)",
		"Plan approved — begin execution? [y/N]",
	}
	if len(prompter.asked) != len(wantAsked) {
		t.Fatalf("asked %d questions, want %d: %q", len(prompter.asked), len(wantAsked), prompter.asked)
	}
	for i, want := range wantAsked {
		if !strings.Contains(prompter.asked[i], want) {
			t.Fatalf("question %d = %q, want it to contain %q", i+1, prompter.asked[i], want)
		}
	}
	if !strings.Contains(prompter.asked[1], "- 60 requests/minute limit") {
		t.Fatalf("assumptions question did not relay the drafted list: %q", prompter.asked[1])
	}

	// Invocation order: plan, plan, realign, annotate, assess, refine, assess.
	wantPrompts := []string{"plan", "plan", "realign", "annotate", "assess", "refine", "assess"}
	if runner.calls != len(wantPrompts) {
		t.Fatalf("runner calls = %d, want %d: %v", runner.calls, len(wantPrompts), runner.invocations)
	}
	for i, want := range wantPrompts {
		if got := runner.prompt(i + 1); got != want {
			t.Fatalf("invocation %d = %q, want %q", i+1, got, want)
		}
	}

	// The correction was staged for the annotate invocation and republished.
	if !strings.Contains(annotationStaged, "cap at 100 requests/minute, not 60") {
		t.Fatalf("annotate invocation did not see the staged correction, got %q", annotationStaged)
	}
	if reporter.plan != journeyCorrectedPlan {
		t.Fatalf("republished plan = %q, want the corrected plan", reporter.plan)
	}
	if !strings.Contains(reporter.assumptions, "100 requests/minute limit") {
		t.Fatalf("republished assumptions = %q, want the corrected default", reporter.assumptions)
	}

	// The preference answer landed in ANSWERS.md before the refiner ran, and
	// the questions file was cleared so the refiner only sees answers.
	if !strings.Contains(answersAtRefine, "Prefer a sliding or fixed window?") ||
		!strings.Contains(answersAtRefine, "sliding") {
		t.Fatalf("ANSWERS.md at refine time = %q, want the recorded preference answer", answersAtRefine)
	}
	if !questionsClearedAtRefine {
		t.Fatal("QUESTIONS.md was not cleared before the refine invocation")
	}
	if !strings.Contains(fs.data["ANSWERS.md"], "use sensible defaults") {
		t.Fatalf("ANSWERS.md lost the clarifying answer: %q", fs.data["ANSWERS.md"])
	}

	// The automatic realign pass fixed the misaligned verdict without a gate ask.
	if fs.data["TESTS.md"] != journeyAlignedTests {
		t.Fatalf("TESTS.md = %q, want the realigned document", fs.data["TESTS.md"])
	}

	// The exhausted cap published both open findings before the accept answer.
	for _, finding := range []string{"Finding A", "Finding B"} {
		if !journeyContainsEvent(reporter.events, "progress: unresolved finding: "+finding) {
			t.Fatalf("cap gate did not publish %q, events %q", finding, reporter.events)
		}
	}
	if fs.Exists("REFINEMENTS.md") {
		t.Fatalf("accept did not clear the assessment, got %q", fs.data["REFINEMENTS.md"])
	}

	// Bare Enter declined execution: the loop never ran, plan files survive.
	if executed {
		t.Fatal("bare Enter must not start the execute loop")
	}
	if executor.status != nil {
		t.Fatal("the execute loop was invoked despite the declined approval")
	}
	if outcome != models.OutcomePlanReady {
		t.Fatalf("final outcome = %v, want %v", outcome, models.OutcomePlanReady)
	}
	for _, name := range []string{"PLAN.md", "STEPS.md", "TESTS.md"} {
		if !fs.Exists(name) {
			t.Fatalf("%s did not survive the declined approval", name)
		}
	}
	if fs.data["PLAN.md"] != journeyCorrectedPlan {
		t.Fatalf("PLAN.md = %q, want the corrected plan intact", fs.data["PLAN.md"])
	}
}
