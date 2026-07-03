package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"determined/src/models"
)

// CommandRunner runs one AI-coding-tool invocation, streaming its combined
// stdout and stderr to out. The real implementation is clients.ExecCommandRunner.
type CommandRunner interface {
	Run(ctx context.Context, inv models.Invocation, out io.Writer) error
}

// Clock reads wall-clock time.
type Clock interface {
	Now() time.Time
}

// LogSink opens a fresh, closable log writer for each iteration.
type LogSink interface {
	OpenIteration(iteration int) (io.WriteCloser, error)
}

// Orchestrator runs the AI coding tool in a loop until every step in the
// steps file is checked complete, an invocation fails, the time budget is
// exhausted, progress stalls, or a signal interrupts it. Each iteration it re-reads the steps
// file and aims the tool at exactly the next unchecked step. Completion is
// decided by the parsed checkboxes, never by the tool: a STOP.md created
// while unchecked steps remain is deleted and the loop continues.
type Orchestrator struct {
	runner   CommandRunner
	files    FileStore
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.Config

	iteration int
	// stalled counts consecutive iterations that checked no new step; hitting
	// cfg.MaxStalledIterations ends the run with OutcomeStalled.
	stalled int
}

// NewOrchestrator wires an orchestrator from its dependencies.
func NewOrchestrator(
	runner CommandRunner,
	files FileStore,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.Config,
) *Orchestrator {
	return &Orchestrator{
		runner:   runner,
		files:    files,
		clock:    clock,
		logs:     logs,
		terminal: terminal,
		cfg:      cfg,
	}
}

// Run executes the loop and returns the terminal outcome.
func (o *Orchestrator) Run(ctx context.Context) models.Outcome {
	if !o.protocolFilesPresent() {
		return models.OutcomeMissingFiles
	}
	deadline := o.deadline()
	for {
		if outcome, stop := o.preIteration(ctx, deadline); stop {
			return outcome
		}
		completedBefore := o.completedStepCount()
		if outcome, stop := o.runOnce(ctx); stop {
			return outcome
		}
		if o.stalledOut(completedBefore) {
			fmt.Fprintf(o.terminal,
				"determined: no step checked in %d consecutive iterations; stopping\n",
				o.cfg.MaxStalledIterations)
			return models.OutcomeStalled
		}
	}
}

// protocolFilesPresent verifies the plan-produced files exist before any tool
// run, naming each missing one. Without them the loop has nothing to aim the
// tool at, so failing fast beats burning iterations on an unplanned directory.
// A stale STOP.md is not checked here: the first preIteration deletes it (with
// a warning) whenever unchecked steps remain.
func (o *Orchestrator) protocolFilesPresent() bool {
	present := true
	for _, f := range []string{o.cfg.PlanFile, o.cfg.StepsFile} {
		if !o.files.Exists(f) {
			fmt.Fprintf(o.terminal,
				"determined: %s not found; run `determined --plan \"<goal>\"` to create it first\n", f)
			present = false
		}
	}
	return present
}

// preIteration applies the between-iteration guards before starting more work.
// The budget is checked here, so a running invocation always finishes first.
func (o *Orchestrator) preIteration(ctx context.Context, deadline time.Time) (models.Outcome, bool) {
	if ctx.Err() != nil {
		return models.OutcomeInterrupted, true
	}
	if outcome, stop := o.checkCompletion(); stop {
		return outcome, true
	}
	if o.budgetExceeded(deadline) {
		return models.OutcomeBudgetExceeded, true
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// checkCompletion decides whether the run is finished by parsing the steps
// file. The checkboxes are the authority, not STOP.md: the sentinel is written
// on completion only for compatibility, and one that appears while unchecked
// steps remain is deleted so the loop keeps going.
func (o *Orchestrator) checkCompletion() (models.Outcome, bool) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return models.OutcomeStopped, false // runOnce reports the read failure
	}
	steps := ParseSteps(content)
	switch {
	case AllStepsComplete(steps):
		o.writeStopSentinel()
		return models.OutcomeStopped, true
	case len(steps) > 0:
		o.deletePrematureStop()
	case o.files.Exists(o.cfg.StopFile):
		// No parseable checkboxes to judge by, so fall back to trusting the
		// sentinel; otherwise a stepless STEPS.md could never end the run.
		return models.OutcomeStopped, true
	}
	return models.OutcomeStopped, false
}

