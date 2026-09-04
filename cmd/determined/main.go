package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

// version is the semantic version of the binary. It defaults to "dev" for local
// builds and is overridden at link time via -ldflags="-X main.version=<semver>"
// by the release build (see Dockerfile.build / Makefile).
var version = "1.1.0"

func main() {
	if isUpdateCommand(os.Args) {
		runUpdateCommand()
		return
	}
	if isInitCommand(os.Args) {
		runInitCommand()
		return
	}

	budget := registerBudgetFlags(flag.CommandLine)
	initialize := registerInitFlag(flag.CommandLine)
	logDir := flag.String("log-dir", "logs", "directory for per-iteration log files")
	tool := flag.String("tool", "droid", "AI coding CLI to run (droid|pi|claude)")
	planTool := flag.String("plan-tool", "", "AI CLI for milestone planning gates (droid|pi|claude; defaults to -tool)")
	model := flag.String("model", "", "model ID or alias to pass to droid or claude")
	execModel := flag.String("exec-model", "", "model ID or alias used only for execution steps; falls back to -model when empty")
	plan := flag.String("plan", "", "describe a goal to plan interactively, producing PLAN.md and STEPS.md")
	exec := flag.Bool("exec", false, "run the execute loop against PLAN.md / STEPS.md; add -interactive for a live status page and failed-run retries")
	milestones := flag.Bool("milestones", false, "plan or execute using bounded, independently verified milestones")
	maxPlanRevisions := flag.Int("max-plan-revisions", 3, "maximum milestone replans; 0 means unlimited")
	maxIntentRetries := flag.Int("max-intent-retries", 2, "maximum failed intent checks per milestone; 0 means unlimited")
	reviewPlan := flag.Bool("review-plan", false, "critique and interactively revise existing PLAN.md and STEPS.md")
	criteria := flag.Bool("criteria", false, "interactively capture BDD journey tests into CRITERIA.md; with -plan or -exec, the session runs first and the tests become required acceptance criteria")
	interactive := flag.Bool("interactive", false, "with -plan or -exec, serve a live HTML status page on a local web server")
	statusHost := flag.String("status-host", "127.0.0.1",
		"interface the live status page binds to; loopback by default — setting a non-loopback host (e.g. 0.0.0.0) exposes the page and its execution controls to the network")
	chat := flag.Bool("chat", false, "connect to the running determined session for a persistent conversation")
	message := flag.String("m", "", "ask one synchronous question (requires -chat)")
	mvp := flag.Bool("mvp", false, "create a lean plan for the smallest usable outcome (plan mode only)")
	prototype := flag.Bool("prototype", false, "create a fast experimental plan with minimal questioning and no refinement (plan mode only)")
	maxStepPasses := registerMaxStepPassesFlag(flag.CommandLine)
	maxStalled := flag.Int("max-stalled-iterations", 3,
		"stop with exit 3 after this many consecutive iterations check no new step; 0 disables")
	maxFailures := flag.Int("max-consecutive-failures", 3,
		"abort with exit 1 after this many consecutive failed tool invocations; a success resets the count")
	maxIterationDuration := flag.Duration("max-iteration-duration", 15*time.Minute,
		"kill a single tool invocation after this long, counting it as a failed invocation; 0 means unlimited")
	stepMaxRuntime := flag.Duration("step-max-runtime", 15*time.Minute,
		"stop the run when a single step's total runtime across invocations exceeds this, checked between invocations; 0 means unlimited")
	verify := flag.Bool("verify", true,
		"after each newly checked step, run an independent correctness reviewer that unchecks it and records why in FIXES.md when its acceptance criterion is not met")
	stepSimplicity := registerStepSimplicityFlag(flag.CommandLine)
	specializedReviews := flag.Bool("specialized-reviews", true,
		"before the final audit, run independent security, performance, and reliability/maintainability reviews")
	maxSpecialistRounds := flag.Int("max-specialist-rounds", 2,
		"stop with exit 3 when a single specialist review triggers remediation in more than this many completion passes, leaving its findings in FIXES.md for user review; 0 disables")
	gitCheckpoint := flag.Bool("git-checkpoint", true,
		"git-commit the working tree after each verified step when running in a git repository")
	tieBreaker := flag.Bool("tie-breaker", true,
		"when the coder and verifier deadlock, run an AI invocation to break the tie; its verdict is final and skips further verification")
	showVersion := flag.Bool("version", false, "print the version and exit")
	link := flag.Bool("link", false, "print the URL of the interactive status page served by a still-running determined process, and exit")
	flag.Parse()

	if err := validateChatFlags(*chat, *message, *plan != "", *exec, *reviewPlan, *criteria, *interactive); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if *chat {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		code := runChatCommand(ctx, *message, os.Stdin, os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if *link {
		os.Exit(runLinkCommand(os.Stdout, os.Stderr))
	}
	if *initialize {
		runInitCommand()
		return
	}
	if *showVersion {
		fmt.Printf("determined %s\n", version)
		os.Exit(0)
	}
	planMode, err := selectPlanMode(*plan != "", *reviewPlan, *mvp, *prototype)
	if err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if err := validateExecFlag(*reviewPlan, *exec); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if err := validateCriteriaFlag(*criteria, *reviewPlan); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if err := validateInteractiveFlag(*interactive, *plan != "", *exec); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if err := validateMilestoneFlags(*milestones, *exec, *plan != "", *planTool, *maxPlanRevisions, *maxIntentRetries); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	executing := executeRequested(*exec, *plan != "", *reviewPlan, *criteria)
	if err := validateExecModelFlag(*execModel, executing); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}

	selected, err := models.SelectTool(
		models.ToolName(*tool),
		models.ToolOptions{Model: models.ModelID(*model)},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	executionTool, err := selectExecTool(
		models.ToolName(*tool), models.ModelID(*execModel), selected)
	if err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	selectedPlanTool := selected
	if *planTool != "" {
		selectedPlanTool, err = models.SelectTool(models.ToolName(*planTool), models.ToolOptions{Model: models.ModelID(*model)})
		if err != nil {
			fmt.Fprintf(os.Stderr, "determined: %v\n", err)
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clock := clients.NewSystemClock()
	logs := clients.NewFileLogSink(*logDir, clock)

	isolation := services.NewBranchIsolation(
		clients.NewExecGitWorkspace(), clients.NewOsFileStore(), clock, os.Stdout)
	branch := models.BranchState{}
	if *plan != "" || executing {
		branch, err = isolation.Begin(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "determined: %v\n", err)
			os.Exit(1)
		}
	}

	var outcome models.Outcome
	proceed := true
	if *criteria {
		outcome = runCriteria(ctx, selected, *budget, clock, logs)
		proceed = criteriaAllowsContinuation(outcome) &&
			operationRequested(*plan != "", *reviewPlan, *exec, false)
	}
	if proceed {
		milestoneCfg := milestoneExecutionConfig{Enabled: *milestones, PlanTool: selectedPlanTool, MaxPlanRevisions: *maxPlanRevisions, MaxIntentRetries: *maxIntentRetries}
		executor := func(ctx context.Context, status services.ExecStatusReporter) models.Outcome {
			return runLoop(ctx, executionTool, *budget, *maxStalled, *maxFailures, *maxSpecialistRounds, *maxIterationDuration, *stepMaxRuntime, *verify, *stepSimplicity, *specializedReviews, *gitCheckpoint, *tieBreaker, status, clock, logs, milestoneCfg)
		}
		if *reviewPlan {
			outcome = runReviewPlan(ctx, selected, *budget, *maxStepPasses, *maxFailures, clock, logs)
		} else if *plan != "" {
			outcome = runPlan(ctx, selected, planInput(*plan, flag.Args()), planMode, *budget, *maxStepPasses, *maxFailures, *interactive, executing, *statusHost, executor, clock, logs, *milestones)
			if shouldExecuteAfterPlan(executing, *interactive, outcome) &&
				approveExecution(ctx, clients.NewStdinPrompter(os.Stdout, os.Stdin)) {
				outcome = runHeadlessExec(ctx, executionTool, *statusHost, executor, clock)
			}
		} else if *interactive {
			outcome = runInteractiveExec(ctx, selected, *budget, *maxFailures, *statusHost, executor, clock, logs)
		} else {
			outcome = runHeadlessExec(ctx, executionTool, *statusHost, executor, clock)
		}
	}

	if outcome == models.OutcomeStopped {
		if err := isolation.Finish(ctx, branch); err != nil {
			fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		}
	}

	fmt.Fprintf(os.Stderr, "\ndetermined: %s\n", outcome)
	os.Exit(outcome.ExitCode())
}

func registerInitFlag(flags *flag.FlagSet) *bool {
	return flags.Bool("init", false, "install personal CLAUDE.md and AGENTS.md files")
}

func registerMaxStepPassesFlag(flags *flag.FlagSet) *int {
	return flags.Int("max-step-passes", 2,
		"max quality assess/refine rounds during planning; 0 disables")
}

func registerStepSimplicityFlag(flags *flag.FlagSet) *bool {
	return flags.Bool("step-simplicity", false,
		"run an additional simplicity reviewer before each checked step's correctness review")
}

func runInitCommand() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "determined: init failed: find home directory: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg := initializationConfig(home)
	service := services.NewInitializationService(
		clients.NewHttpDocumentSource(http.DefaultClient),
		clients.NewOsDocumentStore(),
		cfg,
	)
	if err := service.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "determined: init failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "installed ~/.claude/CLAUDE.md and ~/AGENTS.md")
}

func initializationConfig(home string) models.InitializationConfig {
	base := "https://raw.githubusercontent.com/leonj1/open-doc-format/master/personal-knowledge"
	return models.InitializationConfig{Documents: []models.InitializationDocument{
		{Source: models.DocumentURL(base + "/CLAUDE.md"), Destination: models.DestinationPath(filepath.Join(home, ".claude", "CLAUDE.md"))},
		{Source: models.DocumentURL(base + "/AGENTS.md"), Destination: models.DestinationPath(filepath.Join(home, "AGENTS.md"))},
	}}
}

func selectPlanMode(planning, reviewing, mvp, prototype bool) (models.PlanMode, error) {
	if planning && reviewing {
		return "", fmt.Errorf("-plan and -review-plan cannot be used together")
	}
	if mvp && prototype {
		return "", fmt.Errorf("-mvp and -prototype cannot be used together")
	}
	if (!planning || reviewing) && (mvp || prototype) {
		return "", fmt.Errorf("-mvp and -prototype require -plan")
	}
	if mvp {
		return models.PlanModeMVP, nil
	}
	if prototype {
		return models.PlanModePrototype, nil
	}
	return models.PlanModeStandard, nil
}

func selectExecTool(name models.ToolName, execModel models.ModelID, fallback models.Tool) (models.Tool, error) {
	if execModel.Empty() {
		return fallback, nil
	}
	tool, err := models.SelectTool(name, models.ToolOptions{Model: execModel})
	if err != nil {
		return nil, fmt.Errorf("select -exec-model: %w", err)
	}
	return tool, nil
}

// validateExecFlag rejects -exec alongside -review-plan: review mode critiques
// an existing plan and never enters the execute loop.
func validateExecFlag(reviewing, executing bool) error {
	if reviewing && executing {
		return fmt.Errorf("-exec and -review-plan cannot be used together")
	}
	return nil
}

// validateExecModelFlag rejects an execution model when no execute loop can
// run, so a user-provided model is never silently ignored.
func validateExecModelFlag(execModel string, executing bool) error {
	if execModel != "" && !executing {
		return fmt.Errorf("-exec-model requires an execution phase")
	}
	return nil
}

// statusPageProbeTimeout bounds the -link liveness probe: a listener that has
// not answered by then is treated as not serving, so -link always returns
// promptly instead of hanging on a wedged process.
const statusPageProbeTimeout = 2 * time.Second

// sessionLocator builds the locator over the well-known record path. It reports
// false when the home directory is unknown, in which case sessions cannot be
// recorded or recovered.
func sessionLocator() (services.SessionLocator, bool) {
	path, err := clients.DefaultSessionRecordPath()
	if err != nil {
		return services.SessionLocator{}, false
	}
	return services.NewSessionLocator(
		clients.NewFileSessionRecordStore(path),
		clients.NewSignalProcessProbe(),
		clients.NewHttpStatusPageProbe(statusPageProbeTimeout),
	), true
}

// runLinkCommand prints the URL of a verified-live status page and returns the
// process exit code: 0 when a session was confirmed, 1 when none was. It
// reports a link only after proving the recording process is alive, its port is
// listening, and that port answers with the determined status page.
func runLinkCommand(stdout, stderr io.Writer) int {
	locator, located := sessionLocator()
	if !located {
		fmt.Fprintf(stderr, "determined: cannot locate the session record: home directory is unknown\n")
		return 1
	}
	link, err := locator.Locate()
	if err != nil {
		fmt.Fprintf(stderr, "determined: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", link.URL)
	return 0
}

const chatReplyTimeout = 10 * time.Second

func runChatCommand(ctx context.Context, message string, input io.Reader, output, errors io.Writer) int {
	locator, located := sessionLocator()
	if !located {
		fmt.Fprintln(errors, "determined: cannot locate the session record: home directory is unknown")
		return 1
	}
	client := services.NewChatClient(locator, clients.NewWebSocketDialer(), input, output, chatReplyTimeout)
	var err error
	if message != "" {
		err = client.Ask(ctx, message)
	} else {
		err = client.Converse(ctx)
	}
	if err != nil {
		fmt.Fprintf(errors, "determined: %v\n", err)
		return 1
	}
	return 0
}

func validateChatFlags(chat bool, message string, planning, executing, reviewing, criteria, interactive bool) error {
	if message != "" && !chat {
		return fmt.Errorf("-m requires -chat")
	}
	if !chat {
		return nil
	}
	if planning || executing || reviewing || criteria || interactive {
		return fmt.Errorf("-chat cannot be combined with -plan, -exec, -review-plan, -criteria, or -interactive")
	}
	return nil
}

// operationRequested reports whether the flags select any run operation.
func operationRequested(planning, reviewing, executing, criteria bool) bool {
	return planning || reviewing || executing || criteria
}

// executeRequested reports whether the run should enter the execute loop:
// either -exec was given, or no operation flag selected one, in which case
// execution is the default.
func executeRequested(executing, planning, reviewing, criteria bool) bool {
	return executing || !operationRequested(planning, reviewing, executing, criteria)
}

// validateCriteriaFlag rejects -criteria alongside -review-plan: the review
// flow critiques an existing plan and never reads the criteria file.
func validateCriteriaFlag(criteria, reviewing bool) error {
	if criteria && reviewing {
		return fmt.Errorf("-criteria and -review-plan cannot be used together")
	}
	return nil
}

// validateInteractiveFlag rejects a status page without an explicit session.
func validateInteractiveFlag(interactive, planning, executing bool) error {
	if interactive && !planning && !executing {
		return fmt.Errorf("-interactive requires -plan or -exec")
	}
	return nil
}

func validateMilestoneFlags(enabled, executing, planning bool, planTool string, revisions, retries int) error {
	if enabled && !executing && !planning {
		return fmt.Errorf("-milestones requires -exec or -plan")
	}
	if revisions < 0 {
		return fmt.Errorf("-max-plan-revisions must be >= 0")
	}
	if retries < 0 {
		return fmt.Errorf("-max-intent-retries must be >= 0")
	}
	if planTool != "" && planTool != "droid" && planTool != "pi" && planTool != "claude" {
		return fmt.Errorf("unknown tool %q", planTool)
	}
	return nil
}

// criteriaAllowsContinuation reports whether a -criteria session left the run
// able to continue into planning or execution: a finished session does, and
// so does a cancelled one (cancel discards the session's tests, not the run).
// Aborts — interrupt, budget, tool failure, stall — stop the whole run.
func criteriaAllowsContinuation(outcome models.Outcome) bool {
	return outcome == models.OutcomeCriteriaReady || outcome == models.OutcomeCriteriaCancelled
}

// shouldExecuteAfterPlan reports whether a -plan -exec run should continue in
// the headless execute loop. Interactive sessions execute before page shutdown.
func shouldExecuteAfterPlan(executing, interactive bool, outcome models.Outcome) bool {
	return executing && !interactive && outcome == models.OutcomePlanReady
}

func refinePasses(mode models.PlanMode, configured int) int {
	if mode == models.PlanModePrototype {
		return 0
	}
	return configured
}

// createPlanConfig builds the complete planning protocol configuration used by
// both new plans and resumed interactive execution sessions.
func createPlanConfig(tool models.Tool, goal string, mode models.PlanMode, budget time.Duration, maxStepPasses, maxFailures int, milestoneArgs ...bool) models.PlanConfig {
	milestones := len(milestoneArgs) > 0 && milestoneArgs[0]
	prompts := services.PlanningPrompts(mode)
	if milestones {
		prompts = services.BuildMilestonePlanningPrompts(mode)
	}
	return models.PlanConfig{
		Milestones: milestones, MilestonesFile: "MILESTONES.md",
		Operation: models.PlanOperationCreate, Goal: goal,
		Invocation: tool.Invocation(prompts.Plan), Budget: budget,
		AssessInvocation: tool.Invocation(prompts.Assess), RefineInvocation: tool.Invocation(prompts.Refine),
		SimplifyInvocation: tool.Invocation(prompts.Simplify),
		TestsInvocation:    tool.Invocation(prompts.Tests), AlignInvocation: tool.Invocation(prompts.Align),
		RealignInvocation:  tool.Invocation(prompts.Realign),
		DemoInvocation:     tool.Invocation(prompts.Demo),
		AnnotateInvocation: tool.Invocation(prompts.Annotate),
		MaxRefinePasses:    refinePasses(mode, maxStepPasses), MaxConsecutiveFailures: maxFailures,
		GoalFile: "GOAL.md", QuestionsFile: "QUESTIONS.md", AnswersFile: "ANSWERS.md",
		PlanFile: "PLAN.md", StepsFile: "STEPS.md", TestsFile: "TESTS.md", DemoFile: "DEMO.html",
		AssessmentFile: "REFINEMENTS.md", AnnotationFile: "ANNOTATION.md",
	}
}

// planExecutor starts the execute loop for a planned session, streaming its
// events to the given status reporter (nil disables reporting).
type planExecutor func(ctx context.Context, status services.ExecStatusReporter) models.Outcome

// milestoneExecutionConfig carries the optional outer-loop configuration.
type milestoneExecutionConfig struct {
	Enabled          bool
	PlanTool         models.Tool
	MaxPlanRevisions int
	MaxIntentRetries int
}

// runLoop runs the unattended execute loop against PLAN.md / STEPS.md. A
// non-nil status reporter streams the run to the interactive status page.
func runLoop(ctx context.Context, tool models.Tool, budget time.Duration, maxStalled, maxFailures, maxSpecialistRounds int, maxIterationDuration, stepMaxRuntime time.Duration, verify, stepSimplicity, specializedReviews, gitCheckpoint, tieBreaker bool, status services.ExecStatusReporter, clock services.Clock, logs services.LogSink, milestone milestoneExecutionConfig) models.Outcome {
	cfg := buildExecutionConfig(tool, budget, maxStalled, maxFailures, maxSpecialistRounds, maxIterationDuration, stepMaxRuntime, verify, stepSimplicity, specializedReviews, gitCheckpoint, tieBreaker)
	if milestone.Enabled {
		return runMilestoneLoop(ctx, cfg, milestone, status, clock, logs)
	}
	files := clients.NewOsFileStore()
	if files.Exists("MILESTONES.md") && !files.Exists("STEPS.md") {
		fmt.Fprintln(os.Stderr, "determined: MILESTONES.md found; run with -milestones")
		return models.OutcomeMissingFiles
	}
	orchestrator := services.NewOrchestrator(clients.NewExecCommandRunner(), files, clock, logs, os.Stdout, cfg).WithStatusReporter(status)
	if control, ok := status.(services.TaskController); ok {
		orchestrator.WithTaskControl(control)
	}
	if resolver, ok := status.(services.StallResolver); ok {
		orchestrator.WithStallResolver(resolver)
	}
	return orchestrator.Run(ctx)
}

// buildExecutionConfig maps CLI execution settings to the domain configuration.
func buildExecutionConfig(tool models.Tool, budget time.Duration, maxStalled, maxFailures, maxSpecialistRounds int, maxIterationDuration, stepMaxRuntime time.Duration, verify, stepSimplicity, specializedReviews, gitCheckpoint, tieBreaker bool) models.Config {
	return models.Config{
		StopFile:               "STOP.md",
		PlanFile:               "PLAN.md",
		StepsFile:              "STEPS.md",
		ExplanationFile:        "EXPLANATION.md",
		QuizFile:               "QUIZ.json",
		ProtectedFiles:         []string{"PLAN.md", "TESTS.md", "CRITERIA.md"},
		Tool:                   tool,
		Budget:                 budget,
		MaxStalledIterations:   maxStalled,
		MaxConsecutiveFailures: maxFailures,
		MaxIterationDuration:   maxIterationDuration,
		StepMaxRuntime:         stepMaxRuntime,
		Verify:                 verify,
		SimplicityReviews:      stepSimplicity,
		SpecializedReviews:     specializedReviews,
		MaxSpecialistRounds:    maxSpecialistRounds,
		GitCheckpoint:          gitCheckpoint,
		TieBreaker:             tieBreaker,
	}
}

// runMilestoneLoop constructs the milestone-specific dependencies and state paths.
func runMilestoneLoop(ctx context.Context, cfg models.Config, milestone milestoneExecutionConfig, status services.ExecStatusReporter, clock services.Clock, logs services.LogSink) models.Outcome {
	if err := os.MkdirAll(".determined/steps", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		return models.OutcomeDroidFailed
	}
	cfg.Milestones = true
	cfg.PlanTool = milestone.PlanTool
	cfg.MaxPlanRevisions = milestone.MaxPlanRevisions
	cfg.MaxIntentRetries = milestone.MaxIntentRetries
	cfg.MilestonesFile = "MILESTONES.md"
	cfg.MilestoneStateFile = ".determined/milestones.json"
	cfg.DivergenceFile = "DIVERGENCE.md"
	files := clients.NewOsFileStore()
	runner := clients.NewExecCommandRunner()
	factory := func(inner models.Config) *services.Orchestrator {
		return services.NewOrchestrator(runner, files, clock, logs, os.Stdout, inner).WithStatusReporter(status)
	}
	return services.NewMilestoneOrchestrator(runner, files, clock, logs, os.Stdout, cfg, factory, status).Run(ctx)
}

// runExplain generates the explanation and quiz on demand from the status
// page, creating the necessary infrastructure fresh. It is called from the
// feedback loop when the user clicks Generate Explanation.
func runExplain(ctx context.Context, tool models.Tool, status services.ExecStatusReporter) {
	cfg := models.Config{
		StopFile:        "STOP.md",
		PlanFile:        "PLAN.md",
		StepsFile:       "STEPS.md",
		ExplanationFile: "EXPLANATION.md",
		QuizFile:        "QUIZ.json",
		ProtectedFiles:  []string{"PLAN.md", "TESTS.md", "CRITERIA.md"},
	}
	explainer := services.NewExplanationService(
		clients.NewExecCommandRunner(),
		clients.NewOsFileStore(),
		clients.NewSystemClock(),
		clients.NewFileLogSink("logs", clients.NewSystemClock()),
		os.Stdout,
		status,
		tool,
		cfg,
	)
	explainer.Run(ctx)
}

func isUpdateCommand(args []string) bool {
	return len(args) > 1 && args[1] == "update"
}

func isInitCommand(args []string) bool {
	return len(args) > 1 && args[1] == "init"
}

func registerBudgetFlags(flags *flag.FlagSet) *time.Duration {
	budget := time.Hour
	usage := "wall-clock budget, checked between iterations; 0 means unlimited"
	flags.DurationVar(&budget, "max-duration", time.Hour, usage)
	flags.DurationVar(&budget, "t", time.Hour, "alias for --max-duration")
	return &budget
}

func planInput(flagValue string, remaining []string) string {
	if len(remaining) == 0 {
		return flagValue
	}
	return strings.Join(append([]string{flagValue}, remaining...), " ")
}

func runUpdateCommand() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := models.UpdateConfig{
		CurrentVersion: models.Version(version),
		Platform:       models.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
	}
	updater := services.NewUpdateService(
		clients.NewGitHubReleaseSource(
			models.Repository{Owner: "leonj1", Name: "determined"},
			models.APIBaseURL("https://api.github.com"),
			http.DefaultClient,
		),
		clients.NewSelfExecutableInstaller(http.DefaultClient),
		os.Stdout,
		cfg,
	)
	if err := updater.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "determined: update failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runPlan runs the attended planning loop, relaying the tool's clarifying
// questions to the user until a plan is produced. With interactive set, a
// local web server shows the session live. Executing selects automatic live
// execution; otherwise the page offers its Implement button after planning.
func runPlan(ctx context.Context, tool models.Tool, goal string, mode models.PlanMode, budget time.Duration, maxStepPasses, maxFailures int, interactive, executing bool, statusHost string, execute planExecutor, clock services.Clock, logs services.LogSink, milestoneArgs ...bool) models.Outcome {
	cfg := createPlanConfig(tool, goal, mode, budget, maxStepPasses, maxFailures, milestoneArgs...)
	orchestrator := services.NewPlanOrchestrator(
		clients.NewExecCommandRunner(),
		clients.NewOsFileStore(),
		clients.NewStdinPrompter(os.Stdout, os.Stdin),
		clock,
		logs,
		os.Stdout,
		cfg,
	)
	if !interactive {
		return orchestrator.Run(ctx)
	}
	return runInteractivePlan(ctx, orchestrator, tool, executing, statusHost, execute, clock)
}

// runInteractivePlan wraps a planning run with the live status web server. A
// bind failure aborts before the AI tool is ever invoked. A ready plan either
// offers implementation or executes automatically, then stays viewable until
// the user presses Enter or interrupts.
func runInteractivePlan(ctx context.Context, orchestrator *services.PlanOrchestrator, tool models.Tool, executing bool, statusHost string, execute planExecutor, clock services.Clock) models.Outcome {
	status := newPlanStatusService(clock, clients.NewGitContextReader().Read(ctx), tool.Identity())
	server, cleanup, ok := startStatusSession(status, clock, statusHost)
	if !ok {
		return models.OutcomeDroidFailed
	}
	defer cleanup()
	outcome := orchestrator.WithPrompter(status).WithStatusReporter(status).WithTaskControl(status).Run(ctx)
	if ctx.Err() != nil {
		return outcome
	}
	switch postPlanActionFor(executing, outcome) {
	case postPlanOffer:
		explain := func(ctx context.Context) { runExplain(ctx, tool, status) }
		return holdStatusPage(ctx, orchestrator, status, server.URL(), execute, explain, outcome)
	case postPlanAutoExec:
		if !approveExecution(ctx, status) {
			return outcome
		}
		fmt.Fprintf(os.Stdout, "determined: plan ready — executing now; status page streaming at %s\n", server.URL())
		hold := completedStatusPageHolder(server.URL(), os.Stdout, os.Stdin)
		return runAutoExec(ctx, status, execute, hold)
	}
	return outcome
}

func startStatusSession(status *services.PlanStatusService, clock services.Clock, statusHost string) (*clients.PlanStatusServer, func(), bool) {
	historyPath := statusLogHistoryPath()
	history, err := clients.NewSQLiteLogStore(historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		return nil, func() {}, false
	}
	status.WithLogHistory(history)
	chat := services.NewChatService(status, clock)
	server := clients.NewPlanStatusServer(status, status, status, clock).
		WithBindHost(statusHost).
		WithLogSource(status).WithChatResponder(chat).
		WithTaskControl(status).WithStallChoice(status).WithPromptResponses(status).
		WithExplainSink(status)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		history.Close() //nolint:errcheck // best-effort close on failed start
		removeStatusLogHistory(historyPath)
		return nil, func() {}, false
	}
	locator, located := sessionLocator()
	if located {
		locator.Remember(models.SessionRecord{PID: os.Getpid(), Port: server.Port()}) //nolint:errcheck // -link is a convenience; planning proceeds regardless
	}
	fmt.Fprintf(os.Stdout, "determined: status page at %s\n", server.URL())
	fmt.Fprintf(os.Stdout, "determined: recover this link later with `determined -link`\n")
	cleanup := func() {
		if located {
			locator.Forget() //nolint:errcheck // best-effort cleanup on exit
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx) //nolint:errcheck // best-effort shutdown on exit
		history.Close()              //nolint:errcheck // best-effort close on exit
		removeStatusLogHistory(historyPath)
	}
	return server, cleanup, true
}

// statusLogHistoryPath names this run's log history database: per-process, in
// the system temp directory, removed again at session cleanup.
func statusLogHistoryPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("determined-status-logs-%d.db", os.Getpid()))
}

