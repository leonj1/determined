package services_test

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

// validTestsDoc is a minimal TESTS.md whose journey test carries the required
// mermaid sequence diagram and its alignment verdict against the plan's goal.
const validTestsDoc = "### Test 1: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n"

// --- Hand-written fakes for the planning loop ---

// fakeFileStore is an in-memory stand-in for the protocol files.
type fakeFileStore struct {
	data map[string]string
}

func newFakeFileStore() *fakeFileStore { return &fakeFileStore{data: map[string]string{}} }

func (f *fakeFileStore) Exists(path string) bool { _, ok := f.data[path]; return ok }

func (f *fakeFileStore) Read(path string) (string, error) {
	v, ok := f.data[path]
	if !ok {
		return "", errors.New("no such file: " + path)
	}
	return v, nil
}

func (f *fakeFileStore) Write(path, content string) error { f.data[path] = content; return nil }

func (f *fakeFileStore) Append(path, content string) error {
	f.data[path] += content
	return nil
}

func (f *fakeFileStore) Remove(path string) error { delete(f.data, path); return nil }

// fakePrompter replays scripted answers and records the questions asked.
type fakePrompter struct {
	answers []string
	asked   []string
	next    int
}

func (p *fakePrompter) Ask(question string) (string, error) {
	p.asked = append(p.asked, question)
	if p.next >= len(p.answers) {
		return "", io.EOF
	}
	a := p.answers[p.next]
	p.next++
	return a, nil
}

func planConfig(budget time.Duration) models.PlanConfig {
	return models.PlanConfig{
		Operation:          models.PlanOperationCreate,
		Goal:               "build a todo CLI",
		Invocation:         models.Invocation{Binary: "claude", Args: []string{"-p", "plan"}},
		Budget:             budget,
		AssessInvocation:   models.Invocation{Binary: "claude", Args: []string{"-p", "assess"}},
		RefineInvocation:   models.Invocation{Binary: "claude", Args: []string{"-p", "refine"}},
		TestsInvocation:    models.Invocation{Binary: "claude", Args: []string{"-p", "tests"}},
		AlignInvocation:    models.Invocation{Binary: "claude", Args: []string{"-p", "align"}},
		RealignInvocation:  models.Invocation{Binary: "claude", Args: []string{"-p", "realign"}},
		AnnotateInvocation: models.Invocation{Binary: "claude", Args: []string{"-p", "annotate"}},
		MaxRefinePasses:    0, // refinement off by default; refinement tests opt in
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

func reviewConfig() models.PlanConfig {
	cfg := planConfig(0)
	cfg.Operation = models.PlanOperationReview
	cfg.Goal = ""
	cfg.Invocation = models.Invocation{}
	cfg.MaxRefinePasses = 3
	cfg.QuestionsFile = "REVIEW_QUESTIONS.md"
	cfg.AnswersFile = "REVIEW_ANSWERS.md"
	return cfg
}

// --- Functional tests ---

func TestPlanAsksQuestionsThenCompletes(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"SQLite", "no auth"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("QUESTIONS.md", "1. What database?\n2. Auth required?\n")
		case 2:
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "the steps")
			fs.Write("TESTS.md", validTestsDoc)
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady || outcome.ExitCode() != 0 {
		t.Fatalf("expected a ready plan (exit 0), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected 2 tool rounds (ask, then write plan), got %d", runner.calls)
	}
	if fs.data["GOAL.md"] != "build a todo CLI\n" {
		t.Fatalf("expected the goal seeded to GOAL.md, got %q", fs.data["GOAL.md"])
	}
	if fs.Exists("QUESTIONS.md") {
		t.Fatal("expected QUESTIONS.md to be cleared after relaying")
	}
	answers := fs.data["ANSWERS.md"]
	for _, want := range []string{"What database?", "SQLite", "Auth required?", "no auth"} {
		if !strings.Contains(answers, want) {
			t.Fatalf("expected ANSWERS.md to record %q, got:\n%s", want, answers)
		}
	}
	if len(prompter.asked) != 2 {
		t.Fatalf("expected the user to be asked 2 questions, got %d", len(prompter.asked))
	}
}

func TestUserCanSeeTimestampedPlanningStages(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 2
	clock := &fakeClock{now: time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)}
	prompter := &fakePrompter{answers: []string{"SQLite"}}
	var terminal strings.Builder
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("QUESTIONS.md", "1. Which database?\n")
		case 2:
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "the steps")
			fs.Write("TESTS.md", validTestsDoc)
		case 3:
			fs.Write("REFINEMENTS.md", "- Add validation")
		case 5:
			fs.Write("REFINEMENTS.md", "NONE")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(
		runner, fs, prompter, clock, &fakeLogSink{}, &terminal, cfg,
	)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected timestamped planning to complete, got %v", outcome)
	}
	prefix := "==> [2026-07-11 09:30:00] "
	for _, stage := range []string{
		"writing planning goal", "planning project", "answering planning questions",
		"assessing plan", "refining plan",
	} {
		if !strings.Contains(terminal.String(), prefix+stage) {
			t.Fatalf("expected visible stage %q, got:\n%s", stage, terminal.String())
		}
	}
}