// writeStopSentinel records completion in STOP.md for compatibility with
// anything watching for the sentinel. Best-effort: the run is already over.
func (o *Orchestrator) writeStopSentinel() {
	if o.files.Exists(o.cfg.StopFile) {
		return
	}
	if err := o.files.Write(o.cfg.StopFile, "All steps in STEPS.md are checked complete.\n"); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.StopFile, err)
	}
}

// deletePrematureStop removes a STOP.md created while unchecked steps remain,
// warning the user that the tool tried to declare completion early.
func (o *Orchestrator) deletePrematureStop() {
	if !o.files.Exists(o.cfg.StopFile) {
		return
	}
	if err := o.files.Remove(o.cfg.StopFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not delete premature %s: %v\n", o.cfg.StopFile, err)
		return
	}
	fmt.Fprintf(o.terminal,
		"determined: warning: %s existed while unchecked steps remain; deleted it and continuing\n",
		o.cfg.StopFile)
}

// completedStepCount parses the steps file and counts the checked steps. A
// read failure counts as zero; the surrounding loop surfaces the failure
// itself on the next runOnce.
func (o *Orchestrator) completedStepCount() int {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return 0
	}
	return CompletedStepCount(ParseSteps(content))
}

// stalledOut updates the consecutive-no-progress counter by comparing the
// completed-step count against its pre-iteration snapshot, and reports whether
// the stall cap has been hit. Any newly checked step resets the counter.
func (o *Orchestrator) stalledOut(completedBefore int) bool {
	if o.cfg.MaxStalledIterations <= 0 {
		return false
	}
	if o.completedStepCount() > completedBefore {
		o.stalled = 0
		return false
	}
	o.stalled++
	return o.stalled >= o.cfg.MaxStalledIterations
}

// runOnce runs a single invocation, teeing its output to the terminal and a
// per-iteration log. It reports whether the loop should stop.
func (o *Orchestrator) runOnce(ctx context.Context) (models.Outcome, bool) {
	prompt, err := o.iterationPrompt()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.StepsFile, err)
		return models.OutcomeDroidFailed, true
	}
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return models.OutcomeDroidFailed, true
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	if err := o.runner.Run(ctx, o.cfg.Tool.Invocation(prompt), out); err != nil {
		return o.classifyFailure(ctx), true
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// noParsableStepsPrompt is used when STEPS.md contains no checkbox-format
// steps, so the orchestrator cannot judge progress itself: the tool either
// restores a parseable step list or confirms the work is done with STOP.md.
const noParsableStepsPrompt = "Read STEPS.md. It contains no checkbox-format steps " +
	"(`- [ ]` items), so progress cannot be tracked. If work remains, rewrite STEPS.md " +
	"as a markdown checkbox list, one `- [ ]` item per remaining step, each ending with " +
	"a `Done when:` line stating a checkable acceptance condition. " +
	"If the work is genuinely finished, create STOP.md. Do not start new work."

// iterationPrompt reads the steps file and builds this iteration's instruction,
// aiming the tool at exactly the next unchecked step. When the run reaches
// here, at least one step is unchecked (checkCompletion ends the run first
// otherwise), so a missing next step means the file had no parseable steps.
func (o *Orchestrator) iterationPrompt() (string, error) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return "", err
	}
	step, ok := NextIncompleteStep(ParseSteps(content))
	if !ok {
		return noParsableStepsPrompt, nil
	}
	return stepPrompt(step), nil
}

// stepPrompt builds the execute instruction for a single step: work only that
// step, meet its acceptance criterion, and check its box when done.
func stepPrompt(step Step) string {
	var b strings.Builder
	b.WriteString("Work on exactly this step and no other: ")
	b.WriteString(sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Its acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	b.WriteString(" Mark it `[x]` in STEPS.md when done.")
	return b.String()
}

// sentence trims s and ensures it ends with a period, so injected step text
// composes into readable prompt sentences.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// classifyFailure distinguishes a genuine tool failure from an interruption,
// since a cancelled context kills the child and surfaces as an error too.
func (o *Orchestrator) classifyFailure(ctx context.Context) models.Outcome {
	if ctx.Err() != nil {
		return models.OutcomeInterrupted
	}
	return models.OutcomeDroidFailed
}

// deadline returns the wall-clock instant the run must stop by, or the zero
// time when the budget is unlimited.
func (o *Orchestrator) deadline() time.Time {
	if o.cfg.Budget <= 0 {
		return time.Time{}
	}
	return o.clock.Now().Add(o.cfg.Budget)
}

func (o *Orchestrator) budgetExceeded(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return !o.clock.Now().Before(deadline)
}
