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
		Operation:        models.PlanOperationCreate,
		Goal:             "build a todo CLI",
		Invocation:       models.Invocation{Binary: "claude", Args: []string{"-p", "plan"}},
		Budget:           budget,
		AssessInvocation: models.Invocation{Binary: "claude", Args: []string{"-p", "assess"}},
		RefineInvocation: models.Invocation{Binary: "claude", Args: []string{"-p", "refine"}},
		MaxRefinePasses:  0, // refinement off by default; refinement tests opt in
		GoalFile:         "GOAL.md",
		QuestionsFile:    "QUESTIONS.md",
		AnswersFile:      "ANSWERS.md",
		PlanFile:         "PLAN.md",
		StepsFile:        "STEPS.md",
		TestsFile:        "TESTS.md",
		AssessmentFile:   "REFINEMENTS.md",
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
		case 2: // first assessment: the step is too large
			fs.Write("REFINEMENTS.md", "- Step is too large: Build the entire app")
		case 3: // breakdown: rewrite STEPS.md into smaller steps
			fs.Write("STEPS.md", "1. Add storage\n2. Add CLI\n3. Wire up")
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
	prompter := &fakePrompter{answers: []string{"Skip invalid rows and report them"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("REFINEMENTS.md", "- Decide how partially invalid imports behave")
			fs.Write("REVIEW_QUESTIONS.md", "1. Should one invalid row reject the whole import, or should valid rows continue?\n")
		case 2:
			fs.Write("PLAN.md", "Import valid rows; skip and report invalid rows")
			fs.Write("STEPS.md", "- [ ] Add partial import reporting. Done when: valid rows persist and invalid rows appear in the summary")
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

func TestPlanRefinementStopsAtPassCap(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 2
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
			return nil
		}
		// Every assessment keeps flagging a too-large step: it never converges.
		fs.Write("REFINEMENTS.md", "- Still too big")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the usable plan to be returned when the cap is hit, got %v", outcome)
	}
	// plan(1) + assess(2) + breakdown(3) + assess(4), then cap stops it.
	if runner.calls != 4 {
		t.Fatalf("expected the cap of 2 passes to stop the loop after 4 runs, got %d", runner.calls)
	}
}

func TestPlanRefinementDisabledByZeroPasses(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0) // MaxRefinePasses is 0
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
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
