package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

// planPrompt is the instruction handed to the tool each round of the attended
// planning loop. It drives the QUESTIONS.md / ANSWERS.md protocol the
// PlanOrchestrator mediates.
const planPrompt = "You are helping plan a software project before any code is written. " +
	"Read GOAL.md for the user's goal, and read ANSWERS.md if it exists for clarifying " +
	"questions you already asked and the user's answers. " +
	"If you do NOT yet have enough detail to write a thorough plan, write your clarifying " +
	"questions to QUESTIONS.md as a markdown numbered list, one question per line, and do " +
	"nothing else. " +
	"If you DO have enough detail, write a detailed PLAN.md (overview, goals, constraints, " +
	"architecture) and a STEPS.md containing an ordered list of discrete, individually " +
	"completable steps, and do not write QUESTIONS.md. " +
	"STEPS.md MUST be a markdown checkbox list: each step is a single `- [ ]` item, marked " +
	"incomplete. Every step MUST end with a line beginning `Done when:` stating a concrete, " +
	"checkable acceptance condition — a command to run or a behavior to observe. " +
	"Do not implement anything. Do not create STOP.md."

// assessPrompt asks the tool to judge whether each step in STEPS.md is small
// enough for an AI coding tool to implement in a single pass, recording the
// verdict in OVERSIZED.md for the PlanOrchestrator to act on.
const assessPrompt = "Read STEPS.md. For each step, judge whether a capable AI coding tool could " +
	"implement it correctly and completely in a single pass — one focused change. " +
	"Write to OVERSIZED.md a markdown list naming every step that is too large or does too much " +
	"to be implemented in one pass; quote or summarize each so it can be identified. " +
	"If every step is already small enough, write exactly the single word NONE to OVERSIZED.md. " +
	"Do not modify STEPS.md or PLAN.md. Do not implement anything. Do not create STOP.md."

// breakdownPrompt asks the tool to split the steps flagged in OVERSIZED.md into
// smaller, individually-implementable steps, rewriting STEPS.md in place.
const breakdownPrompt = "Read STEPS.md and OVERSIZED.md. OVERSIZED.md lists steps that are too large to " +
	"implement in one pass. Rewrite STEPS.md so each oversized step is broken into smaller, ordered, " +
	"individually-implementable sub-steps; leave the already-small steps unchanged and preserve the " +
	"overall order. Every step must be something an AI coding tool can implement correctly in a single " +
	"focused pass. Keep STEPS.md a markdown checkbox list: each step is a single `- [ ]` item, marked " +
	"incomplete, and every step MUST end with a line beginning `Done when:` stating a concrete, " +
	"checkable acceptance condition — a command to run or a behavior to observe. " +
	"Do not implement anything. Do not create STOP.md."

// version is the semantic version of the binary. It defaults to "dev" for local
// builds and is overridden at link time via -ldflags="-X main.version=<semver>"
// by the release build (see Dockerfile.build / Makefile).
var version = "dev"

func main() {
	budget := flag.Duration("max-duration", time.Hour,
		"wall-clock budget, checked between iterations; 0 means unlimited")
	logDir := flag.String("log-dir", "logs", "directory for per-iteration log files")
	tool := flag.String("tool", "droid", "AI coding CLI to run (droid|pi|claude)")
	plan := flag.String("plan", "", "describe a goal to plan interactively, producing PLAN.md and STEPS.md")
	maxStepPasses := flag.Int("max-step-passes", 5,
		"max assess/breakdown rounds to shrink oversized steps during planning; 0 disables")
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

	selected, err := models.SelectTool(*tool)
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
		outcome = runPlan(ctx, selected, *plan, *budget, *maxStepPasses, clock, logs)
	} else {
		outcome = runLoop(ctx, selected, *budget, *maxStalled, *maxFailures, *maxIterationDuration, *verify, *gitCheckpoint, clock, logs)
	}

	fmt.Fprintf(os.Stderr, "\ndetermined: %s\n", outcome)
	os.Exit(outcome.ExitCode())
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

// runPlan runs the attended planning loop, relaying the tool's clarifying
// questions to the user until a plan is produced.
func runPlan(ctx context.Context, tool models.Tool, goal string, budget time.Duration, maxStepPasses int, clock services.Clock, logs services.LogSink) models.Outcome {
	cfg := models.PlanConfig{
		Goal:                goal,
		Invocation:          tool.Invocation(planPrompt),
		Budget:              budget,
		AssessInvocation:    tool.Invocation(assessPrompt),
		BreakdownInvocation: tool.Invocation(breakdownPrompt),
		MaxRefinePasses:     maxStepPasses,
		GoalFile:            "GOAL.md",
		QuestionsFile:       "QUESTIONS.md",
		AnswersFile:         "ANSWERS.md",
		PlanFile:            "PLAN.md",
		StepsFile:           "STEPS.md",
		OversizedFile:       "OVERSIZED.md",
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