// removeStatusLogHistory deletes the history database and its WAL sidecars.
func removeStatusLogHistory(path string) {
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		os.Remove(name) //nolint:errcheck // best-effort cleanup on exit
	}
}

// runHeadlessExec makes an unattended execution observable while preserving
// execution as the primary outcome when the optional server cannot start.
func runHeadlessExec(ctx context.Context, tool models.Tool, statusHost string, execute planExecutor, clock services.Clock) models.Outcome {
	status := newPlanStatusService(clock, clients.NewGitContextReader().Read(ctx), tool.Identity())
	_, cleanup, ok := startStatusSession(status, clock, statusHost)
	if !ok {
		return continueHeadlessExec(ctx, status, execute, cleanup, false, nil)
	}
	cfg := createPlanConfig(tool, "", models.PlanModeStandard, 0, 0, 0)
	docs := services.NewPlanDocumentPublisher(clients.NewOsFileStore(), cfg)
	return continueHeadlessExec(ctx, status, execute, cleanup, true, docs)
}

func continueHeadlessExec(ctx context.Context, status *services.PlanStatusService, execute planExecutor, cleanup func(), serving bool, docs planDocumentPublisher) models.Outcome {
	if !serving {
		return execute(ctx, nil)
	}
	defer cleanup()
	seedResumedSession(status, docs)
	return execute(ctx, status)
}