func TestPlanUsesExistingGoalWhenConfirmed(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "existing goal\n")
	prompter := &fakePrompter{answers: []string{"yes", "SQLite"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("QUESTIONS.md", "1. What database?\n")
		case 2:
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "the steps")
			fs.Write("TESTS.md", validTestsDoc)
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "existing goal\n" {
		t.Fatalf("expected existing GOAL.md to be preserved, got %q", fs.data["GOAL.md"])
	}
	if len(prompter.asked) != 2 {
		t.Fatalf("expected one goal confirmation and one clarifying question, got %d prompts", len(prompter.asked))
	}
	if !strings.Contains(prompter.asked[0], "GOAL.md already exists") {
		t.Fatalf("expected the first prompt to confirm the existing goal, got %q", prompter.asked[0])
	}
}

func TestPlanReplacesExistingGoalWhenDeclined(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "existing goal\n")
	prompter := &fakePrompter{answers: []string{"no"}}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "build a todo CLI\n" {
		t.Fatalf("expected GOAL.md to be replaced with the CLI goal, got %q", fs.data["GOAL.md"])
	}
	if len(prompter.asked) != 1 {
		t.Fatalf("expected one goal confirmation, got %d prompts", len(prompter.asked))
	}
}

func TestPlanUsesProvidedFileAsGoal(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("TODO.md", "# Goal\n\nBuild the todo CLI from this file.\n")
	cfg := planConfig(0)
	cfg.Goal = "Read TODO.md"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != fs.data["TODO.md"] {
		t.Fatalf("expected GOAL.md to use TODO.md contents, got %q", fs.data["GOAL.md"])
	}
}

func TestPlanUsesProvidedFileWithSpacesAsGoal(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("todo goal.md", "build from a filename with spaces\n")
	cfg := planConfig(0)
	cfg.Goal = "Read todo goal.md"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "build from a filename with spaces\n" {
		t.Fatalf("expected GOAL.md to use the spaced filename contents, got %q", fs.data["GOAL.md"])
	}
}

func TestPlanUsesProvidedPathAsGoal(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("TODO.md", "build from the bare path\n")
	cfg := planConfig(0)
	cfg.Goal = "TODO.md"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "build from the bare path\n" {
		t.Fatalf("expected GOAL.md to use TODO.md contents, got %q", fs.data["GOAL.md"])
	}
}

func TestPlanReplacesExistingGoalWithProvidedFileWhenDeclined(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "existing goal\n")
	fs.Write("TODO.md", "new session goal\n")
	prompter := &fakePrompter{answers: []string{"no"}}
	cfg := planConfig(0)
	cfg.Goal = "Read TODO.md"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "new session goal\n" {
		t.Fatalf("expected GOAL.md to be replaced with TODO.md contents, got %q", fs.data["GOAL.md"])
	}
	if len(prompter.asked) != 1 {
		t.Fatalf("expected one goal confirmation, got %d prompts", len(prompter.asked))
	}
}

func TestPlanReplacesBareHeadingGoalWithoutPrompt(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "#\n")
	prompter := &fakePrompter{}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if fs.data["GOAL.md"] != "build a todo CLI\n" {
		t.Fatalf("expected placeholder GOAL.md to be replaced, got %q", fs.data["GOAL.md"])
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no prompt for placeholder GOAL.md, got %d prompts", len(prompter.asked))
	}
	if !strings.Contains(terminal.String(), "replacing it with --plan input") {
		t.Fatalf("expected terminal to explain placeholder replacement, got %q", terminal.String())
	}
}

func TestPlanRejectsBareHeadingInputBeforeToolRuns(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.Goal = "#"
	runner := &fakeRunner{}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeInvalidGoal || outcome.ExitCode() != 1 {
		t.Fatalf("expected invalid goal (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool runs for invalid goal, got %d", runner.calls)
	}
	if fs.Exists("GOAL.md") {
		t.Fatal("expected invalid GOAL.md not to be written")
	}
	for _, want := range []string{"bare `#` heading", "--plan TODO.md", "--plan \"$(cat TODO.md)\""} {
		if !strings.Contains(terminal.String(), want) {
			t.Fatalf("expected terminal guidance to include %q, got %q", want, terminal.String())
		}
	}
}

