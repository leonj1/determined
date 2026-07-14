package services_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func criteriaConfig(budget time.Duration) models.CriteriaConfig {
	return models.CriteriaConfig{
		Invocation:   models.Invocation{Binary: "claude", Args: []string{"-p", "criteria"}},
		Budget:       budget,
		CriteriaFile: "CRITERIA.md",
		RequestFile:  "CRITERIA_REQUEST.md",
		DraftFile:    "CRITERIA_DRAFT.md",
	}
}

func newCriteriaOrchestrator(runner *fakeRunner, fs *fakeFileStore, prompter *fakePrompter) *services.CriteriaOrchestrator {
	return services.NewCriteriaOrchestrator(
		runner, fs, prompter, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, criteriaConfig(0))
}

// draftingRunner writes a distinct Gherkin draft on every call, standing in
// for the tool converting each journey description into a BDD test.
func draftingRunner(fs *fakeFileStore) *fakeRunner {
	return &fakeRunner{script: func(call int, _ io.Writer) error {
		fs.Write("CRITERIA_DRAFT.md", fmt.Sprintf("Feature: journey %d\n", call))
		return nil
	}}
}

func TestUserCanAcceptTestsAndEndTheSession(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"login journey", "a", "checkout journey", "e"}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaReady || outcome.ExitCode() != 0 {
		t.Fatalf("expected ready criteria (exit 0), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	criteria := fs.data["CRITERIA.md"]
	for _, expected := range []string{"# Acceptance Criteria", "## Test 1", "Feature: journey 1", "## Test 2", "Feature: journey 2"} {
		if !strings.Contains(criteria, expected) {
			t.Fatalf("expected CRITERIA.md to contain %q, got:\n%s", expected, criteria)
		}
	}
	if fs.Exists("CRITERIA_REQUEST.md") || fs.Exists("CRITERIA_DRAFT.md") {
		t.Fatal("expected the transient request and draft files to be removed")
	}
}

func TestUserCanModifyAProposalBeforeAccepting(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"login journey", "m", "require MFA", "a", ""}}
	revisionSeen := ""
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			revisionSeen = fs.data["CRITERIA_REQUEST.md"]
		}
		fs.Write("CRITERIA_DRAFT.md", fmt.Sprintf("Feature: login v%d\n", call))
		return nil
	}}
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaReady {
		t.Fatalf("expected ready criteria, got %v", outcome)
	}
	if runner.calls != 2 {
		t.Fatalf("expected a redraft after the modify verdict (2 tool runs), got %d", runner.calls)
	}
	for _, expected := range []string{"# Journey", "login journey", "## Revision", "require MFA"} {
		if !strings.Contains(revisionSeen, expected) {
			t.Fatalf("expected the redraft to see %q in the request file, got:\n%s", expected, revisionSeen)
		}
	}
	if !strings.Contains(fs.data["CRITERIA.md"], "Feature: login v2") {
		t.Fatalf("expected the revised draft to be the accepted test, got:\n%s", fs.data["CRITERIA.md"])
	}
}

func TestUserCanSkipAProposalAndDescribeAnother(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"journey one", "s", "journey two", "a", ""}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaReady {
		t.Fatalf("expected ready criteria, got %v", outcome)
	}
	criteria := fs.data["CRITERIA.md"]
	if strings.Contains(criteria, "Feature: journey 1") {
		t.Fatalf("expected the skipped draft to be forgotten, got:\n%s", criteria)
	}
	if !strings.Contains(criteria, "## Test 1") || !strings.Contains(criteria, "Feature: journey 2") {
		t.Fatalf("expected only the accepted test recorded as Test 1, got:\n%s", criteria)
	}
}