func runInteractiveExec(ctx context.Context, tool models.Tool, budget time.Duration, maxFailures int, statusHost string, execute planExecutor, clock services.Clock, logs services.LogSink) models.Outcome {
	cfg := createPlanConfig(tool, "", models.PlanModeStandard, budget, 0, maxFailures)
	status := newPlanStatusService(clock, clients.NewGitContextReader().Read(ctx), tool.Identity())
	server, cleanup, ok := startStatusSession(status, clock, statusHost)
	if !ok {
		return models.OutcomeDroidFailed
	}
	defer cleanup()
	files := clients.NewOsFileStore()
	orchestrator := services.NewPlanOrchestrator(
		clients.NewExecCommandRunner(), files, clients.NewStdinPrompter(os.Stdout, os.Stdin),
		clock, logs, os.Stdout, cfg,
	).WithStatusReporter(status).WithTaskControl(status)
	seedResumedSession(status, services.NewPlanDocumentPublisher(files, cfg))
	outcome := execute(ctx, status)
	if ctx.Err() != nil {
		return outcome
	}
	status.OfferImplement()
	fmt.Fprintf(os.Stdout, "determined: status page still serving at %s — annotate sections, click Implement to run again after a failure, or press Enter to exit\n", server.URL())
	explain := func(ctx context.Context) { runExplain(ctx, tool, status) }
	return serveFeedbackLoop(ctx, orchestrator, status, execute, explain, outcome)
}