func TestPlanRefinesOversizedSteps(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 5
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // planning round produces a plan with one big step
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build the entire app")
			fs.Write("TESTS.md", validTestsDoc)
		case 2: // first assessment: the step is too large
			fs.Write("REFINEMENTS.md", "- Step is too large: Build the entire app")
		case 3: // breakdown: rewrite STEPS.md into smaller steps
			fs.Write("STEPS.md", "1. Add storage\n2. Add CLI\n3. Wire up")
			fs.Write("TESTS.md", validTestsDoc)
		case 4: // second assessment: now everything is small enough
			fs.Write("REFINEMENTS.md", "NONE")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady || outcome.ExitCode() != 0 {
		t.Fatalf("expected a refined, ready plan (exit 0), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 4 {
		t.Fatalf("expected plan + assess + breakdown + assess (4 runs), got %d", runner.calls)
	}
	if fs.Exists("REFINEMENTS.md") {
		t.Fatal("expected REFINEMENTS.md to be cleared once the plan passes")
	}
	if !strings.Contains(fs.data["STEPS.md"], "Add storage") {
		t.Fatalf("expected STEPS.md to hold the broken-down steps, got %q", fs.data["STEPS.md"])
	}
}

func TestUserCanReviewExistingPlanThroughAnInterview(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("GOAL.md", "ship a safe import flow")
	fs.Write("PLAN.md", "Import all files")
	fs.Write("STEPS.md", "- [ ] Add import. Done when: it works")
	fs.Write("TESTS.md", validTestsDoc)
	prompter := &fakePrompter{answers: []string{"Skip invalid rows and report them"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("REFINEMENTS.md", "- Decide how partially invalid imports behave")
			fs.Write("REVIEW_QUESTIONS.md", "1. Should one invalid row reject the whole import, or should valid rows continue?\n")
		case 2:
			fs.Write("PLAN.md", "Import valid rows; skip and report invalid rows")
			fs.Write("STEPS.md", "- [ ] Add partial import reporting. Done when: valid rows persist and invalid rows appear in the summary")
			fs.Write("TESTS.md", validTestsDoc)
		case 3:
			fs.Write("REFINEMENTS.md", "NONE")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, reviewConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReviewed || outcome.ExitCode() != 0 {
		t.Fatalf("expected a reviewed plan (exit 0), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if len(prompter.asked) != 1 || !strings.Contains(prompter.asked[0], "invalid row") {
		t.Fatalf("expected one edge-case interview question, got %#v", prompter.asked)
	}
	for _, want := range []string{"Should one invalid row", "Skip invalid rows"} {
		if !strings.Contains(fs.data["REVIEW_ANSWERS.md"], want) {
			t.Fatalf("expected review answers to contain %q, got %q", want, fs.data["REVIEW_ANSWERS.md"])
		}
	}
	if !strings.Contains(fs.data["PLAN.md"], "skip and report invalid rows") {
		t.Fatalf("expected the answer to be reflected in PLAN.md, got %q", fs.data["PLAN.md"])
	}
	if fs.Exists("REVIEW_QUESTIONS.md") || fs.Exists("REFINEMENTS.md") {
		t.Fatal("expected transient review files to be cleared after review passes")
	}
}

func TestReviewRequiresAnExistingPlanAndSteps(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "plan without steps")
	runner := &fakeRunner{}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, reviewConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeMissingFiles || outcome.ExitCode() != 1 {
		t.Fatalf("expected missing files (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 || !strings.Contains(terminal.String(), "review requires PLAN.md and STEPS.md") {
		t.Fatalf("expected a clear error before invoking the tool, calls=%d output=%q", runner.calls, terminal.String())
	}
}

func TestReviewResumesAPendingInterview(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing plan")
	fs.Write("STEPS.md", "existing steps")
	fs.Write("TESTS.md", validTestsDoc)
	fs.Write("REVIEW_QUESTIONS.md", "1. Keep backward compatibility?\n")
	prompter := &fakePrompter{answers: []string{"Yes"}}
	runner := &fakeRunner{script: func(_ int, _ io.Writer) error {
		fs.Write("REFINEMENTS.md", "NONE")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, reviewConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReviewed {
		t.Fatalf("expected a resumed review to complete, got %v", outcome)
	}
	if !strings.Contains(fs.data["REVIEW_ANSWERS.md"], "Yes") || fs.Exists("REVIEW_QUESTIONS.md") {
		t.Fatalf("expected the pending answer to be recorded and question cleared, files=%#v", fs.data)
	}
}

// exhaustedCapFixture builds a create-mode run whose first call drafts the
// plan and whose every assessment keeps flagging the same finding, so pass 1
// of a 1-pass cap always reaches the exhaustion gate. cleanFromCall, when
// positive, makes assessments from that runner call on report NONE instead.
func exhaustedCapFixture(fs *fakeFileStore, cleanFromCall int) (*fakeRunner, models.PlanConfig) {
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 1
	runner := &fakeRunner{}
	runner.script = func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
			fs.Write("TESTS.md", validTestsDoc)
			return nil
		}
		if runner.prompt(call) != "assess" {
			return nil
		}
		if cleanFromCall > 0 && call >= cleanFromCall {
			fs.Write("REFINEMENTS.md", "NONE")
			return nil
		}
		fs.Write("REFINEMENTS.md", "- Still too big")
		return nil
	}
	return runner, cfg
}

func TestExhaustedCapWithoutAnswerStallsInsteadOfAccepting(t *testing.T) {
	fs := newFakeFileStore()
	runner, cfg := exhaustedCapFixture(fs, 0)
	// No scripted answers: the gate's question cannot be answered, so the
	// plan must stall — never silently reach OutcomePlanReady.
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled {
		t.Fatalf("expected an unanswered exhaustion gate to stall the plan, got %v", outcome)
	}
	// plan(1) + assess(2), then the gate blocks; nothing else may run.
	if runner.calls != 2 {
		t.Fatalf("expected no invocations after the gate, got %d", runner.calls)
	}
}

func TestExhaustedCapAcceptCompletesAndPublishesFindings(t *testing.T) {
	fs := newFakeFileStore()
	runner, cfg := exhaustedCapFixture(fs, 0)
	prompter := &fakePrompter{answers: []string{"accept"}}
	reporter := &fakeStatusReporter{}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected accept to complete the plan, got %v", outcome)
	}
	if runner.calls != 2 || fs.Exists("REFINEMENTS.md") {
		t.Fatalf("expected accept to end the loop and clear the assessment, calls=%d files=%#v", runner.calls, fs.data)
	}
	if !strings.Contains(terminal.String(), "- Still too big") {
		t.Fatalf("expected the finding on the terminal, got:\n%s", terminal.String())
	}
	found := false
	for _, event := range reporter.events {
		if event == "progress: unresolved finding: Still too big" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the finding on the status reporter, events=%v", reporter.events)
	}
}

func TestExhaustedCapRefineGrantsOnePassThenReasksWhileDirty(t *testing.T) {
	fs := newFakeFileStore()
	// Assessments stay dirty forever: the granted pass must re-reach the gate.
	runner, cfg := exhaustedCapFixture(fs, 0)
	prompter := &fakePrompter{answers: []string{"refine", "accept"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the second gate's accept to complete the plan, got %v", outcome)
	}
	// plan(1) + assess(2) + gate + refine(3) + assess(4) + gate again.
	if runner.calls != 4 || runner.prompt(3) != "refine" || runner.prompt(4) != "assess" {
		t.Fatalf("expected refine to grant exactly one more pass, invocations %v", runner.invocations)
	}
	if len(prompter.asked) != 2 || prompter.asked[0] != prompter.asked[1] {
		t.Fatalf("expected the still-dirty pass to re-ask the same gate question, asked %q", prompter.asked)
	}
	if !strings.Contains(prompter.asked[0], "accept") ||
		!strings.Contains(prompter.asked[0], "refine") ||
		!strings.Contains(prompter.asked[0], "edit") {
		t.Fatalf("expected the gate question to offer all three choices, got %q", prompter.asked[0])
	}
}

func TestExhaustedCapRefineCompletesOnCleanPass(t *testing.T) {
	fs := newFakeFileStore()
	// The granted pass converges: assess(4) reports NONE.
	runner, cfg := exhaustedCapFixture(fs, 4)
	prompter := &fakePrompter{answers: []string{"refine"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the clean granted pass to complete the plan, got %v", outcome)
	}
	if runner.calls != 4 || len(prompter.asked) != 1 {
		t.Fatalf("expected one granted pass and no re-ask, calls=%d asked=%q", runner.calls, prompter.asked)
	}
}

func TestExhaustedCapEditWaitsForEnterThenReassesses(t *testing.T) {
	fs := newFakeFileStore()
	// The user's manual edit resolves the finding: assess(3) reports NONE.
	runner, cfg := exhaustedCapFixture(fs, 3)
	prompter := &fakePrompter{answers: []string{"edit", ""}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected edit then a clean re-assessment to complete, got %v", outcome)
	}
	// plan(1) + assess(2) + gate + edit wait + assess(3): no refine invocation.
	if runner.calls != 3 || runner.prompt(3) != "assess" {
		t.Fatalf("expected edit to skip refine and re-assess, invocations %v", runner.invocations)
	}
	if len(prompter.asked) != 2 || !strings.Contains(prompter.asked[1], "PLAN.md") ||
		!strings.Contains(prompter.asked[1], "STEPS.md") {
		t.Fatalf("expected an edit-wait prompt naming the plan files, asked %q", prompter.asked)
	}
}

func TestExhaustedCapInvalidAnswerReasks(t *testing.T) {
	fs := newFakeFileStore()
	runner, cfg := exhaustedCapFixture(fs, 0)
	prompter := &fakePrompter{answers: []string{"maybe", "accept"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the re-asked gate to accept, got %v", outcome)
	}
	if len(prompter.asked) != 2 || prompter.asked[0] != prompter.asked[1] {
		t.Fatalf("expected the invalid answer to re-ask the same question, asked %q", prompter.asked)
	}
}

func TestPlanRefinementDisabledByZeroPasses(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0) // MaxRefinePasses is 0
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
			fs.Write("TESTS.md", validTestsDoc)
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected no assessment runs when refinement is disabled, got %d", runner.calls)
	}
}

func TestPlanRefinementAbortsWhenAssessorFails(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 5
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
			fs.Write("TESTS.md", validTestsDoc)
			return nil
		}
		return errors.New("claude: rate limited") // the assessment call fails
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort (exit 1) when the assessor fails, got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestPlanResumesWhenAlreadyComplete(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	fs.Write("TESTS.md", validTestsDoc)
	runner := &fakeRunner{}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected an already-complete plan to be ready, got %v", outcome)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool runs when the plan already exists, got %d", runner.calls)
	}
}

func TestPlanBackfillsMissingRecommendedTests(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the plan to be ready after backfilling tests, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected exactly one tests-only tool run, got %d", runner.calls)
	}
	if got := runner.invocations[0].Args[1]; got != "tests" {
		t.Fatalf("expected the tests invocation to run, got prompt %q", got)
	}
	if fs.data["TESTS.md"] != validTestsDoc {
		t.Fatalf("expected TESTS.md to be written, got %q", fs.data["TESTS.md"])
	}
	if fs.data["PLAN.md"] != "existing" || fs.data["STEPS.md"] != "existing" {
		t.Fatal("expected the existing plan and steps to be untouched")
	}
}

func TestPlanStallsWhenTestsBackfillProducesNothing(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	runner := &fakeRunner{} // succeeds but never writes TESTS.md
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected a stall when tests are never produced, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 1 {
		t.Fatalf("expected the loop to give up after one fruitless tests round, got %d", runner.calls)
	}
}

func TestPlanRegeneratesTestsWhenJourneyDiagramMissing(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("TESTS.md", "### Test 1: journey\n**Type:** Journey\nNo diagram.\n")
			return nil
		}
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan after regenerating tests, got %v", outcome)
	}
	if runner.calls != 2 {
		t.Fatalf("expected one regeneration round, got %d tool runs", runner.calls)
	}
	if fs.data["TESTS.md"] != validTestsDoc {
		t.Fatalf("expected the regenerated TESTS.md, got %q", fs.data["TESTS.md"])
	}
	if !strings.Contains(terminal.String(), "missing a sequence diagram") {
		t.Fatalf("expected a regeneration notice, got:\n%s", terminal.String())
	}
}

