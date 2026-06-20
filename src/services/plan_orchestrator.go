package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"determined/src/models"
)

// Prompter asks the user a single question and returns their answer. The real
// implementation is clients.StdinPrompter.
type Prompter interface {
	Ask(question string) (string, error)
}

// FileStore is the small slice of filesystem behaviour the planning loop needs:
// it reads and writes the protocol files and reports whether they exist. The
// real implementation is clients.OsFileStore.
type FileStore interface {
	Exists(path string) bool
	Read(path string) (string, error)
	Write(path, content string) error
	Append(path, content string) error
	Remove(path string) error
}

// PlanOrchestrator runs the attended planning loop: it seeds the goal, runs the
// tool, relays any clarifying questions to the user, records the answers, and
// finishes once the tool has produced both the plan and the step list.
type PlanOrchestrator struct {
	runner   CommandRunner
	files    FileStore
	prompter Prompter
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.PlanConfig

	iteration int
}

// NewPlanOrchestrator wires a PlanOrchestrator from its dependencies.
func NewPlanOrchestrator(
	runner CommandRunner,
	files FileStore,
	prompter Prompter,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.PlanConfig,
) *PlanOrchestrator {
	return &PlanOrchestrator{
		runner:   runner,
		files:    files,
		prompter: prompter,
		clock:    clock,
		logs:     logs,
		terminal: terminal,
		cfg:      cfg,
	}
}

// Run executes the planning loop and returns the terminal outcome.
func (o *PlanOrchestrator) Run(ctx context.Context) models.Outcome {
	if err := o.files.Write(o.cfg.GoalFile, o.cfg.Goal+"\n"); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.GoalFile, err)
		return models.OutcomeDroidFailed
	}
	deadline := o.deadline()
	for {
		switch {
		case ctx.Err() != nil:
			return models.OutcomeInterrupted
		case o.planComplete():
			return o.refine(ctx, deadline)
		case o.budgetExceeded(deadline):
			return models.OutcomeBudgetExceeded
		}

		if outcome, stop := o.runInvocation(ctx, o.cfg.Invocation); stop {
			return outcome
		}

		if o.planComplete() {
			return o.refine(ctx, deadline)
		}
		if o.files.Exists(o.cfg.QuestionsFile) {
			if outcome, stop := o.relayQuestions(); stop {
				return outcome
			}
			continue
		}
		// The tool wrote neither questions nor a plan: it cannot make progress.
		return models.OutcomePlanStalled
	}
}

// refine runs the step-granularity loop once a plan exists: it asks the tool to
// flag steps that are too large to implement in one pass, then to break those
// down, repeating until every step is small enough, the budget runs out, or the
// pass cap is hit. A hit cap leaves the usable plan in place with a warning.
func (o *PlanOrchestrator) refine(ctx context.Context, deadline time.Time) models.Outcome {
	if o.cfg.MaxRefinePasses == 0 {
		return models.OutcomePlanReady // refinement disabled
	}
	for pass := 1; ; pass++ {
		switch {
		case ctx.Err() != nil:
			return models.OutcomeInterrupted
		case o.budgetExceeded(deadline):
			return models.OutcomeBudgetExceeded
		}

		if outcome, stop := o.runInvocation(ctx, o.cfg.AssessInvocation); stop {
			return outcome
		}
		content, err := o.files.Read(o.cfg.OversizedFile)
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.OversizedFile, err)
			return models.OutcomeDroidFailed
		}
		oversized := OversizedSteps(content)
		if len(oversized) == 0 {
			o.files.Remove(o.cfg.OversizedFile)
			return models.OutcomePlanReady // every step is small enough
		}
		if pass >= o.cfg.MaxRefinePasses {
			fmt.Fprintf(o.terminal,
				"determined: %d step(s) still look too large after %d refine pass(es); leaving the plan as-is\n",
				len(oversized), pass)
			o.files.Remove(o.cfg.OversizedFile)
			return models.OutcomePlanReady
		}

		if outcome, stop := o.runInvocation(ctx, o.cfg.BreakdownInvocation); stop {
			return outcome
		}
		o.files.Remove(o.cfg.OversizedFile)
	}
}

// runInvocation runs a single tool invocation, teeing its output to the terminal
// and a per-iteration log. It reports whether the loop should stop.
func (o *PlanOrchestrator) runInvocation(ctx context.Context, inv models.Invocation) (models.Outcome, bool) {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return models.OutcomeDroidFailed, true
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	if err := o.runner.Run(ctx, inv, out); err != nil {
		if ctx.Err() != nil {
			return models.OutcomeInterrupted, true
		}
		return models.OutcomeDroidFailed, true
	}
	return models.OutcomePlanReady, false // outcome ignored when stop is false
}

// relayQuestions reads the tool's questions, asks the user each one, appends the
// round to the answers history, and clears the questions file so the next tool
// run starts clean. It reports whether the loop should stop.
func (o *PlanOrchestrator) relayQuestions() (models.Outcome, bool) {
	content, err := o.files.Read(o.cfg.QuestionsFile)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.QuestionsFile, err)
		return models.OutcomeDroidFailed, true
	}
	questions := ParseQuestions(content)
	if len(questions) == 0 {
		fmt.Fprintf(o.terminal, "determined: %s had no parseable questions\n", o.cfg.QuestionsFile)
		return models.OutcomePlanStalled, true
	}

	var round strings.Builder
	fmt.Fprintf(&round, "## Round %d\n\n", o.iteration)
	for _, q := range questions {
		answer, err := o.prompter.Ask(q)
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
			return models.OutcomeInterrupted, true
		}
		fmt.Fprintf(&round, "**Q: %s**\n\n%s\n\n", q, strings.TrimSpace(answer))
	}

	if err := o.files.Append(o.cfg.AnswersFile, round.String()); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.AnswersFile, err)
		return models.OutcomeDroidFailed, true
	}
	if err := o.files.Remove(o.cfg.QuestionsFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not clear %s: %v\n", o.cfg.QuestionsFile, err)
		return models.OutcomeDroidFailed, true
	}
	return models.OutcomePlanReady, false // outcome ignored when stop is false
}

// planComplete reports whether both finished-plan files now exist.
func (o *PlanOrchestrator) planComplete() bool {
	return o.files.Exists(o.cfg.PlanFile) && o.files.Exists(o.cfg.StepsFile)
}

// deadline returns the instant the run must stop by, or the zero time when the
// budget is unlimited.
func (o *PlanOrchestrator) deadline() time.Time {
	if o.cfg.Budget <= 0 {
		return time.Time{}
	}
	return o.clock.Now().Add(o.cfg.Budget)
}

func (o *PlanOrchestrator) budgetExceeded(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return !o.clock.Now().Before(deadline)
}