const statusLogCapacity = 2000

func newPlanStatusService(clock services.Clock, git models.GitContext, tool models.ToolIdentity) *services.PlanStatusService {
	return services.NewPlanStatusService(
		clock, git, tool, services.NewCircularLogBuffer(statusLogCapacity))
}

// completedStatusPageHolder drains terminal input during execution, then keeps
// the completed page available until a fresh Enter or an interruption.
func completedStatusPageHolder(url string, output io.Writer, input io.Reader) completedPageHolder {
	ready := make(chan struct{})
	dismissed := dismissalChannelWhenReady(input, ready)
	return func(ctx context.Context) {
		fmt.Fprintf(output,
			"determined: execution finished; status page still serving at %s — press Enter to exit\n",
			url)
		close(ready)
		select {
		case <-ctx.Done():
		case <-dismissed:
		}
	}
}

// dismissalChannelWhenReady discards reads before the page is ready to close.
func dismissalChannelWhenReady(input io.Reader, ready <-chan struct{}) <-chan struct{} {
	dismissed := make(chan struct{})
	go func() {
		for {
			buf := make([]byte, 1)
			_, err := input.Read(buf)
			select {
			case <-ready:
				close(dismissed)
				return
			default:
			}
			if err != nil {
				<-ready
				close(dismissed)
				return
			}
		}
	}()
	return dismissed
}