func TestPlanAssessesTestAlignmentWhenVerdictsAreMissing(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	unjudged := "### Test 1: journey\n**Type:** Journey\n" +
		"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n"
	judged := unjudged + "**Alignment:** partial\n**Alignment note:** only covers signup.\n"
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("TESTS.md", unjudged)
			return nil
		}
		fs.Write("TESTS.md", judged)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan once every test is judged, got %v", outcome)
	}
	if runner.calls != 2 {
		t.Fatalf("expected a tests run then one alignment run, got %d tool runs", runner.calls)
	}
	if got := runner.invocations[1].Args[1]; got != "align" {
		t.Fatalf("expected the alignment invocation second, got prompt %q", got)
	}
	if fs.data["TESTS.md"] != judged {
		t.Fatalf("expected the judged TESTS.md to remain, got %q", fs.data["TESTS.md"])
	}
}

func TestPlanSkipsAlignmentWhenTestsAreAlreadyJudged(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected no alignment run when verdicts exist, got %d tool runs", runner.calls)
	}
}

func TestPlanRealignsMisalignedTestsOnceWithoutAsking(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	misaligned := "### Test 1: journey\n**Type:** Journey\n" +
		"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
		"**Alignment:** misaligned\n**Alignment note:** proves logging, not the goal.\n"
	realigned := "### Test 1: journey\n**Type:** Journey\n" +
		"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
		"**Alignment:** partial\n**Alignment note:** now covers the goal path.\n"
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("TESTS.md", misaligned)
			return nil
		}
		fs.Write("TESTS.md", realigned)
		return nil
	}}
	prompter := &fakePrompter{}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan after the realign rewrite, got %v", outcome)
	}
	if runner.calls != 2 {
		t.Fatalf("expected a tests run then exactly one realign run, got %d tool runs", runner.calls)
	}
	if got := runner.invocations[1].Args[1]; got != "realign" {
		t.Fatalf("expected the realign invocation second, got prompt %q", got)
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no user questions during automatic realign, got %v", prompter.asked)
	}
	if fs.data["TESTS.md"] != realigned {
		t.Fatalf("expected the realigned TESTS.md to remain, got %q", fs.data["TESTS.md"])
	}
}

