package services

import (
	"bytes"
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
	// replansUsed counts replan invocations spent on stalled steps; once it
	// reaches cfg.MaxReplans, hitting the stall cap ends the run for good.
	replansUsed int
	// rejections counts, per step (keyed by stepKey), how many times a checked
	// box was rejected by the check gate, verifier, or audit. From the second
	// rejection of the same step onward the failed attempt is stashed so the
	// retry starts from the last verified checkpoint.
	rejections map[string]int
	// stashes records, per step (keyed by stepKey), the immutable stash commit
	// hashes of its stashed failed attempts, so re-run prompts can point at the
	// latest one and a step that finally passes can drop them all.
	stashes map[string][]string
	// stashingEnabled reports whether failed-attempt stashing is available for
	// this run; initAttemptStashing decides once at startup.
	stashingEnabled bool
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
		runner:     runner,
		files:      files,
		clock:      clock,
		logs:       logs,
		terminal:   terminal,
		cfg:        cfg,
		rejections: map[string]int{},
		stashes:    map[string][]string{},
	}
}

// Run executes the loop and returns the terminal outcome.
func (o *Orchestrator) Run(ctx context.Context) models.Outcome {
	if !o.protocolFilesPresent() {
		return models.OutcomeMissingFiles
	}
	o.initAttemptStashing(ctx)
	deadline := o.deadline()
	for {
		if outcome, stop := o.preIteration(ctx, deadline); stop {
			return outcome
		}
		// The raw snapshot backs the tamper guard's byte-exact restore; its
		// parse is the progress baseline for the gate, verifier, checkpoint,
		// and stall counter. A read failure leaves both empty and skips the
		// guard; runOnce surfaces the failure itself.
		rawBefore, rawErr := o.files.Read(o.cfg.StepsFile)
		before := ParseSteps(rawBefore)
		intent, outcome, stop := o.runOnce(ctx)
		if stop {
			return outcome
		}
		if rawErr == nil {
			o.guardStepsFile(intent, rawBefore, before)
		}
		// The post-guard snapshot fixes what this iteration's work checked, so
		// rejection tracking can tell a gate/verifier rejection (checked here,
		// unchecked below) from a step that was never checked at all.
		afterWork := o.parsedSteps()
		if o.checkGatePassed(ctx, before) {
			if outcome, stop := o.verifyNewSteps(ctx, before); stop {
				return outcome
			}
			o.checkpointNewSteps(ctx, before)
		}
		o.recordRejections(ctx, intent, before, afterWork)
		if o.stalledOut(CompletedStepCount(before)) {
			outcome, stop, resumed := o.replanStuckStep(ctx)
			if stop {
				return outcome
			}
			if resumed {
				continue
			}
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

// replanStuckStep is the escalation between hitting the stall cap and giving
// up: a step that repeatedly fails verification is usually too large for one
// invocation, not impossible, so one invocation is spent replacing it with
// smaller steps instead of exiting stalled. It returns the loop's next move:
// stop with the outcome (the replan invocation was interrupted or exhausted
// the failure cap), resume the loop (the replan demonstrably changed the
// stuck step), or neither — the run stalls out as it would have without
// replanning. Each attempt consumes one of cfg.MaxReplans regardless of
// outcome, so a tool that replans badly cannot loop forever. With every box
// checked (audit ping-pong) or nothing parseable there is no single stuck
// step to split, and no replan is spent.
func (o *Orchestrator) replanStuckStep(ctx context.Context) (models.Outcome, bool, bool) {
	if o.replansUsed >= o.cfg.MaxReplans {
		return models.OutcomeStopped, false, false
	}
	rawBefore, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return models.OutcomeStopped, false, false
	}
	before := ParseSteps(rawBefore)
	i, step, ok := NextIncompleteStep(before)
	if !ok {
		return models.OutcomeStopped, false, false
	}
	o.replansUsed++
	fmt.Fprintf(o.terminal,
		"determined: step %d is stuck; replanning it into smaller steps (replan %d of %d)\n",
		i+1, o.replansUsed, o.cfg.MaxReplans)
	if outcome, stop := o.invoke(ctx, replanPrompt(i+1, step)); stop {
		return outcome, true, false
	}
	if !o.replanSucceeded(rawBefore, before, i) {
		return models.OutcomeStopped, false, false
	}
	o.stalled = 0
	return models.OutcomeStopped, false, true
}

// replanSucceeded judges the replan by its effect on the steps file, never by
// the tool's word: the stuck step must demonstrably differ (its text or
// criterion changed, or the step count did) while every previously completed
// step keeps its check. A damaged file — nothing parses, or finished work lost
// its check — is restored to the pre-replan snapshot so stalling out never
// leaves the file worse than the stall found it.
func (o *Orchestrator) replanSucceeded(rawBefore string, before []Step, target int) bool {
	rawAfter, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return false
	}
	after := ParseSteps(rawAfter)
	if len(after) == 0 || !completedStepsPreserved(before, after) {
		fmt.Fprintf(o.terminal,
			"determined: replan damaged %s; restoring it\n", o.cfg.StepsFile)
		if err := o.files.Write(o.cfg.StepsFile, rawBefore); err != nil {
			fmt.Fprintf(o.terminal, "determined: could not restore %s: %v\n", o.cfg.StepsFile, err)
		}
		return false
	}
	if len(after) == len(before) &&
		after[target].Text == before[target].Text &&
		after[target].DoneWhen == before[target].DoneWhen {
		fmt.Fprintf(o.terminal, "determined: replan left step %d unchanged\n", target+1)
		return false
	}
	fmt.Fprintf(o.terminal,
		"determined: step %d replanned; %s now has %d steps; resuming\n",
		target+1, o.cfg.StepsFile, len(after))
	return true
}

// completedStepsPreserved reports whether every step checked complete before
// the replan is still present and checked after it, matched by text and
// criterion: a replan may only reshape unfinished work, never lose finished
// work.
func completedStepsPreserved(before, after []Step) bool {
	remaining := make(map[Step]int)
	for _, s := range after {
		if s.Completed {
			remaining[s]++
		}
	}
	for _, s := range before {
		if !s.Completed {
			continue
		}
		if remaining[s] == 0 {
			return false
		}
		remaining[s]--
	}
	return true
}

// invocationKind classifies what an iteration's invocation was asked to do,
// so the tamper guard keys off intent rather than string-matching prompts.
type invocationKind int

const (
	// invocationStep works exactly one unchecked step; the only STEPS.md
	// edit it may make is checking that step's box.
	invocationStep invocationKind = iota
	// invocationAudit is the whole-plan audit; it legitimately unchecks steps.
	invocationAudit
	// invocationRewrite is the no-parseable-steps fallback; it legitimately
	// rewrites the whole file.
	invocationRewrite
)

// iterationIntent records what an iteration aimed its invocation at: the kind
// of invocation and, for single-step work, the zero-based target step index.
type iterationIntent struct {
	kind   invocationKind
	target int
}

// runOnce runs a single work invocation aimed at the next unchecked step. It
// returns what the invocation was aimed at and whether the loop should stop.
func (o *Orchestrator) runOnce(ctx context.Context) (iterationIntent, models.Outcome, bool) {
	prompt, intent, err := o.iterationPrompt()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.StepsFile, err)
		return intent, models.OutcomeDroidFailed, true
	}
	outcome, stop := o.invoke(ctx, prompt)
	return intent, outcome, stop
}