// holdStatusPage keeps a plan-only session interactive: annotations refine the
// plan documents, and the Implement button starts execution in the live page.
// Enter dismisses the page. The result is execution's outcome when requested,
// or the planning outcome when the page is dismissed without implementation.
func holdStatusPage(ctx context.Context, orchestrator *services.PlanOrchestrator, status *services.PlanStatusService, url string, execute planExecutor, explain func(context.Context), outcome models.Outcome) models.Outcome {
	if outcome == models.OutcomePlanReady {
		status.OfferImplement()
	}
	fmt.Fprintf(os.Stdout,
		"determined: status page still serving at %s — annotate sections to refine the plan, click Implement to execute it, or press Enter to exit\n",
		url)
	return serveFeedbackLoop(ctx, orchestrator, status, execute, explain, outcome)
}

func serveFeedbackLoop(ctx context.Context, orchestrator *services.PlanOrchestrator, status *services.PlanStatusService, execute planExecutor, explain func(context.Context), outcome models.Outcome) models.Outcome {
	dismissed := dismissalChannel()
	for {
		action := orchestrator.ServeFeedback(ctx, dismissed)
		switch action {
		case models.FeedbackActionImplement:
			outcome = execute(ctx, status)
		case models.FeedbackActionExplain:
			explain(ctx)
		default:
			return outcome
		}
	}
}