// stubbornTestsDoc keeps its first test misaligned no matter how often the
// realign invocation rewrites TESTS.md, so the gate always fires.
const stubbornTestsDoc = "### Test 1: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** misaligned\n**Alignment note:** proves logging, not the goal.\n\n" +
	"### Test 2: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n"

// droppedTestsDoc is stubbornTestsDoc with the misaligned section removed.
const droppedTestsDoc = "### Test 2: journey\n**Type:** Journey\n" +
	"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n" +
	"**Alignment:** aligned\n"

// containsEvent reports whether the fake reporter recorded the event.
func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

// misalignedGateFixture builds a run whose tests invocation (call 1) and
// automatic realign pass (call 2) both leave TESTS.md misaligned, so the gate
// asks. afterGate scripts every later runner call; nil keeps TESTS.md stubborn.
func misalignedGateFixture(fs *fakeFileStore, afterGate func(call int)) *fakeRunner {
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	return &fakeRunner{script: func(call int, _ io.Writer) error {
		if call <= 2 || afterGate == nil {
			fs.Write("TESTS.md", stubbornTestsDoc)
			return nil
		}
		afterGate(call)
		return nil
	}}
}

func TestMisalignedGateAcceptCompletesAndPublishesTests(t *testing.T) {
	fs := newFakeFileStore()
	runner := misalignedGateFixture(fs, nil)
	prompter := &fakePrompter{answers: []string{"accept"}}
	reporter := &fakeStatusReporter{}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0)).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected accept to complete the plan, got %v", outcome)
	}
	// tests(1) + automatic realign(2); accept must not invoke anything else.
	if runner.calls != 2 {
		t.Fatalf("expected no invocation after accept, got %d tool runs", runner.calls)
	}
	if len(prompter.asked) != 1 || !strings.Contains(prompter.asked[0], "accept") ||
		!strings.Contains(prompter.asked[0], "rewrite") || !strings.Contains(prompter.asked[0], "drop") {
		t.Fatalf("expected one accept/rewrite/drop question, got %v", prompter.asked)
	}
	finding := "progress: misaligned test: ### Test 1: journey — proves logging, not the goal."
	if !containsEvent(reporter.events, finding) {
		t.Fatalf("expected the misaligned test on the status reporter, got %v", reporter.events)
	}
	if !containsEvent(reporter.events, "wait-for-input") {
		t.Fatalf("expected the gate to signal wait-for-input, got %v", reporter.events)
	}
	if !strings.Contains(terminal.String(), "proves logging, not the goal.") {
		t.Fatalf("expected the alignment note on the terminal, got:\n%s", terminal.String())
	}
}

