package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

	budget := registerBudgetFlags(flag.CommandLine)
	logDir := flag.String("log-dir", "logs", "directory for per-iteration log files")
	tool := flag.String("tool", "droid", "AI coding CLI to run (droid|pi|claude)")
	model := flag.String("model", "", "model ID or alias to pass to droid or claude")
	plan := flag.String("plan", "", "describe a goal to plan interactively, producing PLAN.md and STEPS.md")
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
	gitCheckpoint := flag.Bool("git-checkpoint", true,
		"git-commit the working tree after each verified step when running in a git repository")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("determined %s\n", version)
		os.Exit(0)
	}
	planMode, err := selectPlanMode(*plan != "", *mvp, *prototype)
	if err != nil {
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clock := clients.NewSystemClock()
	logs := clients.NewFileLogSink(*logDir, clock)

	var outcome models.Outcome
	if *plan != "" {
		outcome = runPlan(ctx, selected, planInput(*plan, flag.Args()), planMode, *budget, *maxStepPasses, clock, logs)
	} else {
		outcome = runLoop(ctx, selected, *budget, *maxStalled, *maxFailures, *maxIterationDuration, *verify, *gitCheckpoint, clock, logs)
	}

	fmt.Fprintf(os.Stderr, "\ndetermined: %s\n", outcome)
	os.Exit(outcome.ExitCode())
}

func selectPlanMode(planning, mvp, prototype bool) (models.PlanMode, error) {
	if mvp && prototype {
		return "", fmt.Errorf("-mvp and -prototype cannot be used together")
	}
	if !planning && (mvp || prototype) {
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

func refinePasses(mode models.PlanMode, configured int) int {
	if mode == models.PlanModePrototype {
		return 0
	}
	return configured
}

// runLoop runs the unattended execute loop against PLAN.md / STEPS.md.
func runLoop(ctx context.Context, tool models.Tool, budget time.Duration, maxStalled, maxFailures int, maxIterationDuration time.Duration, verify, gitCheckpoint bool, clock services.Clock, logs services.LogSink) models.Outcome {
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
// questions to the user until a plan is produced.
func runPlan(ctx context.Context, tool models.Tool, goal string, mode models.PlanMode, budget time.Duration, maxStepPasses int, clock services.Clock, logs services.LogSink) models.Outcome {
	prompts := services.PlanningPrompts(mode)
	cfg := models.PlanConfig{
		Goal:             goal,
		Invocation:       tool.Invocation(prompts.Plan),
		Budget:           budget,
		AssessInvocation: tool.Invocation(prompts.Assess),
		RefineInvocation: tool.Invocation(prompts.Refine),
		MaxRefinePasses:  refinePasses(mode, maxStepPasses),
		GoalFile:         "GOAL.md",
		QuestionsFile:    "QUESTIONS.md",
		AnswersFile:      "ANSWERS.md",
		PlanFile:         "PLAN.md",
		StepsFile:        "STEPS.md",
		AssessmentFile:   "REFINEMENTS.md",
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
