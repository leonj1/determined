package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// These fakes stay in package main because they exercise the unexported
// runAutoExec seam; Go requires same-package tests for that access.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakePlanExecutor struct {
	status  services.ExecStatusReporter
	outcome models.Outcome
}

func (f *fakePlanExecutor) Execute(_ context.Context, status services.ExecStatusReporter) models.Outcome {
	f.status = status
	return f.outcome
}

type fakeStdin struct {
	reads   chan byte
	started chan struct{}
}

func newFakeStdin() *fakeStdin {
	return &fakeStdin{reads: make(chan byte), started: make(chan struct{})}
}

func (f *fakeStdin) Read(buf []byte) (int, error) {
	f.started <- struct{}{}
	buf[0] = <-f.reads
	return 1, nil
}

func TestUserCanRunUpdateCommand(t *testing.T) {
	if !isUpdateCommand([]string{"determined", "update"}) {
		t.Fatal("update subcommand should be recognized")
	}
}

func TestNormalRunIsNotUpdateCommand(t *testing.T) {
	if isUpdateCommand([]string{"determined", "--version"}) {
		t.Fatal("normal flags should not be treated as update")
	}
}

func TestUserCanRunInitCommand(t *testing.T) {
	if !isInitCommand([]string{"determined", "init"}) {
		t.Fatal("init subcommand should be recognized")
	}
}

func TestNormalRunIsNotInitCommand(t *testing.T) {
	if isInitCommand([]string{"determined", "--version"}) {
		t.Fatal("normal flags should not be treated as init")
	}
}

func TestUserCanSelectMVPPlanning(t *testing.T) {
	mode, err := selectPlanMode(true, false, true, false)
	if err != nil || mode != models.PlanModeMVP {
		t.Fatalf("expected MVP planning, got mode %q and error %v", mode, err)
	}
}

func TestUserCanSelectPrototypePlanning(t *testing.T) {
	mode, err := selectPlanMode(true, false, false, true)
	if err != nil || mode != models.PlanModePrototype {
		t.Fatalf("expected prototype planning, got mode %q and error %v", mode, err)
	}
}

func TestUserCannotCombinePlanningModes(t *testing.T) {
	if _, err := selectPlanMode(true, false, true, true); err == nil {
		t.Fatal("expected combined MVP and prototype modes to be rejected")
	}
}

func TestUserCannotSelectPlanningModeDuringExecution(t *testing.T) {
	if _, err := selectPlanMode(false, false, true, false); err == nil {
		t.Fatal("expected MVP without -plan to be rejected")
	}
}

func TestUserCannotCreateAndReviewPlanTogether(t *testing.T) {
	if _, err := selectPlanMode(true, true, false, false); err == nil {
		t.Fatal("expected -plan and -review-plan together to be rejected")
	}
}

func TestUserCannotApplyCreationModesToReview(t *testing.T) {
	if _, err := selectPlanMode(false, true, true, false); err == nil {
		t.Fatal("expected -mvp with -review-plan to be rejected")
	}
}

func TestUserMustRequestPlanReviewOrExecution(t *testing.T) {
	if operationRequested(false, false, false, false) {
		t.Fatal("no flags should leave no operation requested")
	}
}

func TestUserGetsExecutionByDefault(t *testing.T) {
	if !executeRequested(false, false, false, false) {
		t.Fatal("no flags should default to the execute loop")
	}
}

func TestUserExecFlagStillRequestsExecution(t *testing.T) {
	if !executeRequested(true, false, false, false) {
		t.Fatal("-exec alone should request the execute loop")
	}
}

func TestUserPlanAloneDoesNotDefaultToExecution(t *testing.T) {
	if executeRequested(false, true, false, false) {
		t.Fatal("-plan alone should not enter the execute loop")
	}
}

func TestUserReviewAloneDoesNotDefaultToExecution(t *testing.T) {
	if executeRequested(false, false, true, false) {
		t.Fatal("-review-plan alone should not enter the execute loop")
	}
}

func TestUserCriteriaAloneDoesNotDefaultToExecution(t *testing.T) {
	if executeRequested(false, false, false, true) {
		t.Fatal("-criteria alone should not enter the execute loop")
	}
}

func TestUserCanRequestExecutionAlone(t *testing.T) {
	if !operationRequested(false, false, true, false) {
		t.Fatal("-exec alone should request the execute loop")
	}
}

func TestUserCanRequestPlanningAlone(t *testing.T) {
	if !operationRequested(true, false, false, false) {
		t.Fatal("-plan alone should request planning")
	}
}

func TestUserCanRequestReviewAlone(t *testing.T) {
	if !operationRequested(false, true, false, false) {
		t.Fatal("-review-plan alone should request a plan review")
	}
}

func TestUserCanRequestCriteriaAlone(t *testing.T) {
	if !operationRequested(false, false, false, true) {
		t.Fatal("-criteria alone should request a criteria session")
	}
}

