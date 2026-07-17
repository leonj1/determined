package main

import (
	"context"
	"flag"
	"fmt"
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
var version = "dev"

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
	model := flag.String("model", "", "model ID or alias to pass to droid or claude")
	plan := flag.String("plan", "", "describe a goal to plan interactively, producing PLAN.md and STEPS.md")
	exec := flag.Bool("exec", false, "run the execute loop against PLAN.md / STEPS.md; with -plan, execution follows successful planning")
	reviewPlan := flag.Bool("review-plan", false, "critique and interactively revise existing PLAN.md and STEPS.md")
	criteria := flag.Bool("criteria", false, "interactively capture BDD journey tests into CRITERIA.md; with -plan or -exec, the session runs first and the tests become required acceptance criteria")
	interactive := flag.Bool("interactive", false, "with -plan, serve a live HTML status page for the planning session on a local web server")
	mvp := flag.Bool("mvp", false, "create a lean plan for the smallest usable outcome (plan mode only)")
	prototype := flag.Bool("prototype", false, "create a fast experimental plan with minimal questioning and no refinement (plan mode only)")
	maxStepPasses := flag.Int("max-step-passes", 5,
		"max quality assess/refine rounds during planning; 0 disables")
	maxStalled := flag.Int("max-stalled-iterations", 3,
		"stop with exit 3 after this many consecutive iterations check no new step; 0 disables")
	maxFailures := flag.Int("max-consecutive-failures", 3,
		"abort with exit 1 after this many consecutive failed tool invocations; a success resets the count")
	maxIterationDuration := flag.Duration("max-iteration-duration", 15*time.Minute,
		"kill a single tool invocation after this long, counting it as a failed invocation; 0 means unlimited")
	verify := flag.Bool("verify", true,
		"after each newly checked step, run an independent verifier invocation that unchecks it (recording why in FIXES.md) if its acceptance criterion is not met")
	specializedReviews := flag.Bool("specialized-reviews", true,
		"before the final audit, run independent security, performance, and reliability/maintainability reviews")
	gitCheckpoint := flag.Bool("git-checkpoint", true,
		"git-commit the working tree after each verified step when running in a git repository")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

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
	if err := validateInteractiveFlag(*interactive, *plan != ""); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		os.Exit(2)
	}
	if !operationRequested(*plan != "", *reviewPlan, *exec, *criteria) {
		flag.Usage()
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clock := clients.NewSystemClock()
	logs := clients.NewFileLogSink(*logDir, clock)

	var outcome models.Outcome
	proceed := true
	if *criteria {
		outcome = runCriteria(ctx, selected, *budget, clock, logs)
		proceed = criteriaAllowsContinuation(outcome) &&
			operationRequested(*plan != "", *reviewPlan, *exec, false)
	}
	if proceed {
		if *reviewPlan {
			outcome = runReviewPlan(ctx, selected, *budget, *maxStepPasses, *maxFailures, clock, logs)
		} else if *plan != "" {
			outcome = runPlan(ctx, selected, planInput(*plan, flag.Args()), planMode, *budget, *maxStepPasses, *maxFailures, *interactive, !*exec, clock, logs)
			if shouldExecuteAfterPlan(*exec, outcome) {
				outcome = runLoop(ctx, selected, *budget, *maxStalled, *maxFailures, *maxIterationDuration, *verify, *specializedReviews, *gitCheckpoint, clock, logs)
			}
		} else {
			outcome = runLoop(ctx, selected, *budget, *maxStalled, *maxFailures, *maxIterationDuration, *verify, *specializedReviews, *gitCheckpoint, clock, logs)
		}
	}

	fmt.Fprintf(os.Stderr, "\ndetermined: %s\n", outcome)
	os.Exit(outcome.ExitCode())
}