func TestMisalignedGateRewriteRealignsAgainThenCompletes(t *testing.T) {
	fs := newFakeFileStore()
	runner := misalignedGateFixture(fs, func(int) {
		fs.Write("TESTS.md", validTestsDoc)
	})
	prompter := &fakePrompter{answers: []string{"rewrite"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the plan to complete after the second rewrite, got %v", outcome)
	}
	if runner.calls != 3 {
		t.Fatalf("expected tests, auto realign, then one rewrite realign, got %d tool runs", runner.calls)
	}
	if got := runner.prompt(3); got != "realign" {
		t.Fatalf("expected rewrite to re-invoke the realign prompt, got %q", got)
	}
	if len(prompter.asked) != 1 {
		t.Fatalf("expected a clean re-check to ask nothing further, got %v", prompter.asked)
	}
}

func TestMisalignedGateDropRemovesSectionsViaConstrainedRealign(t *testing.T) {
	fs := newFakeFileStore()
	runner := misalignedGateFixture(fs, func(int) {
		fs.Write("TESTS.md", droppedTestsDoc)
	})
	prompter := &fakePrompter{answers: []string{"drop"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the plan to complete after the drop, got %v", outcome)
	}
	if runner.calls != 3 {
		t.Fatalf("expected tests, auto realign, then one drop invocation, got %d tool runs", runner.calls)
	}
	dropPrompt := runner.prompt(3)
	if !strings.HasPrefix(dropPrompt, "realign") {
		t.Fatalf("expected drop to constrain the realign prompt, got %q", dropPrompt)
	}
	if !strings.Contains(dropPrompt, "delete that entire section") {
		t.Fatalf("expected the drop constraint appended to the prompt, got %q", dropPrompt)
	}
	if fs.data["TESTS.md"] != droppedTestsDoc {
		t.Fatalf("expected the misaligned section removed from TESTS.md, got %q", fs.data["TESTS.md"])
	}
}

func TestMisalignedGateReasksWhileMisalignedVerdictsRemain(t *testing.T) {
	fs := newFakeFileStore()
	// Every rewrite keeps the misaligned verdict, so only accept can finish.
	runner := misalignedGateFixture(fs, nil)
	prompter := &fakePrompter{answers: []string{"rewrite", "rewrite", "accept"}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the explicit accept to complete the plan, got %v", outcome)
	}
	if len(prompter.asked) != 3 {
		t.Fatalf("expected the gate to re-ask after each dirty rewrite, got %v", prompter.asked)
	}
	// tests(1) + auto realign(2) + two granted rewrites(3,4).
	if runner.calls != 4 {
		t.Fatalf("expected each rewrite answer to run one realign invocation, got %d tool runs", runner.calls)
	}
}

func TestMisalignedGateInvalidAnswerReasks(t *testing.T) {
	fs := newFakeFileStore()
	runner := misalignedGateFixture(fs, nil)
	prompter := &fakePrompter{answers: []string{"whatever", "accept"}}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the retried accept to complete the plan, got %v", outcome)
	}
	if len(prompter.asked) != 2 {
		t.Fatalf("expected the invalid answer to re-ask the same question, got %v", prompter.asked)
	}
	if !strings.Contains(terminal.String(), "please answer accept, rewrite, or drop") {
		t.Fatalf("expected an invalid-answer notice, got:\n%s", terminal.String())
	}
}

func TestMisalignedGateWithoutAnswerStallsInsteadOfAccepting(t *testing.T) {
	fs := newFakeFileStore()
	runner := misalignedGateFixture(fs, nil)
	// No scripted answers: the gate's question cannot be answered, so the
	// plan must stall — never reach OutcomePlanReady with a standing
	// misaligned verdict absent an explicit accept.
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled {
		t.Fatalf("expected the unanswered gate to stall the plan, got %v", outcome)
	}
	// tests(1) + automatic realign(2); nothing may run past the gate.
	if runner.calls != 2 {
		t.Fatalf("expected no invocations after the stalled gate, got %d", runner.calls)
	}
}

func TestPlanSkipsRealignWhenNoTestIsMisaligned(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected no realign run when every verdict is aligned, got %d tool runs", runner.calls)
	}
}

func TestPlanStallsWhenAlignmentVerdictsStayMissing(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	unjudged := "### Test 1: journey\n**Type:** Journey\n" +
		"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", unjudged)
		return nil
	}}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected a stall when verdicts never appear, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the loop to give up after one alignment round, got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "still lack an alignment verdict") {
		t.Fatalf("expected a stall notice, got:\n%s", terminal.String())
	}
}

func TestPlanStallsWhenJourneyDiagramsStayMissing(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing")
	fs.Write("STEPS.md", "existing")
	badTests := "### Test 1: journey\n**Type:** Journey\nStill no diagram.\n"
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", badTests)
		return nil
	}}
	var terminal strings.Builder
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected a stall when diagrams never appear, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the loop to give up after two rounds, got %d", runner.calls)
	}
	if fs.data["TESTS.md"] != badTests {
		t.Fatalf("expected the last TESTS.md to remain for inspection, got %q", fs.data["TESTS.md"])
	}
	if !strings.Contains(terminal.String(), "still lack sequence diagrams") {
		t.Fatalf("expected a stall notice, got:\n%s", terminal.String())
	}
}

func TestReviewBackfillsMissingRecommendedTests(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "existing plan")
	fs.Write("STEPS.md", "existing steps")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("TESTS.md", validTestsDoc)
			return nil
		}
		fs.Write("REFINEMENTS.md", "NONE")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, reviewConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReviewed {
		t.Fatalf("expected a reviewed plan after backfilling tests, got %v", outcome)
	}
	if got := runner.invocations[0].Args[1]; got != "tests" {
		t.Fatalf("expected the first invocation to backfill tests, got prompt %q", got)
	}
	if fs.data["TESTS.md"] != validTestsDoc {
		t.Fatalf("expected TESTS.md to be written, got %q", fs.data["TESTS.md"])
	}
}