func TestUserCannotCombineCriteriaWithReview(t *testing.T) {
	if err := validateCriteriaFlag(true, true); err == nil {
		t.Fatal("expected -criteria with -review-plan to be rejected")
	}
}

func TestUserCanCombineCriteriaWithPlanning(t *testing.T) {
	if err := validateCriteriaFlag(true, false); err != nil {
		t.Fatalf("-criteria without -review-plan should be accepted: %v", err)
	}
}

func TestFinishedCriteriaSessionContinuesIntoPlanning(t *testing.T) {
	if !criteriaAllowsContinuation(models.OutcomeCriteriaReady) {
		t.Fatal("a finished criteria session should allow planning to run")
	}
}

func TestCancelledCriteriaSessionStillContinues(t *testing.T) {
	if !criteriaAllowsContinuation(models.OutcomeCriteriaCancelled) {
		t.Fatal("cancel discards the session's tests, not the requested run")
	}
}

func TestAbortedCriteriaSessionStopsTheRun(t *testing.T) {
	for _, outcome := range []models.Outcome{
		models.OutcomeInterrupted,
		models.OutcomeBudgetExceeded,
		models.OutcomeDroidFailed,
		models.OutcomeCriteriaStalled,
	} {
		if criteriaAllowsContinuation(outcome) {
			t.Fatalf("outcome %v should stop the run before planning or execution", outcome)
		}
	}
}

func TestUserCannotExecuteDuringReview(t *testing.T) {
	if err := validateExecFlag(true, true); err == nil {
		t.Fatal("expected -exec with -review-plan to be rejected")
	}
}

func TestUserCanCombinePlanningWithExecution(t *testing.T) {
	if err := validateExecFlag(false, true); err != nil {
		t.Fatalf("-exec without -review-plan should be accepted: %v", err)
	}
}

func TestSuccessfulPlanningContinuesIntoExecution(t *testing.T) {
	if !shouldExecuteAfterPlan(true, false, models.OutcomePlanReady) {
		t.Fatal("-plan -exec should execute once the plan is ready")
	}
}

func TestFailedPlanningSkipsExecution(t *testing.T) {
	if shouldExecuteAfterPlan(true, false, models.OutcomePlanStalled) {
		t.Fatal("a stalled plan run should not continue into execution")
	}
}

func TestPlanningWithoutExecFlagStopsAfterPlanning(t *testing.T) {
	if shouldExecuteAfterPlan(false, false, models.OutcomePlanReady) {
		t.Fatal("without -exec, planning should not continue into execution")
	}
}

func TestInteractivePlanningDoesNotStartASecondExecution(t *testing.T) {
	if shouldExecuteAfterPlan(true, true, models.OutcomePlanReady) {
		t.Fatal("interactive -plan -exec should execute only inside the live session")
	}
}

