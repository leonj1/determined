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

// ExecStatusReporter receives the execute loop's observable events for the
// interactive status page's Execution tab. The real implementation is
// PlanStatusService; a nil reporter disables reporting.
type ExecStatusReporter interface {
	ProgressSink
	StartExecution()
	BeginExecLogEntry(message string) int
	AppendExecLogOutput(text string)
	SettleExecLogEntryAt(i int, state models.EntryState)
	SetTaskSteps(steps []models.TaskStep)
	SetExecStopReason(reason, advice string)
	FinishExecution(succeeded bool)
	StartExplanation()
	SetExplanation(text string)
	FinishExplanation(succeeded bool)
	StartQuiz()
	SetQuiz(questions []models.QuizQuestion)
	FinishQuiz(succeeded bool)
}

// execOutputSink adapts an ExecStatusReporter onto LogOutputSink so
// logEntryWriter can stream tool output into the execution log.
type execOutputSink struct{ status ExecStatusReporter }

func (s execOutputSink) AppendLogOutput(text string) { s.status.AppendExecLogOutput(text) }

// Orchestrator runs the AI coding tool until every step and enabled review
// gate passes, too many invocations fail, the budget expires, progress stalls,
// or a signal interrupts it. Each iteration targets the next unchecked step.
// Once every box is checked, specialist reviews precede the whole-plan audit.
// Only that final sequence can create an accepted STOP.md.
type Orchestrator struct {
	runner   CommandRunner
	files    FileStore
	clock    Clock
	logs     LogSink
	terminal io.Writer
	status   ExecStatusReporter
	guard    *TamperGuard
	cfg      models.Config

	iteration int
	// stalled counts consecutive iterations that checked no new step; hitting
	// cfg.MaxStalledIterations ends the run with OutcomeStalled.
	stalled int
	// failures counts consecutive failed tool invocations; hitting
	// cfg.MaxConsecutiveFailures ends the run with OutcomeDroidFailed. Any
	// successful invocation resets it.
	failures int
	// stepIndex is the index of the unchecked step the loop is currently
	// aiming at (-1 before the first iteration or once every box is checked),
	// and stepStarted is when it became the target. Together they bound one
	// step's cumulative runtime by cfg.StepMaxRuntime.
	stepIndex   int
	stepStarted time.Time
}

// invocationResult distinguishes a successful tool run from a retryable
// failure, which lets a review sequence stop before later gates run.
type invocationResult struct {
	outcome   models.Outcome
	stop      bool
	succeeded bool
	// entry indexes this invocation's execution log entry, so a later signal
	// can revise its outcome. It is -1 when no status page is attached.
	entry int
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
		runner:    runner,
		files:     files,
		clock:     clock,
		logs:      logs,
		terminal:  terminal,
		guard:     NewTamperGuard(files, cfg.ProtectedFiles),
		cfg:       cfg,
		stepIndex: -1,
	}
}

// WithStatusReporter attaches the interactive status reporter and returns the
// orchestrator for chaining. Without one the run is terminal-only.
func (o *Orchestrator) WithStatusReporter(status ExecStatusReporter) *Orchestrator {
	o.status = status
	return o
}

// Run executes the loop and returns the terminal outcome.
func (o *Orchestrator) Run(ctx context.Context) models.Outcome {
	if !o.protocolFilesPresent() {
		return models.OutcomeMissingFiles
	}
	o.reportStart()
	outcome := o.loop(ctx)
	o.reportFinish(ctx, outcome)
	return outcome
}