func registerInitFlag(flags *flag.FlagSet) *bool {
	return flags.Bool("init", false, "install personal CLAUDE.md and AGENTS.md files")
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

// validateExecFlag rejects -exec alongside -review-plan: review mode critiques
// an existing plan and never enters the execute loop.
func validateExecFlag(reviewing, executing bool) error {
	if reviewing && executing {
		return fmt.Errorf("-exec and -review-plan cannot be used together")
	}
	return nil
}

// operationRequested reports whether the flags select any run operation. When
// none is selected, main shows the usage screen instead of defaulting to a
// mode.
func operationRequested(planning, reviewing, executing, criteria bool) bool {
	return planning || reviewing || executing || criteria
}

// validateCriteriaFlag rejects -criteria alongside -review-plan: the review
// flow critiques an existing plan and never reads the criteria file.
func validateCriteriaFlag(criteria, reviewing bool) error {
	if criteria && reviewing {
		return fmt.Errorf("-criteria and -review-plan cannot be used together")
	}
	return nil
}

// validateInteractiveFlag rejects -interactive without -plan: the live status
// page observes a planning session and has nothing to show otherwise.
func validateInteractiveFlag(interactive, planning bool) error {
	if interactive && !planning {
		return fmt.Errorf("-interactive requires -plan")
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

// shouldExecuteAfterPlan reports whether a -plan -exec run should continue
// into the execute loop: only when planning left a usable plan behind.
func shouldExecuteAfterPlan(executing bool, outcome models.Outcome) bool {
	return executing && outcome == models.OutcomePlanReady
}

func refinePasses(mode models.PlanMode, configured int) int {
	if mode == models.PlanModePrototype {
		return 0
	}
	return configured
}

// runLoop runs the unattended execute loop against PLAN.md / STEPS.md.
func runLoop(ctx context.Context, tool models.Tool, budget time.Duration, maxStalled, maxFailures int, maxIterationDuration time.Duration, verify, specializedReviews, gitCheckpoint bool, clock services.Clock, logs services.LogSink) models.Outcome {
	cfg := models.Config{
		StopFile:               "STOP.md",
		PlanFile:               "PLAN.md",
		StepsFile:              "STEPS.md",
		Tool:                   tool,
		Budget:                 budget,
		MaxStalledIterations:   maxStalled,
		MaxConsecutiveFailures: maxFailures,
		MaxIterationDuration:   maxIterationDuration,
		Verify:                 verify,
		SpecializedReviews:     specializedReviews,
		GitCheckpoint:          gitCheckpoint,
	}
	orchestrator := services.NewOrchestrator(
		clients.NewExecCommandRunner(),
		clients.NewOsFileStore(),
		clock,
		logs,
		os.Stdout,
		cfg,
	)
	return orchestrator.Run(ctx)
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
// local web server shows the session live; holdPage keeps it serving after
// completion (plan-only mode) until the user dismisses it.
func runPlan(ctx context.Context, tool models.Tool, goal string, mode models.PlanMode, budget time.Duration, maxStepPasses, maxFailures int, interactive, holdPage bool, clock services.Clock, logs services.LogSink) models.Outcome {
	prompts := services.PlanningPrompts(mode)
	cfg := models.PlanConfig{
		Operation:              models.PlanOperationCreate,
		Goal:                   goal,
		Invocation:             tool.Invocation(prompts.Plan),
		Budget:                 budget,
		AssessInvocation:       tool.Invocation(prompts.Assess),
		RefineInvocation:       tool.Invocation(prompts.Refine),
		MaxRefinePasses:        refinePasses(mode, maxStepPasses),
		MaxConsecutiveFailures: maxFailures,
		GoalFile:               "GOAL.md",
		QuestionsFile:          "QUESTIONS.md",
		AnswersFile:            "ANSWERS.md",
		PlanFile:               "PLAN.md",
		StepsFile:              "STEPS.md",
		AssessmentFile:         "REFINEMENTS.md",
	}
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
	return runInteractivePlan(ctx, orchestrator, holdPage, clock)
}

// runInteractivePlan wraps a planning run with the live status web server. A
// bind failure aborts before the AI tool is ever invoked; in plan-only mode
// the page stays served after completion until the user presses Enter or
// interrupts, so the completion banner remains viewable.
func runInteractivePlan(ctx context.Context, orchestrator *services.PlanOrchestrator, holdPage bool, clock services.Clock) models.Outcome {
	status := services.NewPlanStatusService(clock, clients.NewGitContextReader().Read(ctx))
	server := clients.NewPlanStatusServer(status)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "determined: %v\n", err)
		return models.OutcomeDroidFailed
	}
	fmt.Fprintf(os.Stdout, "determined: planning status page at %s\n", server.URL())

	outcome := orchestrator.WithStatusReporter(status).Run(ctx)

	if holdPage && ctx.Err() == nil {
		fmt.Fprintf(os.Stdout, "determined: status page still serving at %s — press Enter to exit\n", server.URL())
		waitForDismissal(ctx)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx) //nolint:errcheck // best-effort shutdown on exit
	return outcome
}

// waitForDismissal blocks until the user presses Enter or the run is
// interrupted, keeping the status page available for reading.
func waitForDismissal(ctx context.Context) {
	dismissed := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		os.Stdin.Read(buf) //nolint:errcheck // any read result dismisses
		close(dismissed)
	}()
	select {
	case <-ctx.Done():
	case <-dismissed:
	}
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

// runReviewPlan critiques an existing plan, interviews the user about
// consequential choices, and applies revisions without entering execute mode.
func runReviewPlan(ctx context.Context, tool models.Tool, budget time.Duration, maxStepPasses, maxFailures int, clock services.Clock, logs services.LogSink) models.Outcome {
	prompts := services.ReviewPrompts()
	cfg := models.PlanConfig{
		Operation:              models.PlanOperationReview,
		Budget:                 budget,
		AssessInvocation:       tool.Invocation(prompts.Assess),
		RefineInvocation:       tool.Invocation(prompts.Refine),
		MaxRefinePasses:        maxStepPasses,
		MaxConsecutiveFailures: maxFailures,
		GoalFile:               "GOAL.md",
		QuestionsFile:          "REVIEW_QUESTIONS.md",
		AnswersFile:            "REVIEW_ANSWERS.md",
		PlanFile:               "PLAN.md",
		StepsFile:              "STEPS.md",
		AssessmentFile:         "REFINEMENTS.md",
	}
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
