package services_test

import (
	"context"
	"io"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// These regression tests prove that sessions resumed from pre-change protocol
// files, and runs without a status reporter, pass through the new gates
// untouched: no gate asks, stalls, or panics when its trigger is absent.

// An old-format PLAN.md without a `## Assumptions` heading, resumed from disk,
// must skip the confirmation round entirely — no question, straight to
// refinement.
func TestOldFormatPlanWithoutAssumptionsSkipsConfirmationRound(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "1. Build everything")
	fs.Write("TESTS.md", validTestsDoc)
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 1
	prompter := &fakePrompter{} // any Ask would fail with io.EOF
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("REFINEMENTS.md", "NONE")
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the resumed old-format plan to complete, got %v", outcome)
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no assumptions question, got %q", prompter.asked)
	}
	if runner.calls != 1 || runner.prompt(1) != "assess" {
		t.Fatalf("expected the run to go straight to assessment, invocations %v", runner.invocations)
	}
}

// A refine loop whose assessment writes findings but no questions file must
// relay nothing to the user: the loop refines and converges without one Ask.
func TestRefineWithoutQuestionsFileRelaysNothing(t *testing.T) {
	fs := newFakeFileStore()
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 2
	prompter := &fakePrompter{} // any Ask would fail with io.EOF
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("PLAN.md", "the plan")
			fs.Write("STEPS.md", "1. Build everything")
			fs.Write("TESTS.md", validTestsDoc)
		case 2:
			fs.Write("REFINEMENTS.md", "- Tighten step 1")
		case 4:
			fs.Write("REFINEMENTS.md", "NONE")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the refine loop to converge, got %v", outcome)
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no relayed questions without QUESTIONS.md, got %q", prompter.asked)
	}
	if runner.calls != 4 || runner.prompt(3) != "refine" {
		t.Fatalf("expected plan, assess, refine, assess, invocations %v", runner.invocations)
	}
	if fs.Exists("ANSWERS.md") {
		t.Fatalf("expected no answers recorded, got %q", fs.data["ANSWERS.md"])
	}
}

// An old-format TESTS.md without alignment verdicts, resumed from disk, keeps
// the pre-existing behaviour: one align invocation judges it, and nothing new
// (realign, gate questions) runs when the verdicts come back clean.
func TestOldFormatTestsWithoutVerdictsKeepAlignmentBehavior(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "1. Build everything")
	fs.Write("TESTS.md", "### Test 1: journey\n**Type:** Journey\n"+
		"```mermaid\nsequenceDiagram\nUser->>App: open\n```\n")
	prompter := &fakePrompter{} // any Ask would fail with io.EOF
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("TESTS.md", validTestsDoc)
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected the resumed old-format tests to be judged and pass, got %v", outcome)
	}
	if runner.calls != 1 || runner.prompt(1) != "align" {
		t.Fatalf("expected exactly one align invocation, invocations %v", runner.invocations)
	}
	if len(prompter.asked) != 0 {
		t.Fatalf("expected no gate questions for cleanly judged tests, got %q", prompter.asked)
	}
}

// One run that fires every new gate — the misaligned-test gate, the assumptions
// confirmation, the question relay, and the exhausted refine-cap gate — must
// work with a nil PlanStatusReporter: no panic, every answer honoured.
func TestNewGatesRunWithNilStatusReporter(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", planWithAssumptions)
	fs.Write("STEPS.md", "1. Build everything")
	fs.Write("TESTS.md", stubbornTestsDoc)
	cfg := planConfig(0)
	cfg.MaxRefinePasses = 1
	prompter := &fakePrompter{answers: []string{
		"accept", // misaligned-test gate after the automatic realign
		"",       // assumptions confirmation via bare Enter
		"SQLite", // relayed assessor question
		"accept", // exhausted refine-cap gate
	}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 { // the assessment; call 1 is the automatic realign
			fs.Write("REFINEMENTS.md", "- Still too big")
			fs.Write("QUESTIONS.md", "1. Prefer SQLite or Postgres?\n")
		}
		return nil
	}}
	o := services.NewPlanOrchestrator(runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomePlanReady {
		t.Fatalf("expected every gate to honour its answer without a reporter, got %v", outcome)
	}
	// realign(1) from the stubborn misaligned verdict, then assess(2).
	if runner.calls != 2 || runner.prompt(1) != "realign" || runner.prompt(2) != "assess" {
		t.Fatalf("expected realign then assess, invocations %v", runner.invocations)
	}
	if len(prompter.asked) != 4 {
		t.Fatalf("expected all four gates to ask, got %d: %q", len(prompter.asked), prompter.asked)
	}
}