func TestPlanStallsWhenToolProducesNothing(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{} // writes neither questions nor a plan
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected a stall (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 1 {
		t.Fatalf("expected the loop to give up after one fruitless round, got %d", runner.calls)
	}
}

func TestPlanStallsWhenQuestionsCannotBeParsed(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("QUESTIONS.md", "What database?")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected unparseable questions to stall planning, got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestPlanInterruptedWhenUserCannotAnswerQuestion(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("QUESTIONS.md", "1. What database?\n")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeInterrupted || outcome.ExitCode() != 1 {
		t.Fatalf("expected closed input to interrupt planning, got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestPlanAbortsWhenToolFails(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error { return errors.New("claude: rate limited") }}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 1 {
		t.Fatalf("expected no retries with a zero failure cap, got %d calls", runner.calls)
	}
}

func TestPlanRetriesTransientToolFailure(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			return errors.New("droid: rate limited")
		}
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	terminal := &strings.Builder{}
	cfg := planConfig(0)
	cfg.MaxConsecutiveFailures = 3
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady || outcome.ExitCode() != 0 {
		t.Fatalf("expected a retried run to succeed, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the failed invocation to be retried once (2 calls), got %d", runner.calls)
	}
	for _, want := range []string{"droid: rate limited", "retrying", "(1 of 3 consecutive before aborting)"} {
		if !strings.Contains(terminal.String(), want) {
			t.Fatalf("expected the terminal to report %q, got:\n%s", want, terminal.String())
		}
	}
}

func TestPlanAbortsAfterConsecutiveFailureCap(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error { return errors.New("droid: crashed") }}
	terminal := &strings.Builder{}
	cfg := planConfig(0)
	cfg.MaxConsecutiveFailures = 3
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected exactly 3 attempts before aborting, got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "failed 3 consecutive time(s); stopping: droid: crashed") {
		t.Fatalf("expected the terminal to report the final failure, got:\n%s", terminal.String())
	}
}

func TestPlanWritesFailureReasonToIterationLog(t *testing.T) {
	fs := newFakeFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error { return errors.New("droid: exit status 1") }}
	logs := &fakeLogSink{}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, logs, io.Discard, planConfig(0))

	o.Run(context.Background())

	if len(logs.opened) != 1 {
		t.Fatalf("expected 1 iteration log, got %d", len(logs.opened))
	}
	got := logs.opened[0].buf.String()
	if !strings.Contains(got, "determined: tool invocation failed: droid: exit status 1") {
		t.Fatalf("expected the iteration log to record the failure reason, got:\n%s", got)
	}
}

func TestPlanStopsWhenBudgetExhausted(t *testing.T) {
	fs := newFakeFileStore()
	clock := &fakeClock{now: time.Now()}
	prompter := &fakePrompter{answers: []string{"an answer"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		clock.advance(10 * time.Minute)
		fs.Write("QUESTIONS.md", "1. Keep going?\n")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, clock, &fakeLogSink{}, io.Discard, planConfig(5*time.Minute))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeBudgetExceeded || outcome.ExitCode() != 1 {
		t.Fatalf("expected the budget to stop the run (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 1 {
		t.Fatalf("expected the budget to be checked between rounds (1 run), got %d", runner.calls)
	}
}

func TestPlanInterruptedByCancelledContext(t *testing.T) {
	fs := newFakeFileStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(ctx)

	if outcome != models.OutcomeInterrupted {
		t.Fatalf("expected a cancelled context to interrupt the run, got %v", outcome)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool runs after cancellation, got %d", runner.calls)
	}
}

// planWithAssumptions is a PLAN.md whose `## Assumptions` section the
// confirmation round relays to the user.
const planWithAssumptions = "the plan\n\n## Assumptions\n\n- SQLite database\n- no auth\n"

func TestPlanAssumptionsConfirmationProceedsToRefinement(t *testing.T) {
	for _, answer := range []string{"", "y"} {
		t.Run("answer "+answer, func(t *testing.T) {
			fs := newFakeFileStore()
			cfg := planConfig(0)
			cfg.MaxRefinePasses = 1
			prompter := &fakePrompter{answers: []string{answer}}
			runner := &fakeRunner{script: func(call int, _ io.Writer) error {
				switch call {
				case 1:
					fs.Write("PLAN.md", planWithAssumptions)
					fs.Write("STEPS.md", "the steps")
					fs.Write("TESTS.md", validTestsDoc)
				case 2:
					fs.Write("REFINEMENTS.md", "NONE")
				}
				return nil
			}}
			o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

			outcome := o.Run(context.Background())

			if outcome != models.OutcomePlanReady {
				t.Fatalf("expected a ready plan, got %v", outcome)
			}
			if len(prompter.asked) != 1 {
				t.Fatalf("expected exactly one assumptions question, got %d: %q", len(prompter.asked), prompter.asked)
			}
			if !strings.Contains(prompter.asked[0], "- SQLite database\n- no auth") {
				t.Fatalf("expected the question to relay the assumptions list, got %q", prompter.asked[0])
			}
			if runner.calls != 2 || runner.prompt(2) != "assess" {
				t.Fatalf("expected confirmation to go straight to the refinement assessment, got %d call(s), invocations %v", runner.calls, runner.invocations)
			}
		})
	}
}

func TestPlanAssumptionsCorrectionAnnotatesAndRepublishes(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"use Postgres, not SQLite"}}
	var annotationSeen string
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("PLAN.md", planWithAssumptions)
			fs.Write("STEPS.md", "the steps")
			fs.Write("TESTS.md", validTestsDoc)
		case 2:
			annotationSeen = fs.data["ANNOTATION.md"]
			fs.Write("PLAN.md", "the plan\n\n## Assumptions\n\n- Postgres database\n- no auth\n")
		}
		return nil
	}}
	reporter := &fakeStatusReporter{}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if len(prompter.asked) != 1 {
		t.Fatalf("expected the correction round to ask exactly once, got %d: %q", len(prompter.asked), prompter.asked)
	}
	if runner.calls != 2 || runner.prompt(2) != "annotate" {
		t.Fatalf("expected the correction to run the annotate invocation, got %d call(s), invocations %v", runner.calls, runner.invocations)
	}
	for _, want := range []string{"use Postgres, not SQLite", "plan (PLAN.md)", "Assumptions"} {
		if !strings.Contains(annotationSeen, want) {
			t.Fatalf("expected the staged annotation to record %q, got:\n%s", want, annotationSeen)
		}
	}
	if fs.Exists("ANNOTATION.md") {
		t.Fatal("expected ANNOTATION.md to be cleared after the annotate run")
	}
	if reporter.assumptions != "- Postgres database\n- no auth" {
		t.Fatalf("expected the corrected assumptions to be republished, got %q", reporter.assumptions)
	}
	if reporter.plan != "the plan\n\n## Assumptions\n\n- Postgres database\n- no auth\n" {
		t.Fatalf("expected the corrected plan to be republished, got %q", reporter.plan)
	}
}

