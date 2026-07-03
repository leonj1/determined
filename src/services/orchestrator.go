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
// steps file is checked complete, too many invocations fail in a row, the time
// budget is exhausted, progress stalls, or a signal interrupts it. Each iteration it re-reads the steps
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
	// failures counts consecutive failed tool invocations; hitting
	// cfg.MaxConsecutiveFailures ends the run with OutcomeDroidFailed. Any
	// successful invocation resets it.
	failures int
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
		before := o.parsedSteps()
		if outcome, stop := o.runOnce(ctx); stop {
			return outcome
		}
		if outcome, stop := o.verifyNewSteps(ctx, before); stop {
			return outcome
		}
		if o.stalledOut(CompletedStepCount(before)) {
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

// parsedSteps reads and parses the steps file. A read failure yields no
// steps; the surrounding loop surfaces the failure itself on the next runOnce.
func (o *Orchestrator) parsedSteps() []Step {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return nil
	}
	return ParseSteps(content)
}

// stalledOut updates the consecutive-no-progress counter by comparing the
// completed-step count against its pre-iteration snapshot, and reports whether
// the stall cap has been hit. Any newly checked step resets the counter; a
// step the verifier unchecked again counts as no progress, since verification
// runs before this check.
func (o *Orchestrator) stalledOut(completedBefore int) bool {
	if o.cfg.MaxStalledIterations <= 0 {
		return false
	}
	if CompletedStepCount(o.parsedSteps()) > completedBefore {
		o.stalled = 0
		return false
	}
	o.stalled++
	return o.stalled >= o.cfg.MaxStalledIterations
}

// runOnce runs a single work invocation aimed at the next unchecked step. It
// reports whether the loop should stop.
func (o *Orchestrator) runOnce(ctx context.Context) (models.Outcome, bool) {
	prompt, err := o.iterationPrompt()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.StepsFile, err)
		return models.OutcomeDroidFailed, true
	}
	return o.invoke(ctx, prompt)
}

// invoke runs one tool invocation with the given prompt, teeing its output to
// the terminal and a fresh per-iteration log. It reports whether the loop
// should stop.
func (o *Orchestrator) invoke(ctx context.Context, prompt string) (models.Outcome, bool) {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return models.OutcomeDroidFailed, true
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	runCtx, cancel := o.iterationContext(ctx)
	defer cancel()
	if err := o.runner.Run(runCtx, o.cfg.Tool.Invocation(prompt), out); err != nil {
		return o.recordFailure(ctx, err)
	}
	o.failures = 0
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// verifyNewSteps runs an independent reviewer invocation over every step the
// last iteration newly checked, comparing the steps file against its
// pre-iteration snapshot. The verifier unchecks a step whose acceptance
// criterion is not genuinely met (recording why in FIXES.md), so the loop
// re-runs it. Ping-pong between worker and verifier is bounded because a
// rejection leaves the completed count unchanged, which the stall counter
// (checked right after this pass) treats as a no-progress iteration. A failed
// verifier invocation counts toward the consecutive-failure cap like any
// other; the step simply stays checked.
func (o *Orchestrator) verifyNewSteps(ctx context.Context, before []Step) (models.Outcome, bool) {
	if !o.cfg.Verify {
		return models.OutcomeStopped, false
	}
	for i, step := range o.parsedSteps() {
		if !step.Completed || (i < len(before) && before[i].Completed) {
			continue
		}
		fmt.Fprintf(o.terminal, "determined: verifying step %d\n", i+1)
		if outcome, stop := o.invoke(ctx, verifyPrompt(i+1, step)); stop {
			return outcome, true
		}
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// verifyPrompt builds the reviewer instruction for one newly checked step:
// confirm the acceptance criterion actually holds, and reopen the step with an
// explanation in FIXES.md when it does not.
func verifyPrompt(n int, step Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Step %d claims complete: %s", n, sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	b.WriteString(" Verify by reading the code and running the stated check. " +
		"If not genuinely done, change the step's `[x]` back to `[ ]` in STEPS.md " +
		"and append what is wrong to FIXES.md; if done, do nothing.")
	return b.String()
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
// step, meet its acceptance criterion, and check its box when done. NOTES.md
// carries knowledge between otherwise-independent invocations: each iteration
// reads what earlier steps recorded and appends what later steps need to know.
func stepPrompt(step Step) string {
	var b strings.Builder
	b.WriteString("Read NOTES.md if it exists before starting. ")
	b.WriteString("Work on exactly this step and no other: ")
	b.WriteString(sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Its acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	b.WriteString(" Mark it `[x]` in STEPS.md when done. " +
		"Before finishing, append to NOTES.md any decisions, conventions, or " +
		"gotchas later steps need to know.")
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

// iterationContext bounds a single invocation by cfg.MaxIterationDuration so
// a hung tool cannot hang the loop forever. recordFailure inspects the parent
// ctx, not this one, so a timeout surfaces as an ordinary retryable failure
// rather than an interruption.
func (o *Orchestrator) iterationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.cfg.MaxIterationDuration <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, o.cfg.MaxIterationDuration)
}

// recordFailure decides what a failed invocation means for the run. An
// interruption stops immediately, since a cancelled context kills the child
// and surfaces as an error too. A genuine tool failure (rate limit, crash) is
// often transient, so the same iteration is retried until
// cfg.MaxConsecutiveFailures failures occur with no success in between.
func (o *Orchestrator) recordFailure(ctx context.Context, err error) (models.Outcome, bool) {
	if ctx.Err() != nil {
		return models.OutcomeInterrupted, true
	}
	o.failures++
	if o.failures >= o.cfg.MaxConsecutiveFailures {
		fmt.Fprintf(o.terminal,
			"determined: tool invocation failed %d consecutive times; stopping: %v\n",
			o.failures, err)
		return models.OutcomeDroidFailed, true
	}
	fmt.Fprintf(o.terminal,
		"determined: tool invocation failed (%d of %d consecutive before aborting): %v; retrying\n",
		o.failures, o.cfg.MaxConsecutiveFailures, err)
	return models.OutcomeStopped, false // outcome ignored when stop is false
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