// guardStepsFile is the STEPS.md tamper guard. It applies only to single-step
// work invocations (the audit and the fallback rewrite legitimately uncheck
// or rewrite the file, and the replan escalation — which never routes through
// runOnce — judges its own rewrite in replanSucceeded): such an invocation
// may change the parsed steps in exactly one way, checking its own target
// step's box. Anything else — reworded text, an
// altered Done-when criterion, steps added, deleted, or reordered, another
// box flipped — would make the verifier judge against text the worker chose
// for itself, so the file is restored from the pre-iteration snapshot,
// preserving only the target step's legitimate check. Running before the
// check gate means a surviving check is still gated, verified, and
// checkpointed normally, and a full revert reads as no progress to the stall
// counter. An unreadable file is left alone; the loop surfaces read failures
// elsewhere.
func (o *Orchestrator) guardStepsFile(intent iterationIntent, rawBefore string, before []Step) {
	if intent.kind != invocationStep {
		return
	}
	rawAfter, err := o.files.Read(o.cfg.StepsFile)
	if err != nil || rawAfter == rawBefore {
		return
	}
	after := ParseSteps(rawAfter)
	violation := tamperViolation(before, after, intent.target)
	if violation == "" {
		return
	}
	fmt.Fprintf(o.terminal,
		"determined: warning: STEPS.md was altered beyond checking step %d (%s); restoring it\n",
		intent.target+1, violation)
	restored := rawBefore
	if intent.target < len(after) && after[intent.target].Completed {
		restored = CheckSteps(rawBefore, []int{intent.target})
	}
	if err := o.files.Write(o.cfg.StepsFile, restored); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not restore %s: %v\n", o.cfg.StepsFile, err)
	}
}