// An assumptions correction must not regenerate the UI demo: refine is about
// to rewrite the plan, so the demo is generated once, after planning succeeds.
func TestAssumptionsCorrectionDefersDemoUntilAfterRefinement(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 1
	cfg.DemoFile = "DEMO.html"
	cfg.DemoInvocation = models.Invocation{Binary: "claude", Args: []string{"-p", "demo"}}
	prompter := &fakePrompter{answers: []string{"use Postgres, not SQLite"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("PLAN.md", planWithAssumptions)
			fs.Write("STEPS.md", "the steps")
			fs.Write("TESTS.md", validTestsDoc)
		case 2:
			fs.Write("PLAN.md", "the plan\n\n## Assumptions\n\n- Postgres database\n- no auth\n")
		case 3:
			fs.Write("REFINEMENTS.md", "NONE")
		case 4:
			fs.Write("DEMO.html", "<p>postgres demo</p>")
		}
		return nil
	}}
	reporter := &fakeStatusReporter{}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	want := []string{"plan", "annotate", "assess", "demo"}
	if got := invocationPrompts(runner); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("invocations = %v, want %v (the correction must not run the demo before refinement)", got, want)
	}
	if reporter.demo != "<p>postgres demo</p>" {
		t.Fatalf("expected the end-of-run demo to be published, got %q", reporter.demo)
	}
}

func TestPlanWithoutAssumptionsSectionSkipsConfirmation(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{} // any Ask would fail with io.EOF
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "the plan")
		fs.Write("STEPS.md", "the steps")
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no assumptions question for a plan without the section, got %q", prompter.asked)
	}
}

func TestPlanRefineRelaysAssessorQuestionsInCreateMode(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 3
	prompter := &fakePrompter{answers: []string{"SQLite"}}
	var answersAtRefine string
	var questionsPresentAtRefine bool
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // planning round drafts the full plan
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Add storage")
			fs.Write("TESTS.md", validTestsDoc)
		case 2: // assessment flags a preference-dependent finding and asks
			fs.Write("REFINEMENTS.md", "- Storage choice depends on user preference")
			fs.Write("QUESTIONS.md", "1. Prefer SQLite or Postgres?\n")
		case 3: // refine runs after the relay: capture what it can see
			answersAtRefine = fs.data["ANSWERS.md"]
			questionsPresentAtRefine = fs.Exists("QUESTIONS.md")
		case 4: // second assessment: clean
			fs.Write("REFINEMENTS.md", "NONE")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected a ready plan, got %v", outcome)
	}
	if runner.calls != 4 {
		t.Fatalf("expected plan + assess + refine + assess (4 runs), got %d", runner.calls)
	}
	if got := runner.prompt(2); got != "assess" {
		t.Fatalf("expected run 2 to be the assessment, got %q", got)
	}
	if got := runner.prompt(3); got != "refine" {
		t.Fatalf("expected run 3 to be the refinement, got %q", got)
	}
	if len(prompter.asked) != 1 || !strings.Contains(prompter.asked[0], "Prefer SQLite or Postgres?") {
		t.Fatalf("expected the assessor's question relayed to the user, got %q", prompter.asked)
	}
	for _, want := range []string{"Prefer SQLite or Postgres?", "SQLite"} {
		if !strings.Contains(answersAtRefine, want) {
			t.Fatalf("expected ANSWERS.md to hold %q before the refine invocation, got:\n%s", want, answersAtRefine)
		}
	}
	if questionsPresentAtRefine {
		t.Fatal("expected QUESTIONS.md cleared before the refine invocation")
	}
}
