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
// steps file is checked complete and a final whole-plan audit approves, too
// many invocations fail in a row, the time budget is exhausted, progress
// stalls, or a signal interrupts it. Each iteration it re-reads the steps
// file and aims the tool at exactly the next unchecked step; once every box
// is checked, the next iteration is an audit of the whole plan instead. The
// run ends successfully only when all steps are checked AND the audit has
// created STOP.md: a STOP.md created while unchecked steps remain is deleted
// and the loop continues.
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
		o.checkpointNewSteps(ctx, before)
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
// file. Checked boxes alone are not success: the run ends cleanly only when
// every step is checked AND the whole-plan audit has recorded its approval as
// STOP.md. All boxes checked without STOP.md means the audit still has to run
// (the next iteration is the audit), and a STOP.md that appears while
// unchecked steps remain is deleted so the loop keeps going.
func (o *Orchestrator) checkCompletion() (models.Outcome, bool) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return models.OutcomeStopped, false // runOnce reports the read failure
	}
	steps := ParseSteps(content)
	switch {
	case AllStepsComplete(steps):
		if o.files.Exists(o.cfg.StopFile) {
			return models.OutcomeStopped, true
		}
	case len(steps) > 0:
		o.deletePrematureStop()
	case o.files.Exists(o.cfg.StopFile):
		// No parseable checkboxes to judge by, so fall back to trusting the
		// sentinel; otherwise a stepless STEPS.md could never end the run.
		return models.OutcomeStopped, true
	}
	return models.OutcomeStopped, false
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
// step the verifier or audit unchecked again counts as no progress, since
// both run before this check, so worker/reviewer ping-pong is bounded. A
// completed run (all boxes checked and the audit's STOP.md present) is never
// a stall: the approving audit checks no new step, and the next
// checkCompletion ends the run.
func (o *Orchestrator) stalledOut(completedBefore int) bool {
	if o.cfg.MaxStalledIterations <= 0 {
		return false
	}
	steps := o.parsedSteps()
	if CompletedStepCount(steps) > completedBefore || o.runComplete(steps) {
		o.stalled = 0
		return false
	}
	o.stalled++
	return o.stalled >= o.cfg.MaxStalledIterations
}

// runComplete reports whether the run's success condition holds: every step
// checked and the whole-plan audit's STOP.md approval present.
func (o *Orchestrator) runComplete(steps []Step) bool {
	return AllStepsComplete(steps) && o.files.Exists(o.cfg.StopFile)
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
// explanation in FIXES.md when it does not. The stance is deliberately
// skeptical (the verifier is the same model that did the work), the FIXES.md
// entry is structured so the re-run worker can find and act on it, and fixing
// is forbidden: a verifier that repairs code produces an unverified fix and
// skips the FIXES.md record.
func verifyPrompt(n int, step Step) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	fmt.Fprintf(&b, "Step %d claims complete: %s", n, sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	fmt.Fprintf(&b, " You are the reviewer, not the worker. Assume the step is "+
		"incomplete until you have run the check and seen it pass: verify by reading "+
		"the code and running the stated check. Read NOTES.md if it exists for the "+
		"conventions earlier steps recorded. "+
		"If not genuinely done, change the step's `[x]` back to `[ ]` in STEPS.md "+
		"and append an entry to FIXES.md under a `## Step %d` heading stating the "+
		"specific failing check and what would make it pass; if done, do nothing. "+
		"Do not fix anything yourself, do not modify code, and do not check or "+
		"uncheck any step other than step %d.", n, n)
	return b.String()
}

// checkpointNewSteps git-commits the working tree once per step that this
// iteration newly checked and that survived verification (a step the verifier
// rejected is unchecked again by now, so it is never committed). Runs after
// the verify pass so each commit captures a reviewed state, giving a rewind
// point per step. Skipped outside a git repository with a terminal note; a
// failed git command is noted and ignored, since checkpoints are a convenience
// and must not end the run.
func (o *Orchestrator) checkpointNewSteps(ctx context.Context, before []Step) {
	if !o.cfg.GitCheckpoint {
		return
	}
	for i, step := range o.parsedSteps() {
		if !step.Completed || (i < len(before) && before[i].Completed) {
			continue
		}
		if !o.files.Exists(".git") {
			fmt.Fprintln(o.terminal,
				"determined: not a git repository; skipping git checkpoint")
			return
		}
		o.gitCommit(ctx, i+1, step)
	}
}

// gitCheckpointTimeout bounds each git checkpoint command so a hung git
// operation cannot block the run: checkpoints are a convenience and must never
// stall the loop indefinitely.
const gitCheckpointTimeout = 2 * time.Minute

// gitCommit stages everything and commits it as the checkpoint for one step.
func (o *Orchestrator) gitCommit(ctx context.Context, n int, step Step) {
	message := fmt.Sprintf("determined: step %d: %s", n, strings.TrimSpace(step.Text))
	for _, inv := range []models.Invocation{
		{Binary: "git", Args: []string{"add", "-A"}},
		{Binary: "git", Args: []string{"commit", "-m", message}},
	} {
		runCtx, cancel := context.WithTimeout(ctx, gitCheckpointTimeout)
		err := o.runner.Run(runCtx, inv, o.terminal)
		cancel()
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: git checkpoint for step %d failed: %v\n", n, err)
			return
		}
	}
	fmt.Fprintf(o.terminal, "determined: git checkpoint committed for step %d\n", n)
}