// tamperViolation returns a description of the first illegitimate difference
// between the parsed steps and their pre-iteration snapshot, or "" when the
// only change (if any) is the target step's box flipping `[ ]` to `[x]`.
// Text and Done-when must match exactly; prose outside the checkbox items is
// not judged, since only the parsed steps steer the loop.
func tamperViolation(before, after []Step, target int) string {
	if len(after) != len(before) {
		return fmt.Sprintf("step count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		switch {
		case after[i].Text != before[i].Text:
			return fmt.Sprintf("step %d's text changed", i+1)
		case after[i].DoneWhen != before[i].DoneWhen:
			return fmt.Sprintf("step %d's Done-when criterion changed", i+1)
		case after[i].Completed == before[i].Completed:
		case i != target:
			return fmt.Sprintf("step %d's checkbox changed", i+1)
		case before[i].Completed:
			return fmt.Sprintf("step %d was unchecked", i+1)
		}
	}
	return ""
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

// checkCmdTimeout bounds the check command so a hung test suite cannot block
// the loop indefinitely; a timeout surfaces as an ordinary check failure.
const checkCmdTimeout = 10 * time.Minute

// checkOutputTailLimit caps how much check-command output is recorded per
// FIXES.md entry, so a chatty test suite cannot bloat the file.
const checkOutputTailLimit = 4000

// checkGatePassed runs the deterministic check command when this iteration
// newly checked at least one step, and reports whether the iteration may
// proceed to the AI verify pass and git checkpoint. The command runs once per
// iteration, not per step: it validates the whole tree, so one success covers
// every newly checked step. A failure is a verdict on the work, not a tool
// failure — it never counts toward the consecutive-failure cap. The failing
// steps are unchecked again and the command's output tail recorded in
// FIXES.md, so the loop simply re-runs them and the stall counter sees the
// round as no progress; the gate itself never ends the run.
func (o *Orchestrator) checkGatePassed(ctx context.Context, before []Step) bool {
	if o.cfg.CheckCmd == "" {
		return true
	}
	newly := newlyCheckedIndices(o.parsedSteps(), before)
	if len(newly) == 0 {
		return true
	}
	fmt.Fprintf(o.terminal, "determined: running check command: %s\n", o.cfg.CheckCmd)
	var output bytes.Buffer
	runCtx, cancel := context.WithTimeout(ctx, checkCmdTimeout)
	err := o.runner.Run(runCtx,
		models.Invocation{Binary: "sh", Args: []string{"-c", o.cfg.CheckCmd}},
		io.MultiWriter(o.terminal, &output))
	cancel()
	if err == nil {
		return true
	}
	o.rejectCheckedSteps(newly, output.String(), err)
	return false
}

// newlyCheckedIndices returns the zero-based indices of the steps checked in
// steps but not in the before snapshot.
func newlyCheckedIndices(steps, before []Step) []int {
	var newly []int
	for i, step := range steps {
		if step.Completed && !(i < len(before) && before[i].Completed) {
			newly = append(newly, i)
		}
	}
	return newly
}

// rejectCheckedSteps unchecks the given steps in the steps file and records
// the check command's failure under a `## Step N` FIXES.md heading per step,
// so the re-run worker sees exactly what failed. File errors are noted and
// ignored: the gate delivers verdicts, it never ends the run.
func (o *Orchestrator) rejectCheckedSteps(indices []int, output string, cause error) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err == nil {
		err = o.files.Write(o.cfg.StepsFile, UncheckSteps(content, indices))
	}
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not uncheck steps after failed check: %v\n", err)
	}
	var entry strings.Builder
	for _, i := range indices {
		fmt.Fprintf(&entry,
			"## Step %d\n\nCheck command `%s` failed (%v). Output tail:\n\n```\n%s\n```\n\n",
			i+1, o.cfg.CheckCmd, cause, tail(output, checkOutputTailLimit))
		fmt.Fprintf(o.terminal,
			"determined: check command failed; unchecked step %d and recorded the output in FIXES.md\n", i+1)
	}
	if err := o.files.Append("FIXES.md", entry.String()); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the check failure in FIXES.md: %v\n", err)
	}
}