// loop is the iteration cycle Run wraps with start/finish status reporting.
func (o *Orchestrator) loop(ctx context.Context) models.Outcome {
	deadline := o.deadline()
	for {
		if outcome, stop := o.preIteration(ctx, deadline); stop {
			return outcome
		}
		before := o.parsedSteps()
		if o.stepOverran(before) {
			fmt.Fprintf(o.terminal,
				"determined: step %d has been running for over %s without completing; stopping\n",
				o.stepIndex+1, o.cfg.StepMaxRuntime)
			return models.OutcomeStepTimeout
		}
		if outcome, stop := o.runOnce(ctx); stop {
			return outcome
		}
		if outcome, stop := o.verifyNewSteps(ctx, before); stop {
			return outcome
		}
		o.checkpointNewSteps(ctx, before)
		o.reportTaskSteps()
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
// STOP.md. With specialist gates enabled, an existing sentinel is discarded
// so the current run performs those reviews before auditing. A STOP.md that
// appears while unchecked steps remain is also deleted.
func (o *Orchestrator) checkCompletion() (models.Outcome, bool) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return models.OutcomeStopped, false // runOnce reports the read failure
	}
	steps := ParseSteps(content)
	switch {
	case AllStepsComplete(steps):
		if o.files.Exists(o.cfg.StopFile) {
			if o.cfg.SpecializedReviews {
				o.deleteUnreviewedStop()
				return models.OutcomeStopped, false
			}
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

// deleteUnreviewedStop prevents a sentinel left by a worker or an earlier run
// from bypassing the enabled specialist gates in the current run.
func (o *Orchestrator) deleteUnreviewedStop() {
	if err := o.files.Remove(o.cfg.StopFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not delete unreviewed %s: %v\n", o.cfg.StopFile, err)
		return
	}
	fmt.Fprintf(o.terminal,
		"determined: warning: removed existing %s so specialist reviews can run\n",
		o.cfg.StopFile)
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

// stepOverran tracks how long the loop has been aiming at the same unchecked
// step and reports whether that step's cumulative runtime has exhausted
// cfg.StepMaxRuntime. Like the budget, the cap is checked between invocations,
// so a running invocation always finishes first. Whenever the target step
// changes — checked complete, or an earlier step reopened — the timer
// restarts; a step a reviewer unchecks again keeps its timer, so
// worker/reviewer ping-pong on one step is time-bounded too.
func (o *Orchestrator) stepOverran(steps []Step) bool {
	if o.cfg.StepMaxRuntime <= 0 {
		return false
	}
	i, ok := NextIncompleteStepIndex(steps)
	if !ok {
		o.stepIndex = -1
		return false
	}
	if i != o.stepIndex {
		o.stepIndex = i
		o.stepStarted = o.clock.Now()
		return false
	}
	return !o.clock.Now().Before(o.stepStarted.Add(o.cfg.StepMaxRuntime))
}

// runOnce runs a single work invocation aimed at the next unchecked step. It
// reports whether the loop should stop.
func (o *Orchestrator) runOnce(ctx context.Context) (models.Outcome, bool) {
	if AllStepsComplete(o.parsedSteps()) {
		return o.runCompletionReviews(ctx)
	}
	prompt, progress, err := o.iterationPrompt()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.StepsFile, err)
		return models.OutcomeDroidFailed, true
	}
	result := o.invoke(ctx, prompt, progress)
	return result.outcome, result.stop
}

// invoke runs one tool invocation with the given prompt, teeing its output to
// the terminal and a fresh per-iteration log. It reports whether the loop
// should stop.
func (o *Orchestrator) invoke(
	ctx context.Context,
	prompt string,
	progress progressMessage,
) invocationResult {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return invocationResult{outcome: models.OutcomeDroidFailed, stop: true}
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	writeProgress(out, o.clock, progress)
	notifyProgress(o.status, progress)
	entry := -1
	if o.status != nil {
		entry = o.status.BeginExecLogEntry(string(progress))
		statusLog := newLogEntryWriter(execOutputSink{o.status})
		defer statusLog.Flush()
		out = io.MultiWriter(out, statusLog)
	}
	runCtx, cancel := o.iterationContext(ctx)
	defer cancel()
	tampered, err := o.guardedRun(runCtx, prompt, out)
	if err != nil {
		o.settleEntry(entry, models.EntryStateError)
		outcome, stop := o.recordFailure(ctx, err)
		return invocationResult{outcome: outcome, stop: stop, entry: entry}
	}
	if tampered {
		o.settleEntry(entry, models.EntryStateWarn)
	} else {
		o.settleEntry(entry, models.EntryStateOK)
	}
	o.failures = 0
	return invocationResult{outcome: models.OutcomeStopped, succeeded: true, entry: entry}
}

// settleEntry records an invocation's outcome on its execution log entry, if
// the run has a status page attached.
func (o *Orchestrator) settleEntry(entry int, state models.EntryState) {
	if o.status == nil || entry < 0 {
		return
	}
	o.status.SettleExecLogEntryAt(entry, state)
}

// guardedRun runs one tool invocation with the protected files snapshotted
// around it, reverting any the tool modified — even when the invocation itself
// failed, since a tool can tamper and then crash. It reports whether any
// protected file was reverted, which marks the invocation as warning-worthy.
func (o *Orchestrator) guardedRun(
	ctx context.Context,
	prompt string,
	out io.Writer,
) (bool, error) {
	snapshot := o.guard.Snapshot()
	err := o.runner.Run(ctx, o.cfg.Tool.Invocation(prompt), out)
	restored := o.guard.RestoreTampered(snapshot)
	o.reportTampering(restored)
	return len(restored) > 0, err
}

// reportTampering warns about each protected file the tool modified, on the
// terminal and in FIXES.md so the next invocation sees the correction.
func (o *Orchestrator) reportTampering(restored []string) {
	for _, path := range restored {
		fmt.Fprintf(o.terminal,
			"determined: warning: tool modified protected file %s during iteration %d; restored it\n",
			path, o.iteration)
		o.appendTamperNote(path)
	}
}

func (o *Orchestrator) appendTamperNote(path string) {
	note := fmt.Sprintf("- Iteration %d modified %s, which defines the work's acceptance "+
		"criteria and must not change during execution; the change was reverted. "+
		"Satisfy the criteria as written instead.\n", o.iteration, path)
	if err := o.files.Append("FIXES.md", note); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record tampering in FIXES.md: %v\n", err)
	}
}

// verifyNewSteps runs independent reviewer invocations over every step the
// last iteration newly checked, comparing the steps file against its
// pre-iteration snapshot. Each step gets a simplicity check first, then a
// correctness verification; either reviewer unchecks a step that fails its
// standard (recording why in FIXES.md), so the loop re-runs it. Ping-pong
// between worker and reviewers is bounded because a rejection leaves the
// completed count unchanged, which the stall counter (checked right after
// this pass) treats as a no-progress iteration. A failed reviewer invocation
// counts toward the consecutive-failure cap like any other; the step simply
// stays checked.
func (o *Orchestrator) verifyNewSteps(ctx context.Context, before []Step) (models.Outcome, bool) {
	if !o.cfg.Verify {
		return models.OutcomeStopped, false
	}
	for i, step := range o.parsedSteps() {
		if !step.Completed || (i < len(before) && before[i].Completed) {
			continue
		}
		result := o.verifyStep(ctx, i, step)
		if result.stop {
			return result.outcome, true
		}
		if !result.succeeded {
			return models.OutcomeStopped, false
		}
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// verifyStep runs the reviewer invocations for one newly checked step: the
// simplicity check first, then the correctness verification. A simplicity
// rejection unchecks the step, so the correctness check is skipped — the
// redone step is reviewed again on a later iteration.
func (o *Orchestrator) verifyStep(ctx context.Context, i int, step Step) invocationResult {
	progress := progressMessage(fmt.Sprintf("checking simplicity of step %d", i+1))
	result := o.invoke(ctx, simplicityPrompt(i+1, step), progress)
	if result.stop || !result.succeeded {
		return result
	}
	if o.markRejectedStep(i, result.entry) {
		return result
	}
	progress = progressMessage(fmt.Sprintf("verifying step %d", i+1))
	result = o.invoke(ctx, verifyPrompt(i+1, step), progress)
	if result.stop || !result.succeeded {
		return result
	}
	o.markRejectedStep(i, result.entry)
	return result
}

// markRejectedStep tints a reviewer's own log entry yellow when that reviewer
// unchecked the step it just reviewed, and reports whether that rejection
// happened. A rejection means the step was reported done but was not, which is
// a warning about the run rather than a failure of the reviewer invocation —
// that invocation did its job.
func (o *Orchestrator) markRejectedStep(i, entry int) bool {
	steps := o.parsedSteps()
	if i >= len(steps) || steps[i].Completed {
		return false
	}
	o.settleEntry(entry, models.EntryStateWarn)
	return true
}

// specializedReview describes one independent whole-codebase review gate.
type specializedReview struct {
	name  string
	focus string
}

// runCompletionReviews runs every enabled specialist before the general
// whole-plan audit, which also enforces the CRITERIA.md and TESTS.md test gates.
// A failure or a reviewer-created remediation step prevents later gates from
// running until the next outer iteration.
func (o *Orchestrator) runCompletionReviews(ctx context.Context) (models.Outcome, bool) {
	result := o.invoke(ctx, docsPrompt, "updating project documentation")
	if result.stop {
		return result.outcome, true
	}
	if !result.succeeded {
		return models.OutcomeStopped, false
	}
	if o.cfg.SpecializedReviews {
		reviews := o.runSpecializedReviews(ctx)
		if reviews.stop {
			return reviews.outcome, true
		}
		if !reviews.succeeded {
			return models.OutcomeStopped, false
		}
	}
	result = o.invoke(ctx, auditPrompt, "auditing the whole plan")
	if result.succeeded && o.runComplete(o.parsedSteps()) {
		return models.OutcomeStopped, true
	}
	return result.outcome, result.stop
}

func (o *Orchestrator) runSpecializedReviews(ctx context.Context) invocationResult {
	for _, review := range specializedReviewSequence() {
		progress := progressMessage(fmt.Sprintf("running %s review", review.name))
		result := o.invoke(ctx, specializedReviewPrompt(review), progress)
		if result.stop {
			return result
		}
		if !result.succeeded || !AllStepsComplete(o.parsedSteps()) {
			return invocationResult{outcome: models.OutcomeStopped}
		}
	}
	return invocationResult{outcome: models.OutcomeStopped, succeeded: true}
}

func specializedReviewSequence() []specializedReview {
	return []specializedReview{
		{name: "security", focus: "authentication and authorization, trust boundaries, injection, input validation, secrets, cryptography, dependency risk, and sensitive-data exposure"},
		{name: "performance", focus: "algorithmic complexity, repeated or unbounded work, database/network/file I/O, allocations, concurrency, resource leaks, and evidence from relevant benchmarks or profiling"},
		{name: "reliability and maintainability", focus: "error handling, race conditions, lifecycle cleanup, edge cases, test gaps, API compatibility, readability, coupling, and consistency with project conventions"},
	}
}

func specializedReviewPrompt(review specializedReview) string {
	return fmt.Sprintf("Act as the independent %s specialist. Read PLAN.md, STEPS.md, and the implementation. Review the completed work specifically for %s. Run relevant checks when practical and report only concrete, actionable findings caused or exposed by this work. If you find a material issue, append the finding and evidence to FIXES.md, then reopen the most relevant step in STEPS.md; if no existing step fits, append a new unchecked remediation step with a `Done when:` criterion. If no material issue remains, do nothing. Do not implement fixes during this review.", review.name, review.focus)
}

// stepClaim states one newly checked step — its text, purpose, and acceptance
// criterion — as the shared preamble of the per-step reviewer prompts.
func stepClaim(n int, step Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Step %d claims complete: %s", n, sentence(step.Text))
	if step.Purpose != "" {
		b.WriteString(" Its purpose: ")
		b.WriteString(sentence(step.Purpose))
	}
	if step.DoneWhen != "" {
		b.WriteString(" Acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	return b.String()
}

// simplicityPrompt builds the reviewer instruction that runs before the
// correctness verification for one newly checked step: judge whether the
// implementation is the simplest solution that satisfies the step, and reopen
// the step with the simpler approach recorded in FIXES.md when it is not.
func simplicityPrompt(n int, step Step) string {
	return stepClaim(n, step) +
		" Review this step's implementation for simplicity: could the same acceptance " +
		"criterion be satisfied with a materially simpler solution? Look for needless " +
		"abstraction, speculative generality, duplicated or dead code, and complexity " +
		"the criterion does not require. If a materially simpler solution exists, " +
		"change the step's `[x]` back to `[ ]` in STEPS.md and append the simpler " +
		"approach to FIXES.md; if the implementation is already reasonably simple, do nothing."
}

// verifyPrompt builds the reviewer instruction for one newly checked step:
// confirm the acceptance criterion actually holds, and reopen the step with an
// explanation in FIXES.md when it does not.
func verifyPrompt(n int, step Step) string {
	return stepClaim(n, step) +
		" Verify by reading the code and running the stated check. " +
		"If not genuinely done, change the step's `[x]` back to `[ ]` in STEPS.md " +
		"and append what is wrong to FIXES.md; if done, do nothing."
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
	writeProgress(o.terminal, o.clock,
		progressMessage(fmt.Sprintf("checkpointing step %d", n)))
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

// noParsableStepsPrompt is used when STEPS.md contains no checkbox-format
// steps, so the orchestrator cannot judge progress itself: the tool either
// restores a parseable step list or confirms the work is done with STOP.md.
const noParsableStepsPrompt = "Read STEPS.md. It contains no checkbox-format steps " +
	"(`- [ ]` items), so progress cannot be tracked. If work remains, rewrite STEPS.md " +
	"as a markdown checkbox list, one `- [ ]` item per remaining step, each with a " +
	"`Purpose:` line stating the step's functional intent and ending with " +
	"a `Done when:` line stating a checkable acceptance condition. " +
	"If the work is genuinely finished, create STOP.md. Do not start new work."

// docsPrompt updates the project's own documentation once every step is
// checked, before any specialist review and the final audit. It is the only
// completion-stage invocation that edits files rather than reporting findings.
const docsPrompt = "All steps in STEPS.md are checked complete. Read PLAN.md, STEPS.md, and the implementation. " +
	"Update the project's existing documentation so it describes the work as it now stands: " +
	"README.md, plus any other documentation the project already keeps (a docs/ directory, " +
	"AGENTS.md, CLAUDE.md, BUILD.md, usage or configuration references, changelogs). " +
	"Only document behavior this work actually changed or added: new or renamed commands, " +
	"flags, environment variables, endpoints, configuration, and setup or build steps. " +
	"Correct any statement this work made wrong. Do not create new documentation files unless a " +
	"documented surface has nowhere to live, do not restate the plan, and do not change code, " +
	"tests, PLAN.md, STEPS.md, TESTS.md, or CRITERIA.md. If no documentation needs changing, do nothing."

// auditPrompt is the final whole-plan review run once every step is checked.
// It enforces the plan plus CRITERIA.md and TESTS.md test existence and passing.
// The audit either creates STOP.md — the only thing that lets an all-checked
// run end successfully — or sends remediation back to step execution.
const auditPrompt = "All steps in STEPS.md are checked complete. Read PLAN.md and STEPS.md. " +
	"Audit whether the implementation genuinely satisfies the plan. " +
	"If CRITERIA.md exists, also audit that each of its BDD journey tests exists as an automated test and passes. " +
	"If TESTS.md exists, also audit that each of its journey and BDD tests exists as an automated test and passes. " +
	"If a step is not actually satisfied, change its `[x]` back to `[ ]` in STEPS.md " +
	"and append the reason to FIXES.md. If a required CRITERIA.md or TESTS.md test is missing or failing, " +
	"append a new `- [ ]` step to STEPS.md with a `Done when:` requiring that test to be implemented and passing, " +
	"and append the reason to FIXES.md. " +
	"If everything is satisfied, create STOP.md. " +
	"Do not start work beyond this audit."

// explainPrompt asks for a presentation-only walkthrough after a successful
// run, naming the configured artifact so alternate configurations still work.
func explainPrompt(cfg models.Config) string {
	return "Execution is complete. Inspect the code changes this run produced " +
		"(use `git log` and `git diff` against the branch's starting point; fall back to comparing with " +
		"PLAN.md/STEPS.md if git is unavailable). Write only " + cfg.ExplanationFile + ". " +
		"Start with a single short paragraph giving the intuition of what the changes accomplish — no heading, no code. " +
		"Then present the most important changes in descending order of importance. Introduce each change with a " +
		"short, descriptive, unique markdown `## ` heading. Headings that differ only by capitalization or punctuation " +
		"are not unique because they produce the same link. For each change: first a brief " +
		"plain-language explanation of why this code matters and what it does, then the relevant diff in a fenced " +
		"```diff block using unified diff format with `+`/`-` line prefixes and a `--- / +++` file header. " +
		"Cover only the changes that carry the design; skip mechanical edits. Do not modify any other file, " +
		"implement anything, or create STOP.md."
}

// quizPrompt asks for a machine-readable knowledge check grounded only in the
// explanation, naming both configured artifacts explicitly.
func quizPrompt(cfg models.Config) string {
	return "Read " + cfg.ExplanationFile + ". Every question must be answerable from that explanation alone. " +
		"Write only " + cfg.QuizFile + " as JSON with exactly this shape: " +
		`{"questions":[{"question":"What changed?","choices":["A","B","C","D"],` +
		`"correctIndex":0,"rationale":"One sentence explaining the answer.",` +
		`"sourceSection":"Exact heading text"}]}. ` +
		"Create exactly 5 questions with exactly 4 non-empty choices each. Use a zero-based correctIndex " +
		"from 0 through 3 and a non-empty one-sentence rationale for every question. Every question must include " +
		"sourceSection copied verbatim from one of the explanation's `##` headings. The file must contain only the JSON object. " +
		"Do not modify any other file, implement anything, or create STOP.md."
}

func quizRetryPrompt(cfg models.Config, validationErr error) string {
	return quizPrompt(cfg) + " Previous attempt failed validation: " + validationErr.Error() +
		". Regenerate the complete quiz and replace " + cfg.QuizFile + "."
}

// iterationPrompt reads the steps file and builds this iteration's instruction:
// the whole-plan audit when every box is checked (checkCompletion only ends
// the run once the audit's STOP.md exists), otherwise the next unchecked
// step. A missing next step then means the file had no parseable steps.
func (o *Orchestrator) iterationPrompt() (string, progressMessage, error) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return "", "", err
	}
	steps := ParseSteps(content)
	if AllStepsComplete(steps) {
		return auditPrompt, "auditing the whole plan", nil
	}
	for i, step := range steps {
		if !step.Completed {
			progress := executionProgress(i+1, step)
			return stepPrompt(step), progress, nil
		}
	}
	return noParsableStepsPrompt, "repairing step list", nil
}

// executionProgress identifies the step and caps the status below ten words.
func executionProgress(n int, step Step) progressMessage {
	words := strings.Fields(step.Text)
	if len(words) > 6 {
		words = words[:6]
	}
	if len(words) == 0 {
		return progressMessage(fmt.Sprintf("executing step %d", n))
	}
	return progressMessage(fmt.Sprintf(
		"executing step %d: %s", n, strings.Join(words, " ")))
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
	if step.Purpose != "" {
		b.WriteString(" Its purpose: ")
		b.WriteString(sentence(step.Purpose))
	}
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

// reportStart marks the execution phase start on the status page and publishes
// the step list's initial state.
func (o *Orchestrator) reportStart() {
	if o.status == nil {
		return
	}
	o.status.StartExecution()
	o.reportTaskSteps()
}

// reportTaskSteps publishes STEPS.md as parsed checkbox items so the status
// page shows step progress as boxes get checked.
func (o *Orchestrator) reportTaskSteps() {
	if o.status == nil {
		return
	}
	o.status.SetTaskSteps(taskSteps(o.parsedSteps()))
}

// reportFinish records execution's result, then generates the optional
// presentation-only explanation after a successful interactive run.
func (o *Orchestrator) reportFinish(ctx context.Context, outcome models.Outcome) {
	if o.status == nil {
		return
	}
	o.reportTaskSteps()
	if outcome != models.OutcomeStopped {
		reason, advice := execStopReasonAdvice(outcome, o.cfg)
		o.status.SetExecStopReason(reason, advice)
	}
	o.status.FinishExecution(outcome == models.OutcomeStopped)
	if outcome == models.OutcomeStopped {
		o.explainRun(ctx)
	}
}

// execStopReasonAdvice maps a failed run outcome to the plain-language reason
// and the remediation recommendation the status page shows. Unknown outcomes
// fall back to generic text so a failed run never renders a blank alert.
func execStopReasonAdvice(outcome models.Outcome, cfg models.Config) (string, string) {
	switch outcome {
	case models.OutcomeStalled:
		return fmt.Sprintf("No step was checked in %d consecutive iterations, so the run stopped instead of looping forever. This usually means the worker and the reviewers kept disagreeing about the same step: the worker marked it done and a reviewer unchecked it again.", cfg.MaxStalledIterations),
			"Read FIXES.md for the reviewers' objections and NOTES.md for the worker's reasoning, then break the tie yourself: apply the requested fix or adjust the step's `Done when:` criterion in STEPS.md. Then click Implement (or rerun determined) to resume from the unchecked steps."
	case models.OutcomeDroidFailed:
		return fmt.Sprintf("The AI tool failed %d consecutive invocations, so the run aborted.", cfg.MaxConsecutiveFailures),
			"Check the terminal output and the iteration logs for the tool's error — rate limit, authentication, or a crash. Fix the cause, then click Implement (or rerun determined) to retry."
	case models.OutcomeBudgetExceeded:
		return "The wall-clock budget expired before every step completed.",
			"Completed steps stay checked, so nothing is lost. Click Implement to resume the remaining steps, or rerun determined with a larger --budget."
	case models.OutcomeStepTimeout:
		return fmt.Sprintf("A single step ran for more than %s without completing, so the run stopped instead of grinding on it forever.", cfg.StepMaxRuntime),
			"Completed steps stay checked, so nothing is lost. Split the oversized step into smaller ones in STEPS.md, or allow more time per step with a larger --step-max-runtime, then click Implement (or rerun determined) to resume."
	case models.OutcomeInterrupted:
		return "The run was interrupted by a signal before it could finish.",
			"Click Implement (or rerun determined) to resume from the unchecked steps."
	case models.OutcomeMissingFiles:
		return "Execution started without the required PLAN.md / STEPS.md protocol files.",
			"Run `determined --plan \"<goal>\"` to produce them, then start execution again."
	}
	return "The run ended without completing the plan.",
		"Check the terminal output and the iteration logs, then click Implement (or rerun determined) to retry."
}

func (o *Orchestrator) explainRun(ctx context.Context) {
	o.status.StartExplanation()
	result := o.invoke(ctx, explainPrompt(o.cfg), "explaining the changes")
	if !result.succeeded {
		o.status.FinishExplanation(false)
		return
	}
	content, err := o.files.Read(o.cfg.ExplanationFile)
	if err != nil {
		o.status.FinishExplanation(false)
		return
	}
	o.status.SetExplanation(content)
	o.status.FinishExplanation(true)
	o.quizRun(ctx, content)
}

func (o *Orchestrator) quizRun(ctx context.Context, explanation string) {
	o.status.StartQuiz()
	content, ok := o.quizContent(ctx, quizPrompt(o.cfg))
	if !ok {
		o.status.FinishQuiz(false)
		return
	}
	questions, err := ParseQuiz(content, explanation)
	if err != nil {
		content, ok = o.quizContent(ctx, quizRetryPrompt(o.cfg, err))
		if ok {
			questions, err = ParseQuiz(content, explanation)
		}
	}
	if !ok || err != nil {
		o.status.FinishQuiz(false)
		return
	}
	o.status.SetQuiz(questions)
	o.status.FinishQuiz(true)
}

func (o *Orchestrator) quizContent(ctx context.Context, prompt string) (string, bool) {
	result := o.invoke(ctx, prompt, "writing the quiz")
	if !result.succeeded {
		return "", false
	}
	content, err := o.files.Read(o.cfg.QuizFile)
	return content, err == nil
}