// promptPreamble opens every injected prompt. Each invocation starts with a
// fresh context that knows nothing of the loop's protocol, so the file roles
// must be restated every time for the cost of a couple of sentences.
const promptPreamble = "You are one invocation of an orchestrated loop that runs an AI " +
	"coding tool once per step; you start with no memory of earlier invocations. " +
	"STEPS.md is the loop's source of truth, NOTES.md is cross-iteration memory, and " +
	"FIXES.md records why previously rejected work was reopened. "

// noParsableStepsPrompt is used when STEPS.md contains no checkbox-format
// steps, so the orchestrator cannot judge progress itself: the tool either
// restores a parseable step list or confirms the work is done with STOP.md.
// PLAN.md is the reference for what remains, since the corrupt steps file
// alone cannot answer that.
const noParsableStepsPrompt = promptPreamble +
	"Read STEPS.md. It contains no checkbox-format steps " +
	"(`- [ ]` items), so progress cannot be tracked. Read PLAN.md to determine what " +
	"work remains. If work remains, rewrite STEPS.md " +
	"as a markdown checkbox list, one `- [ ]` item per remaining step, each ending with " +
	"a `Done when:` line stating a checkable acceptance condition. " +
	"If the work is genuinely finished, create STOP.md. Do not start new work."

// auditPrompt is the final whole-plan review run once every step is checked.
// The audit either confirms the implementation satisfies the plan by creating
// STOP.md — the only thing that lets an all-checked run end successfully — or
// reopens the steps that fall short, sending the loop back to step execution.
// It must actually build and test rather than read: it is the only invocation
// positioned to catch integration failures between individually verified
// steps. STOP.md carries a short evidence report so approval cannot be an
// empty rubber stamp.
const auditPrompt = promptPreamble +
	"All steps in STEPS.md are checked complete. Read PLAN.md and STEPS.md. " +
	"Audit whether the implementation genuinely satisfies the plan. " +
	"Run the project's build and test suite as part of the audit, and check that the " +
	"steps work together as a whole, not just individually. " +
	"If a step is not actually satisfied, change its `[x]` back to `[ ]` in STEPS.md " +
	"and append the reason to FIXES.md; do not fix anything yourself. " +
	"If everything is satisfied, create STOP.md containing a short report: what was " +
	"built, what checks you ran, and their results. " +
	"Do not start work beyond this audit."

// iterationPrompt reads the steps file and builds this iteration's instruction:
// the whole-plan audit when every box is checked (checkCompletion only ends
// the run once the audit's STOP.md exists), otherwise the next unchecked
// step. A missing next step then means the file had no parseable steps.
func (o *Orchestrator) iterationPrompt() (string, error) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return "", err
	}
	steps := ParseSteps(content)
	if AllStepsComplete(steps) {
		fmt.Fprintln(o.terminal,
			"determined: all steps checked; auditing the whole plan before declaring success")
		return auditPrompt, nil
	}
	i, step, ok := NextIncompleteStep(steps)
	if !ok {
		return noParsableStepsPrompt, nil
	}
	return stepPrompt(i+1, step), nil
}

// stepPrompt builds the execute instruction for a single step: work only that
// step, meet its acceptance criterion, and check its box when done. NOTES.md
// carries knowledge between otherwise-independent invocations: each iteration
// reads what earlier steps recorded and appends what later steps need to know.
// The step is named by number so the tool edits the right checkbox even when
// step texts look alike. FIXES.md is offered first because a reopened step's
// worker would otherwise repeat the exact mistake the reviewer rejected. The
// box may only be checked after the acceptance check passes — self-correction
// here is cheaper than a verifier rejection, which costs a full extra
// invocation plus stall-counter pressure. Creating STOP.md and touching other
// boxes are forbidden outright: the orchestrator can clean both up, but
// prevention is cheaper than deletion, and a wrongly ticked box silently
// skips work until the audit.
func stepPrompt(n int, step Step) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString("Read NOTES.md if it exists before starting. ")
	b.WriteString("If FIXES.md exists, read it too: this step may have been reopened, " +
		"and it explains what was wrong last time. ")
	fmt.Fprintf(&b, "Work on exactly step %d and no other: ", n)
	b.WriteString(sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Its acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	fmt.Fprintf(&b, " Read PLAN.md for overall context if the step is unclear. "+
		"Run the acceptance check yourself and mark step %d `[x]` in STEPS.md only "+
		"once it passes. Do not check or uncheck any other step, and do not create "+
		"STOP.md. Before finishing, append to NOTES.md any decisions, conventions, or "+
		"gotchas later steps need to know.", n)
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