// tail returns the last limit bytes of s.
func tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
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
		if o.gitCommit(ctx, i+1, step) {
			o.dropStashes(ctx, i+1, step)
		}
	}
}

// gitCheckpointTimeout bounds each git checkpoint command so a hung git
// operation cannot block the run: checkpoints are a convenience and must never
// stall the loop indefinitely.
const gitCheckpointTimeout = 2 * time.Minute

// gitCommit stages everything and commits it as the checkpoint for one step,
// reporting whether the commit landed.
func (o *Orchestrator) gitCommit(ctx context.Context, n int, step Step) bool {
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
			return false
		}
	}
	fmt.Fprintf(o.terminal, "determined: git checkpoint committed for step %d\n", n)
	return true
}

// stashAfterRejections is the per-step rejection count at which a rejected
// attempt starts being stashed. The first rejection retries in place — the
// attempt may be one small fix away — but a second rejection of the same step
// suggests the attempt's foundation is wrong, so later retries start clean
// from the last verified checkpoint with the failed work preserved as a stash.
const stashAfterRejections = 2

// stepKey identifies a step across iterations by its text and criterion — the
// same identity the tamper guard holds fixed — so rejection counts and stashes
// survive the index shifts a replan of other steps can cause.
func stepKey(s Step) string { return s.Text + "\x00" + s.DoneWhen }

// git runs one git command bounded by gitCheckpointTimeout, capturing its
// output instead of streaming it: the stash bookkeeping commands are plumbing
// whose output the user does not need live.
func (o *Orchestrator) git(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	runCtx, cancel := context.WithTimeout(ctx, gitCheckpointTimeout)
	defer cancel()
	err := o.runner.Run(runCtx, models.Invocation{Binary: "git", Args: args}, &out)
	return out.String(), err
}

// protectedPaths lists the files an attempt stash must never touch: STEPS.md
// and FIXES.md hold the rejection the retry needs to see, NOTES.md is
// cross-iteration memory, the planning files may sit uncommitted from a
// --plan session in the same directory, and the log directory holds the run's
// own iteration logs.
func (o *Orchestrator) protectedPaths() []string {
	paths := []string{
		o.cfg.PlanFile, o.cfg.StepsFile, o.cfg.StopFile,
		"NOTES.md", "FIXES.md", "GOAL.md", "QUESTIONS.md", "ANSWERS.md", "OVERSIZED.md",
	}
	if o.cfg.LogDir != "" {
		paths = append(paths, o.cfg.LogDir)
	}
	return paths
}

// stashExcludes returns the pathspecs that keep the protected files out of
// every attempt stash and out of the startup cleanliness check.
func (o *Orchestrator) stashExcludes() []string {
	excludes := make([]string, 0, len(o.protectedPaths()))
	for _, p := range o.protectedPaths() {
		excludes = append(excludes, ":(exclude)"+p)
	}
	return excludes
}

// initAttemptStashing decides once, at startup, whether rejected attempts may
// be stashed this run. Stashing assumes every uncommitted change is the run's
// own work, which holds only when checkpointing commits verified work out of
// the way, the directory is a git repository, and the tree starts clean apart
// from the protected files (which stashes never touch). A tree carrying the
// user's own uncommitted changes disables stashing for the whole run —
// stashing pre-existing work would be the one destructive failure mode here —
// and the run degrades to retrying in place exactly as without the feature.
func (o *Orchestrator) initAttemptStashing(ctx context.Context) {
	if !o.cfg.StashAttempts || !o.cfg.GitCheckpoint || !o.files.Exists(".git") {
		return
	}
	args := append([]string{"status", "--porcelain", "--untracked-files=all", "--", "."},
		o.stashExcludes()...)
	out, err := o.git(ctx, args...)
	if err != nil {
		fmt.Fprintf(o.terminal,
			"determined: could not inspect the working tree (%v); failed-attempt stashing disabled\n", err)
		return
	}
	if strings.TrimSpace(out) != "" {
		fmt.Fprintln(o.terminal,
			"determined: working tree has changes that predate this run; failed-attempt stashing disabled so they are never stashed")
		return
	}
	o.stashingEnabled = true
}

