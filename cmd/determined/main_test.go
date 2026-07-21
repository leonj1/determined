package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type fakeDocsPublisher struct{ published bool }

func (p *fakeDocsPublisher) Publish(sink services.PlanDocumentSink) {
	p.published = true
	sink.SetGoal("resume the existing plan")
	sink.SetPlan("# Plan")
	sink.SetDemo("<button>Demo</button>")
	sink.SetTests("# Tests")
	sink.SetTaskSteps([]models.TaskStep{{Text: "retry execution"}})
}

func (f *fakePlanExecutor) Execute(_ context.Context, status services.ExecStatusReporter) models.Outcome {
	f.status = status
	return f.outcome
}

type fakeStdin struct {
	reads   chan byte
	started chan struct{}
}

const cliArgsEnvironment = "DETERMINED_TEST_CLI_ARGS"

func TestCLIProcess(t *testing.T) {
	encoded, requested := os.LookupEnv(cliArgsEnvironment)
	if !requested {
		return
	}
	flag.CommandLine = flag.NewFlagSet("determined", flag.ExitOnError)
	os.Args = append([]string{"determined"}, strings.Split(encoded, "\t")...)
	main()
}

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$")
	command.Env = append(os.Environ(), cliArgsEnvironment+"="+strings.Join(args, "\t"))
	output, err := command.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run determined: %v", err)
	}
	return exitError.ExitCode(), string(output)
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

func TestChatFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "chat alone", err: validateChatFlags(true, "", false, false, false, false, false)},
		{name: "one shot", err: validateChatFlags(true, "status", false, false, false, false, false)},
	}
	for _, test := range tests {
		if test.err != nil {
			t.Errorf("%s: unexpected error %v", test.name, test.err)
		}
	}
	for name, err := range map[string]error{
		"message without chat":  validateChatFlags(false, "hello", false, false, false, false, false),
		"chat with plan":        validateChatFlags(true, "", true, false, false, false, false),
		"chat with exec":        validateChatFlags(true, "", false, true, false, false, false),
		"chat with review":      validateChatFlags(true, "", false, false, true, false, false),
		"chat with criteria":    validateChatFlags(true, "", false, false, false, true, false),
		"chat with interactive": validateChatFlags(true, "", false, false, false, false, true),
	} {
		if err == nil {
			t.Errorf("%s: expected usage error", name)
		}
	}
}

func TestMessageWithoutChatExitsAsUsageError(t *testing.T) {
	code, output := runCLI(t, "-m", "hello")
	if code != 2 || !strings.Contains(output, "-m requires -chat") {
		t.Fatalf("code=%d output=%q, want usage error", code, output)
	}
}

func TestChatWithExecutionExitsAsUsageError(t *testing.T) {
	code, output := runCLI(t, "-chat", "-exec")
	if code != 2 || !strings.Contains(output, "-chat cannot be combined") {
		t.Fatalf("code=%d output=%q, want usage error", code, output)
	}
}

func TestChatWithoutLiveSessionExitsOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, output := runCLI(t, "-chat", "-m", "status")
	if code != 1 || !strings.Contains(output, "no running interactive session found") {
		t.Fatalf("code=%d output=%q, want no-session failure", code, output)
	}
}

func TestHeadlessExecutionReceivesLiveStatusAndCleansUpSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tool, err := models.SelectTool(models.ToolNameDroid, models.ToolOptions{})
	if err != nil {
		t.Fatalf("select tool: %v", err)
	}
	executor := &fakePlanExecutor{outcome: models.OutcomeStopped}
	outcome := runHeadlessExec(context.Background(), tool, executor.Execute, fixedClock{now: time.Now()})

	if outcome != models.OutcomeStopped || executor.status == nil {
		t.Fatalf("outcome=%v status=%v, want execution with live reporter", outcome, executor.status)
	}
	if _, err := os.Stat(filepath.Join(home, ".determined", "session.json")); !os.IsNotExist(err) {
		t.Fatalf("session record remains after execution: %v", err)
	}
}