// dismissalChannel returns a channel closed when the user presses Enter,
// letting the annotation loop keep the status page interactive until then.
func dismissalChannel() <-chan struct{} {
	dismissed := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		os.Stdin.Read(buf) //nolint:errcheck // any read result dismisses
		close(dismissed)
	}()
	return dismissed
}

// runCriteria runs the attended criteria session, capturing user-approved BDD
// journey tests in CRITERIA.md for planning and the final audit to enforce.
func runCriteria(ctx context.Context, tool models.Tool, budget time.Duration, clock services.Clock, logs services.LogSink) models.Outcome {
	cfg := models.CriteriaConfig{
		Invocation:   tool.Invocation(services.CriteriaPrompt()),
		Budget:       budget,
		CriteriaFile: "CRITERIA.md",
		RequestFile:  "CRITERIA_REQUEST.md",
		DraftFile:    "CRITERIA_DRAFT.md",
	}
	orchestrator := services.NewCriteriaOrchestrator(
		clients.NewExecCommandRunner(),
		clients.NewOsFileStore(),
		clients.NewStdinPrompter(os.Stdout, os.Stdin),
		clock,
		logs,
		os.Stdout,
		cfg,
	)
	return orchestrator.Run(ctx)
}

// reviewPlanConfig builds the complete plan-review protocol configuration.
func reviewPlanConfig(tool models.Tool, budget time.Duration, maxStepPasses, maxFailures int) models.PlanConfig {
	prompts := services.ReviewPrompts()
	return models.PlanConfig{
		Operation:              models.PlanOperationReview,
		Budget:                 budget,
		AssessInvocation:       tool.Invocation(prompts.Assess),
		RefineInvocation:       tool.Invocation(prompts.Refine),
		TestsInvocation:        tool.Invocation(prompts.Tests),
		DemoInvocation:         tool.Invocation(prompts.Demo),
		AlignInvocation:        tool.Invocation(prompts.Align),
		RealignInvocation:      tool.Invocation(prompts.Realign),
		MaxRefinePasses:        maxStepPasses,
		MaxConsecutiveFailures: maxFailures,
		GoalFile:               "GOAL.md",
		QuestionsFile:          "REVIEW_QUESTIONS.md",
		AnswersFile:            "REVIEW_ANSWERS.md",
		PlanFile:               "PLAN.md",
		StepsFile:              "STEPS.md",
		TestsFile:              "TESTS.md",
		DemoFile:               "DEMO.html",
		AssessmentFile:         "REFINEMENTS.md",
	}
}

// runReviewPlan critiques an existing plan, interviews the user about
// consequential choices, and applies revisions without entering execute mode.
func runReviewPlan(ctx context.Context, tool models.Tool, budget time.Duration, maxStepPasses, maxFailures int, clock services.Clock, logs services.LogSink) models.Outcome {
	cfg := reviewPlanConfig(tool, budget, maxStepPasses, maxFailures)
	orchestrator := services.NewPlanOrchestrator(
		clients.NewExecCommandRunner(),
		clients.NewOsFileStore(),
		clients.NewStdinPrompter(os.Stdout, os.Stdin),
		clock,
		logs,
		os.Stdout,
		cfg,
	)
	return orchestrator.Run(ctx)
}