func TestUserCanCancelDiscardingTheSessionsTests(t *testing.T) {
	fs := newFakeFileStore()
	original := "# Acceptance Criteria\n\nEach BDD journey test below must exist as an automated test and pass.\n\n## Test 1\n\n```gherkin\nFeature: earlier session\n```\n\n"
	fs.Write("CRITERIA.md", original)
	prompter := &fakePrompter{answers: []string{"new journey", "a", "another journey", "c"}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaCancelled || outcome.ExitCode() != 0 {
		t.Fatalf("expected a cancelled session (exit 0), got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if fs.data["CRITERIA.md"] != original {
		t.Fatalf("expected CRITERIA.md restored to its pre-session content, got:\n%s", fs.data["CRITERIA.md"])
	}
}

func TestCancelWithoutPriorCriteriaRemovesTheFile(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"a journey", "a", "another journey", "c"}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaCancelled {
		t.Fatalf("expected a cancelled session, got %v", outcome)
	}
	if fs.Exists("CRITERIA.md") {
		t.Fatal("expected CRITERIA.md removed when no criteria predated the session")
	}
}

func TestFinishingWithoutAcceptedTestsWritesNothing(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{""}}
	runner := &fakeRunner{}
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaCancelled {
		t.Fatalf("expected an empty session to end as cancelled, got %v", outcome)
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool runs without a journey description, got %d", runner.calls)
	}
	if fs.Exists("CRITERIA.md") {
		t.Fatal("expected no CRITERIA.md from an empty session")
	}
}

func TestAcceptedTestsContinueNumberingFromEarlierSessions(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("CRITERIA.md", "# Acceptance Criteria\n\n## Test 1\n\n```gherkin\nFeature: earlier\n```\n\n")
	prompter := &fakePrompter{answers: []string{"a journey", "e"}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaReady {
		t.Fatalf("expected ready criteria, got %v", outcome)
	}
	if !strings.Contains(fs.data["CRITERIA.md"], "## Test 2") {
		t.Fatalf("expected the new test recorded as Test 2, got:\n%s", fs.data["CRITERIA.md"])
	}
}

func TestSessionStallsWhenTheToolWritesNoDraft(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"a journey"}}
	runner := &fakeRunner{} // never writes CRITERIA_DRAFT.md
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaStalled || outcome.ExitCode() != 1 {
		t.Fatalf("expected a stalled session (exit 1), got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestUnrecognisedVerdictIsAskedAgain(t *testing.T) {
	fs := newFakeFileStore()
	prompter := &fakePrompter{answers: []string{"a journey", "x", "accept", ""}}
	runner := draftingRunner(fs)
	o := newCriteriaOrchestrator(runner, fs, prompter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCriteriaReady {
		t.Fatalf("expected ready criteria after re-asking the verdict, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected re-asking not to rerun the tool, got %d runs", runner.calls)
	}
	if !strings.Contains(fs.data["CRITERIA.md"], "## Test 1") {
		t.Fatalf("expected the accepted test recorded, got:\n%s", fs.data["CRITERIA.md"])
	}
}

func TestSessionEndsWhenBudgetExpires(t *testing.T) {
	fs := newFakeFileStore()
	clock := &fakeClock{now: time.Now()}
	prompter := &fakePrompter{answers: []string{"a journey", "a", "another journey"}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		fs.Write("CRITERIA_DRAFT.md", "Feature: something\n")
		clock.advance(time.Hour)
		return nil
	}}
	o := services.NewCriteriaOrchestrator(
		runner, fs, prompter, clock, &fakeLogSink{}, io.Discard, criteriaConfig(30*time.Minute))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeBudgetExceeded {
		t.Fatalf("expected the budget to end the session, got %v", outcome)
	}
	if !strings.Contains(fs.data["CRITERIA.md"], "## Test 1") {
		t.Fatalf("expected the already-accepted test to survive the budget stop, got:\n%s", fs.data["CRITERIA.md"])
	}
}

func TestCriteriaPromptDrivesTheFileProtocol(t *testing.T) {
	prompt := services.CriteriaPrompt()
	for _, expected := range []string{"CRITERIA_REQUEST.md", "CRITERIA_DRAFT.md", "Gherkin", "## Revision", "Do not implement"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected the criteria prompt to contain %q", expected)
		}
	}
}

func TestPlanningTreatsCriteriaAsRequiredAcceptanceTests(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Plan
	for _, expected := range []string{"CRITERIA.md", "required acceptance tests"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected the planning prompt to contain %q", expected)
		}
	}
}