func TestHeadlessExecutionContinuesWhenStatusServerCouldNotStart(t *testing.T) {
	executor := &fakePlanExecutor{outcome: models.OutcomeStopped}
	cleaned := false
	outcome := continueHeadlessExec(context.Background(), nil, executor.Execute, func() { cleaned = true }, false, nil)

	if outcome != models.OutcomeStopped || executor.status != nil {
		t.Fatalf("outcome=%v status=%v, want unchanged execution without reporter", outcome, executor.status)
	}
	if cleaned {
		t.Fatal("cleanup should not run for a server that never started")
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

func TestUserCanUseOneModelForPlanningAndAnotherForExecution(t *testing.T) {
	planning, err := models.SelectTool("droid", models.ToolOptions{Model: "plan-model"})
	if err != nil {
		t.Fatalf("select planning model: %v", err)
	}
	execution, err := selectExecTool("droid", "exec-model", planning)
	if err != nil {
		t.Fatalf("select execution model: %v", err)
	}
	if planning.Identity().Model != "plan-model" {
		t.Fatalf("planning model = %q, want plan-model", planning.Identity().Model)
	}
	if execution.Identity().Model != "exec-model" {
		t.Fatalf("execution model = %q, want exec-model", execution.Identity().Model)
	}
}

func TestExecutionFallsBackToPlanningTool(t *testing.T) {
	planning, err := models.SelectTool("claude", models.ToolOptions{Model: "opus"})
	if err != nil {
		t.Fatalf("select planning model: %v", err)
	}
	execution, err := selectExecTool("claude", "", planning)
	if err != nil {
		t.Fatalf("select execution model: %v", err)
	}
	if execution != planning {
		t.Fatal("execution should reuse the planning tool when -exec-model is empty")
	}
}

func TestUserCannotSelectExecutionModelForPi(t *testing.T) {
	planning, err := models.SelectTool("pi", models.ToolOptions{})
	if err != nil {
		t.Fatalf("select pi: %v", err)
	}
	if _, err := selectExecTool("pi", "opus", planning); err == nil {
		t.Fatal("expected -exec-model with pi to be rejected")
	}
}

func TestPiExecutionModelExitsAsUsageError(t *testing.T) {
	code, output := runCLI(t, "-tool", "pi", "-exec-model", "opus")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; output: %s", code, output)
	}
	if !strings.Contains(output, "-exec-model") || !strings.Contains(output, "pi") {
		t.Fatalf("usage error should identify -exec-model and pi: %s", output)
	}
}

func TestUserCannotSelectExecutionModelWithoutExecution(t *testing.T) {
	if err := validateExecModelFlag("opus", false); err == nil {
		t.Fatal("expected -exec-model without execution to be rejected")
	}
}

func TestExecutionModelWithoutExecutionExitsAsUsageError(t *testing.T) {
	code, output := runCLI(t, "-review-plan", "-exec-model", "opus")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; output: %s", code, output)
	}
	if !strings.Contains(output, "-exec-model requires an execution phase") {
		t.Fatalf("usage error should explain the missing execution phase: %s", output)
	}
}

func TestUserCanSelectExecutionModelWhenExecuting(t *testing.T) {
	if err := validateExecModelFlag("opus", true); err != nil {
		t.Fatalf("expected -exec-model during execution to be accepted: %v", err)
	}
}

func TestEmptyExecutionModelDoesNotRequireExecution(t *testing.T) {
	if err := validateExecModelFlag("", false); err != nil {
		t.Fatalf("empty -exec-model should not require execution: %v", err)
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
	err := validateInteractiveFlag(true, false, false)
	if err == nil {
		t.Fatal("expected bare -interactive to be rejected")
	}
	if !strings.Contains(err.Error(), "-plan") || !strings.Contains(err.Error(), "-exec") {
		t.Fatalf("error = %q, want both supported flags", err)
	}
}

func TestUserCanUseInteractiveWithPlan(t *testing.T) {
	if err := validateInteractiveFlag(true, true, false); err != nil {
		t.Fatalf("expected -interactive with -plan to be accepted, got %v", err)
	}
}

func TestUserCanUseInteractiveWithExec(t *testing.T) {
	if err := validateInteractiveFlag(true, false, true); err != nil {
		t.Fatalf("expected -interactive with -exec to be accepted, got %v", err)
	}
}

func TestPlanWithoutInteractiveIsUnaffected(t *testing.T) {
	if err := validateInteractiveFlag(false, true, false); err != nil {
		t.Fatalf("expected plain -plan to be accepted, got %v", err)
	}
}

func TestResumedSessionSeedsDocumentsAndShowsPlanningSucceeded(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	status := services.NewPlanStatusService(fixedClock{now: now}, models.GitContext{}, models.ToolIdentity{})
	docs := &fakeDocsPublisher{}

	seedResumedSession(status, docs)

	snapshot := status.Snapshot()
	if !docs.published {
		t.Fatal("planning documents were not published")
	}
	if snapshot.Phase != models.PlanPhaseSucceeded {
		t.Fatalf("phase = %q, want succeeded", snapshot.Phase)
	}
	if snapshot.Demo != "<button>Demo</button>" {
		t.Fatalf("demo = %q, want the resumed DEMO.html", snapshot.Demo)
	}
	if !snapshot.StartedAt.Equal(now) || !snapshot.EndedAt.Equal(now) {
		t.Fatalf("planning timing = %v to %v, want %v", snapshot.StartedAt, snapshot.EndedAt, now)
	}
}