// recordRejections updates the per-step rejection counts once the gate,
// verifier, and audit have delivered their verdicts, and stashes the failed
// attempt when the same step reaches stashAfterRejections. A rejection is a
// step this iteration's work checked that lost the check to the gate or the
// verifier; steps the audit reopened count toward the same totals (they were
// finished work that turned out wrong) but trigger no stash, since their work
// was committed at checkpoint time and the tree holds nothing to stash.
func (o *Orchestrator) recordRejections(ctx context.Context, intent iterationIntent, before, afterWork []Step) {
	final := o.parsedSteps()
	uncheckedNow := func(i int) bool { return i < len(final) && !final[i].Completed }
	stashTarget := -1
	for _, i := range newlyCheckedIndices(afterWork, before) {
		if uncheckedNow(i) {
			o.rejections[stepKey(afterWork[i])]++
			if stashTarget == -1 {
				stashTarget = i
			}
		}
	}
	if intent.kind == invocationAudit {
		for i, s := range before {
			if s.Completed && uncheckedNow(i) {
				o.rejections[stepKey(s)]++
			}
		}
	}
	if stashTarget == -1 {
		return
	}
	if step := afterWork[stashTarget]; o.rejections[stepKey(step)] >= stashAfterRejections {
		o.stashFailedAttempt(ctx, stashTarget+1, step)
	}
}

// stashFailedAttempt stashes the working tree's uncommitted changes — the
// repeatedly rejected attempt at step n — so the retry starts from the last
// verified checkpoint instead of building on a foundation that keeps failing.
// The attempt is preserved as evidence, not discarded: FIXES.md gets a
// mechanical entry recording the stash's immutable hash (stash@{N} positions
// rot as new stashes push in) and what it changed, and the re-run prompt
// points the worker at it. The protected files are excluded, so the rejection
// record itself survives the stash. Every failure here is noted and ignored:
// stashing is a convenience and must never end the run.
func (o *Orchestrator) stashFailedAttempt(ctx context.Context, n int, step Step) {
	if !o.stashingEnabled {
		return
	}
	key := stepKey(step)
	refBefore, _ := o.git(ctx, "rev-parse", "-q", "--verify", "refs/stash")
	message := fmt.Sprintf("determined: step %d rejected attempt %d: %s",
		n, o.rejections[key], strings.TrimSpace(step.Text))
	args := append([]string{"stash", "push", "--include-untracked", "-m", message, "--", "."},
		o.stashExcludes()...)
	if out, err := o.git(ctx, args...); err != nil {
		fmt.Fprintf(o.terminal,
			"determined: could not stash the rejected attempt at step %d: %v\n%s", n, err, out)
		return
	}
	refAfter, err := o.git(ctx, "rev-parse", "refs/stash")
	hash := strings.TrimSpace(refAfter)
	if err != nil || hash == "" || hash == strings.TrimSpace(refBefore) {
		return // the rejected attempt left no changes to stash
	}
	o.stashes[key] = append(o.stashes[key], hash)
	stat, _ := o.git(ctx, "stash", "show", "--include-untracked", "--stat", hash)
	if err := o.files.Append("FIXES.md", stashEntry(n, hash, stat)); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the stash in FIXES.md: %v\n", err)
	}
	fmt.Fprintf(o.terminal,
		"determined: stashed the rejected attempt at step %d as %s; the retry starts from the last verified checkpoint\n",
		n, hash)
}

// stashEntry builds the mechanical FIXES.md record for one stashed attempt.
// It rides under the same `## Step N` heading convention as the gate and
// verifier entries, which record just above *why* the attempt was rejected.
func stashEntry(n int, hash, stat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Step %d\n\nThe rejected attempt above was stashed as `%s` and the "+
		"working tree reset to the last verified checkpoint. Inspect it with "+
		"`git stash show -p %s` (its new files, if any, with `git show %s^3`). You may "+
		"reuse ideas from it, but do not apply it wholesale — it was rejected for the "+
		"reason recorded above.\n", n, hash, hash, hash)
	if s := strings.TrimSpace(stat); s != "" {
		fmt.Fprintf(&b, "\nIt changed:\n\n```\n%s\n```\n", s)
	}
	b.WriteString("\n")
	return b.String()
}