func TestInteractivePostPlanActionMatchesRequestedFlow(t *testing.T) {
	tests := []struct {
		name      string
		executing bool
		outcome   models.Outcome
		want      postPlanAction
	}{
		{"ready plan offers implementation", false, models.OutcomePlanReady, postPlanOffer},
		{"ready plan executes automatically", true, models.OutcomePlanReady, postPlanAutoExec},
		{"stalled auto-exec plan dismisses", true, models.OutcomePlanStalled, postPlanDismiss},
		{"stalled plan-only session dismisses", false, models.OutcomePlanStalled, postPlanDismiss},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := postPlanActionFor(test.executing, test.outcome); got != test.want {
				t.Fatalf("post-plan action = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAutomaticExecutionUsesLiveStatusAndPropagatesOutcome(t *testing.T) {
	status := services.NewPlanStatusService(fixedClock{}, models.GitContext{}, models.ToolIdentity{})
	executor := &fakePlanExecutor{outcome: models.OutcomeStalled}
	held := false
	hold := func(_ context.Context) { held = true }

	got := runAutoExec(context.Background(), status, executor.Execute, hold)

	if executor.status != status {
		t.Fatal("automatic execution did not receive the session status reporter")
	}
	if got != executor.outcome {
		t.Fatalf("automatic execution outcome = %v, want %v", got, executor.outcome)
	}
	if !held {
		t.Fatal("automatic execution did not hold the completed status page")
	}
}

func TestAutomaticExecutionStartsDismissalWaitAfterExecution(t *testing.T) {
	executed := false
	execute := func(_ context.Context, _ services.ExecStatusReporter) models.Outcome {
		executed = true
		return models.OutcomeStopped
	}
	hold := func(_ context.Context) {
		if !executed {
			t.Fatal("dismissal wait started before execution finished")
		}
	}

	runAutoExec(context.Background(), nil, execute, hold)
}

func TestCompletedExecutionPagePromptsForDismissal(t *testing.T) {
	var output bytes.Buffer
	hold := completedStatusPageHolder("http://status.test", &output, bytes.NewReader(nil))
	hold(context.Background())

	want := "determined: execution finished; status page still serving at http://status.test — press Enter to exit\n"
	if got := output.String(); got != want {
		t.Fatalf("completion prompt = %q, want %q", got, want)
	}
}

func TestDismissalChannelIgnoresEnterDuringExecution(t *testing.T) {
	input := newFakeStdin()
	ready := make(chan struct{})
	dismissed := dismissalChannelWhenReady(input, ready)
	<-input.started
	input.reads <- '\n'
	<-input.started // the early Enter was discarded and the next read began

	close(ready)
	select {
	case <-dismissed:
		t.Fatal("Enter during execution dismissed the completed page")
	default:
	}

	input.reads <- '\n'
	<-dismissed
}

func TestPrototypeSkipsQualityRefinement(t *testing.T) {
	if got := refinePasses(models.PlanModePrototype, 5); got != 0 {
		t.Fatalf("prototype refinement passes = %d, want 0", got)
	}
}

func TestMVPStillUsesConfiguredQualityRefinement(t *testing.T) {
	if got := refinePasses(models.PlanModeMVP, 3); got != 3 {
		t.Fatalf("MVP refinement passes = %d, want 3", got)
	}
}

func TestMaxStepPassesDefaultsToTwo(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	maxStepPasses := registerMaxStepPassesFlag(flags)

	if *maxStepPasses != 2 {
		t.Fatalf("max step passes default = %d, want 2", *maxStepPasses)
	}
}

func TestUserCanSetMaxDurationWithShortFlag(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	budget := registerBudgetFlags(flags)

	if err := flags.Parse([]string{"-t", "2h"}); err != nil {
		t.Fatalf("short max-duration flag should parse: %v", err)
	}
	if *budget != 2*time.Hour {
		t.Fatalf("short max-duration flag set %v, want 2h", *budget)
	}
}

func TestUserCanSetMaxDurationWithLongFlag(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	budget := registerBudgetFlags(flags)

	if err := flags.Parse([]string{"--max-duration", "3h"}); err != nil {
		t.Fatalf("long max-duration flag should parse: %v", err)
	}
	if *budget != 3*time.Hour {
		t.Fatalf("long max-duration flag set %v, want 3h", *budget)
	}
}

func TestUserCanSelectInitialization(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	initialize := registerInitFlag(flags)

	if err := flags.Parse([]string{"-init"}); err != nil {
		t.Fatalf("init flag should parse: %v", err)
	}
	if !*initialize {
		t.Fatal("-init should select initialization")
	}
}

func TestInitializationUsesPersonalKnowledgeDestinations(t *testing.T) {
	cfg := initializationConfig("/home/jose")

	if len(cfg.Documents) != 2 {
		t.Fatalf("initialization document count = %d, want 2", len(cfg.Documents))
	}
	if cfg.Documents[0].Destination != "/home/jose/.claude/CLAUDE.md" {
		t.Fatalf("unexpected Claude destination %q", cfg.Documents[0].Destination)
	}
	if cfg.Documents[1].Destination != "/home/jose/AGENTS.md" {
		t.Fatalf("unexpected Agents destination %q", cfg.Documents[1].Destination)
	}
	if cfg.Documents[0].Source != "https://raw.githubusercontent.com/leonj1/open-doc-format/master/personal-knowledge/CLAUDE.md" {
		t.Fatalf("unexpected Claude source %q", cfg.Documents[0].Source)
	}
	if cfg.Documents[1].Source != "https://raw.githubusercontent.com/leonj1/open-doc-format/master/personal-knowledge/AGENTS.md" {
		t.Fatalf("unexpected Agents source %q", cfg.Documents[1].Source)
	}
}

func TestPlanInputPreservesTrailingWordsSplitByShell(t *testing.T) {
	got := planInput("#", []string{"Goal", "Build", "the", "TODO", "CLI"})
	want := "# Goal Build the TODO CLI"

	if got != want {
		t.Fatalf("plan input = %q, want %q", got, want)
	}
}

func TestPlanInputKeepsQuotedValueWhenNoTrailingWords(t *testing.T) {
	got := planInput("build a todo CLI", nil)

	if got != "build a todo CLI" {
		t.Fatalf("plan input = %q, want the quoted value unchanged", got)
	}
}

func TestUserCannotUseInteractiveWithoutPlan(t *testing.T) {
	if err := validateInteractiveFlag(true, false); err == nil {
		t.Fatal("expected -interactive without -plan to be rejected")
	}
}

func TestUserCanUseInteractiveWithPlan(t *testing.T) {
	if err := validateInteractiveFlag(true, true); err != nil {
		t.Fatalf("expected -interactive with -plan to be accepted, got %v", err)
	}
}

func TestPlanWithoutInteractiveIsUnaffected(t *testing.T) {
	if err := validateInteractiveFlag(false, true); err != nil {
		t.Fatalf("expected plain -plan to be accepted, got %v", err)
	}
}