// stashNote extends a re-run step prompt when earlier attempts at the step
// were stashed: the worker starts from a clean checkpoint and should treat
// the stash as evidence, never as a patch to reapply.
func stashNote(hash string) string {
	return fmt.Sprintf(" A previous attempt at this step was rejected and stashed as %s; "+
		"the working tree was reset to the last verified checkpoint, so implement the step "+
		"fresh. You may inspect the stash with `git stash show -p %s` and reuse ideas from "+
		"it, but do not apply it wholesale — FIXES.md records why it was rejected.", hash, hash)
}

// dropStashes deletes the failed-attempt stashes recorded for a step once the
// step finally passes verification and is checkpointed: from then on they are
// dead weight in the user's stash stack. The recorded hashes are resolved to
// their current stash@{i} positions and dropped highest-first so the lower
// positions stay valid. Best-effort: a failed git command leaves the stashes
// in place.
func (o *Orchestrator) dropStashes(ctx context.Context, n int, step Step) {
	key := stepKey(step)
	hashes := o.stashes[key]
	if len(hashes) == 0 {
		return
	}
	out, err := o.git(ctx, "stash", "list", "--format=%H")
	if err != nil {
		return
	}
	owned := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		owned[h] = true
	}
	list := strings.Fields(out)
	dropped := 0
	for i := len(list) - 1; i >= 0; i-- {
		if !owned[list[i]] {
			continue
		}
		if _, err := o.git(ctx, "stash", "drop", fmt.Sprintf("stash@{%d}", i)); err == nil {
			dropped++
		}
	}
	delete(o.stashes, key)
	if dropped > 0 {
		fmt.Fprintf(o.terminal,
			"determined: dropped %d stashed attempt(s) at step %d now that it passed\n", dropped, n)
	}
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
// step. A missing next step then means the file had no parseable steps. The
// returned intent tells the tamper guard which kind of iteration ran and, for
// single-step work, which step it targeted.
func (o *Orchestrator) iterationPrompt() (string, iterationIntent, error) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return "", iterationIntent{}, err
	}
	steps := ParseSteps(content)
	if AllStepsComplete(steps) {
		fmt.Fprintln(o.terminal,
			"determined: all steps checked; auditing the whole plan before declaring success")
		return auditPrompt, iterationIntent{kind: invocationAudit}, nil
	}
	i, step, ok := NextIncompleteStep(steps)
	if !ok {
		return noParsableStepsPrompt, iterationIntent{kind: invocationRewrite}, nil
	}
	prompt := stepPrompt(i+1, step)
	if hashes := o.stashes[stepKey(step)]; len(hashes) > 0 {
		prompt += stashNote(hashes[len(hashes)-1])
	}
	return prompt, iterationIntent{kind: invocationStep, target: i}, nil
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

// replanPrompt asks the tool to split one stuck step into smaller ones. It is
// a planning move, not a work move: the prompt forbids implementing, checking
// boxes, and STOP.md outright, and points at FIXES.md because the rejection
// history is the best evidence of where the step is too big. The result is
// judged by replanSucceeded, so the prompt promises nothing about acceptance.
func replanPrompt(n int, step Step) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	fmt.Fprintf(&b, "Step %d has repeatedly failed verification and is likely too large "+
		"to implement in one invocation: ", n)
	b.WriteString(sentence(step.Text))
	if step.DoneWhen != "" {
		b.WriteString(" Its acceptance criterion: ")
		b.WriteString(sentence(step.DoneWhen))
	}
	fmt.Fprintf(&b, " Read PLAN.md, plus FIXES.md and NOTES.md if they exist, to see what "+
		"has gone wrong so far. Replace step %d in STEPS.md with 2-4 smaller `- [ ]` "+
		"checkbox steps, each ending with an indented `Done when:` line stating a "+
		"checkable acceptance condition, ordered so completing them all completes the "+
		"original step. Keep every other step exactly as it is, in order. Do not check "+
		"any box, do not implement anything, and do not create STOP.md.", n)
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
