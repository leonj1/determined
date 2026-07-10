package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
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
// created STOP.md carrying an evidence block whose commands, re-run by the
// orchestrator itself, all pass: a STOP.md created while unchecked steps
// remain is deleted and the loop continues.
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
	// planChangesUsed counts the PROPOSALS.md plan changes applied to the
	// steps file; once it reaches cfg.MaxPlanChanges, further proposals are
	// rejected with "plan-change budget exhausted".
	planChangesUsed int
	// rejections records, per step (keyed by stepKey), one short reason string
	// per rejection of a checked box by the check gate, done-when check,
	// verifier, or audit, in order — a step's rejection count is its slice's
	// length. From the second
	// rejection of the same step onward the failed attempt is stashed so the
	// retry starts from the last verified checkpoint; the reasons feed the
	// STALLED.md handoff, while FIXES.md keeps the full entries.
	rejections map[string][]string
	// stashes records, per step (keyed by stepKey), its stashed failed
	// attempts, so re-run prompts can point at the latest one, a step that
	// finally passes can drop them all, and a stalled run can list them.
	stashes map[string][]stashRecord
	// stashingEnabled reports whether failed-attempt stashing is available for
	// this run; initAttemptStashing decides once at startup.
	stashingEnabled bool
	// stopValidated remembers that the current STOP.md's evidence block was
	// parsed and every listed command re-run successfully, so acceptance never
	// runs the commands twice. A rejection deletes the file instead, so the
	// flag never describes a stale STOP.md.
	stopValidated bool
	// auditLackedEvidence notes that the last STOP.md was rejected for missing
	// the required evidence block, so the next audit prompt carries a
	// corrective note; cleared as soon as a STOP.md with a block appears.
	auditLackedEvidence bool
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
		rejections: map[string][]string{},
		stashes:    map[string][]stashRecord{},
	}
}

// Run executes the loop and returns the terminal outcome. Every termination —
// success, stall, tool-failure abort, exhausted budget, interruption, even
// missing protocol files — also writes the machine-readable run-report.json
// summary, built from state the loop already tracks; a stalled termination
// additionally writes the human-readable STALLED.md handoff from the same
// state. Once the reports are written, the --notify-cmd exit hook runs.
// Reporting and notification are observers and never change the outcome.
func (o *Orchestrator) Run(ctx context.Context) models.Outcome {
	start := o.clock.Now()
	o.removeStaleReports()
	outcome := o.run(ctx)
	report := ""
	if o.writeStalledReport(outcome, start) {
		report = stalledReportFile
	}
	o.writeRunReport(outcome, start, report)
	o.runNotifyCmd(outcome, start)
	return outcome
}

// run is the execute loop proper.
func (o *Orchestrator) run(ctx context.Context) models.Outcome {
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
		gatePassed := o.checkGatePassed(ctx, before)
		var doneWhenFailed map[int]string
		if gatePassed {
			failed, outcome, stop := o.verifyNewSteps(ctx, before)
			if stop {
				return outcome
			}
			doneWhenFailed = failed
			o.checkpointNewSteps(ctx, before)
		}
		o.recordRejections(ctx, intent, before, afterWork, gatePassed, doneWhenFailed)
		o.processProposals()
		o.validateStopFile(ctx)
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
	if outcome, stop := o.checkCompletion(ctx); stop {
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
// STOP.md with validated evidence. The validation normally happened at the
// bottom of the audit's own iteration (stopEvidenceValidated is then a cached
// yes), but a run that starts with every box already checked and a STOP.md in
// place validates here — success never bypasses the evidence check. All boxes
// checked without STOP.md means the audit still has to run (the next
// iteration is the audit), and a STOP.md that appears while unchecked steps
// remain is deleted so the loop keeps going.
func (o *Orchestrator) checkCompletion(ctx context.Context) (models.Outcome, bool) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return models.OutcomeStopped, false // runOnce reports the read failure
	}
	steps := ParseSteps(content)
	switch {
	case AllStepsComplete(steps):
		if o.files.Exists(o.cfg.StopFile) && o.stopEvidenceValidated(ctx) {
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

// auditRejectionKey keys the audit-level STOP.md rejections in the rejections
// map. A plain word can never collide with a real step's key, which always
// embeds a NUL separator, and reportRejections' text fallback renders it as
// "audit" in run-report.json.
const auditRejectionKey = "audit"

// parseEvidenceCommands extracts the commands from a STOP.md evidence block:
// a fenced code block opened by a line reading exactly ```evidence and closed
// by ```, one command per non-blank line. The commands carry no exit codes —
// the orchestrator re-runs them itself, so the lines are just what to run.
// The rule is deliberately mechanical: the first such block wins, and a
// missing, empty, or never-closed block yields no commands, which rejects
// the STOP.md.
func parseEvidenceCommands(content string) []string {
	var cmds []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inBlock:
			inBlock = trimmed == "```evidence"
		case trimmed == "```":
			return cmds
		case trimmed != "":
			cmds = append(cmds, trimmed)
		}
	}
	return nil // no ```evidence block, or one never closed
}

// validateStopFile applies the evidence validation at the bottom of an
// iteration, before the stall counter reads the file state: whenever a
// STOP.md sits alongside a fully checked steps file, its evidence must hold
// up. A rejection deletes STOP.md, so the round reads as no progress to
// stalledOut — bounding audit ping-pong exactly like a verifier rejection —
// while an accepted STOP.md keeps runComplete true and the next
// checkCompletion ends the run without re-running the commands.
func (o *Orchestrator) validateStopFile(ctx context.Context) {
	if !AllStepsComplete(o.parsedSteps()) || !o.files.Exists(o.cfg.StopFile) {
		return
	}
	o.stopEvidenceValidated(ctx)
}

// stopEvidenceValidated reports whether the current STOP.md may be trusted.
// A bare STOP.md would make the final audit a single trusted invocation, so
// approval must carry a ```evidence fenced block naming the commands the
// audit ran, and every listed command — re-run by the orchestrator itself via
// `sh -c`, streamed live and bounded per command by the same 10-minute
// timeout as the other gates — must exit 0. Both rejections delete the file
// and send the loop back to the audit; each is a verdict on the audit's
// output, never a tool failure, so the consecutive-failure cap is untouched.
// A validated STOP.md is remembered so acceptance never re-runs the commands.
func (o *Orchestrator) stopEvidenceValidated(ctx context.Context) bool {
	if o.stopValidated {
		return true
	}
	content, err := o.files.Read(o.cfg.StopFile)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.StopFile, err)
		return false
	}
	cmds := parseEvidenceCommands(content)
	if len(cmds) == 0 {
		o.rejectStopMissingEvidence()
		return false
	}
	o.auditLackedEvidence = false
	for _, cmd := range cmds {
		fmt.Fprintf(o.terminal, "determined: re-running audit evidence command: %s\n", cmd)
		var output bytes.Buffer
		runCtx, cancel := context.WithTimeout(ctx, checkCmdTimeout)
		err := o.runner.Run(runCtx,
			models.Invocation{Binary: "sh", Args: []string{"-c", cmd}},
			io.MultiWriter(o.terminal, &output))
		cancel()
		if err != nil {
			o.rejectStopFailedEvidence(cmd, output.String(), err)
			return false
		}
	}
	o.stopValidated = true
	return true
}

// rejectStopMissingEvidence rejects a STOP.md that carries no evidence block:
// the file is deleted with a warning and the next audit prompt gains a
// corrective note. Deleting the file makes the round read as no progress to
// the stall counter, which bounds audit ping-pong; a failed delete is noted
// and ignored, matching deletePrematureStop.
func (o *Orchestrator) rejectStopMissingEvidence() {
	o.auditLackedEvidence = true
	fmt.Fprintf(o.terminal,
		"determined: warning: %s lacks the required ```evidence block; deleting it and re-running the audit\n",
		o.cfg.StopFile)
	if err := o.files.Remove(o.cfg.StopFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not delete %s: %v\n", o.cfg.StopFile, err)
	}
}

// rejectStopFailedEvidence rejects a STOP.md whose evidence did not hold up:
// the failing command's output tail is recorded under a `## Audit` FIXES.md
// heading mirroring the check-gate entry, the rejection reason joins the
// accounting so STALLED.md and run-report.json see it, and STOP.md is deleted
// so the loop resumes on the audit — which should uncheck whatever broke or
// produce honest evidence, never fix anything itself. File errors are noted
// and ignored: like the other gates, this delivers verdicts, it never ends
// the run.
func (o *Orchestrator) rejectStopFailedEvidence(cmd, output string, cause error) {
	entry := fmt.Sprintf(
		"## Audit\n\n%s evidence command `%s` failed (%v). Output tail:\n\n```\n%s\n```\n\n",
		o.cfg.StopFile, cmd, cause, tail(output, checkOutputTailLimit))
	if err := o.files.Append("FIXES.md", entry); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the evidence failure in FIXES.md: %v\n", err)
	}
	o.rejections[auditRejectionKey] = append(o.rejections[auditRejectionKey],
		"audit evidence failed: "+cmd)
	if err := o.files.Remove(o.cfg.StopFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not delete %s: %v\n", o.cfg.StopFile, err)
	}
	fmt.Fprintf(o.terminal,
		"determined: audit evidence command failed; deleted %s and recorded the output in FIXES.md\n",
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

// proposalsFile is the protocol file through which a worker may legitimately
// propose mid-run plan changes: the tamper guard reverts any direct steps-file
// edit beyond the worker's own box, so discoveries that planning missed — an
// obsolete step, a missing one, a wrong criterion — are appended here as
// structured `## Proposal` sections for the orchestrator to judge between
// iterations.
const proposalsFile = "PROPOSALS.md"

// proposal is one `## Proposal` section parsed from PROPOSALS.md: the
// requested action ("add-after 4", "remove 2", "reword 3"), the proposed step
// line where the action needs one, and the worker's stated reason.
type proposal struct {
	action string
	step   string
	reason string
}

// parseProposals extracts the `## Proposal` sections from PROPOSALS.md
// content: each section starts at a `## Proposal` heading and carries
// `action:`, `step:`, and `reason:` lines. Lines outside a section and
// unrecognized lines inside one are ignored, so surrounding chatter parses
// cleanly, exactly as ParseSteps treats the steps file.
func parseProposals(content string) []proposal {
	var proposals []proposal
	var cur *proposal
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "## Proposal" {
			proposals = append(proposals, proposal{})
			cur = &proposals[len(proposals)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case hasFoldPrefix(trimmed, "action:"):
			cur.action = strings.TrimSpace(trimmed[len("action:"):])
		case hasFoldPrefix(trimmed, "step:"):
			cur.step = strings.TrimSpace(trimmed[len("step:"):])
		case hasFoldPrefix(trimmed, "reason:"):
			cur.reason = strings.TrimSpace(trimmed[len("reason:"):])
		}
	}
	return proposals
}

// parseProposalAction splits an action like "add-after 4" into its verb and
// index. Anything but exactly one verb and one integer is unrecognized.
func parseProposalAction(action string) (string, int, bool) {
	fields := strings.Fields(action)
	if len(fields) != 2 {
		return "", 0, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false
	}
	return fields[0], n, true
}

// parseProposalStep validates the step line an add-after or reword proposal
// carries: a single line that parses as an unchecked `- [ ]` checkbox item
// whose text carries an inline `Done when:` criterion, both parts non-empty —
// the same shape ParseSteps reads back once renderSteps splits the criterion
// onto its indented line. It returns the parsed step, or the problem that
// rejects the proposal.
func parseProposalStep(line string) (Step, string) {
	item, ok := checkboxItem(line)
	if !ok {
		return Step{}, fmt.Sprintf("step line %q is not a `- [ ]` checkbox item", line)
	}
	if item.Completed {
		return Step{}, "the proposed step is checked; proposals may only introduce unchecked steps"
	}
	i := foldIndex(item.Text, doneWhenPrefix)
	if i < 0 {
		return Step{}, "the proposed step has no `Done when:` criterion"
	}
	text := strings.TrimSpace(item.Text[:i])
	doneWhen := strings.TrimSpace(item.Text[i+len(doneWhenPrefix):])
	if text == "" {
		return Step{}, "the proposed step has no text before its `Done when:` criterion"
	}
	if doneWhen == "" {
		return Step{}, "the proposed step's `Done when:` criterion is empty"
	}
	return Step{Text: text, DoneWhen: doneWhen}, ""
}

// unrecognizedProposalAction is the rejection for an action the mechanical
// judge does not know, naming the supported forms so the worker can correct
// the proposal instead of guessing.
func unrecognizedProposalAction(action string) string {
	return fmt.Sprintf("unrecognized action %q (supported: `add-after N`, `remove N`, `reword N`)", action)
}

// judgeProposal validates one proposal against the current parsed steps and,
// when valid, returns the steps with it applied plus a short description of
// the change; otherwise it returns only the rejection reason. The rules are
// deliberately mechanical: `add-after N` (N in 0..len, 0 inserts at the top)
// inserts a well-formed unchecked step, and `remove N` / `reword N` (1..len)
// touch only an unchecked step N — completed work is never removed or
// reworded. No action can alter any other step's text, criterion, or checked
// state, because the mutation is built from the parsed model itself and only
// shifts positions around the target.
func judgeProposal(steps []Step, p proposal) (after []Step, change, reason string) {
	verb, n, ok := parseProposalAction(p.action)
	if !ok {
		return nil, "", unrecognizedProposalAction(p.action)
	}
	switch verb {
	case "add-after":
		if n < 0 || n > len(steps) {
			return nil, "", fmt.Sprintf("add-after %d is out of range for a %d-step plan", n, len(steps))
		}
		step, problem := parseProposalStep(p.step)
		if problem != "" {
			return nil, "", problem
		}
		after = append(after, steps[:n]...)
		after = append(after, step)
		after = append(after, steps[n:]...)
		return after, fmt.Sprintf("inserted %q as step %d", step.Text, n+1), ""
	case "remove":
		if n < 1 || n > len(steps) {
			return nil, "", fmt.Sprintf("remove %d is out of range for a %d-step plan", n, len(steps))
		}
		if steps[n-1].Completed {
			return nil, "", fmt.Sprintf("step %d is checked; completed work is never removed", n)
		}
		after = append(after, steps[:n-1]...)
		after = append(after, steps[n:]...)
		return after, fmt.Sprintf("removed step %d (%q)", n, steps[n-1].Text), ""
	case "reword":
		if n < 1 || n > len(steps) {
			return nil, "", fmt.Sprintf("reword %d is out of range for a %d-step plan", n, len(steps))
		}
		if steps[n-1].Completed {
			return nil, "", fmt.Sprintf("step %d is checked; completed work is never reworded", n)
		}
		step, problem := parseProposalStep(p.step)
		if problem != "" {
			return nil, "", problem
		}
		after = append(after, steps...)
		after[n-1] = step
		return after, fmt.Sprintf("replaced step %d (%q) with %q", n, steps[n-1].Text, step.Text), ""
	}
	return nil, "", unrecognizedProposalAction(p.action)
}

// processProposals is the bounded, legitimate channel for mid-run plan
// changes. Execution discovers things planning missed, and the tamper guard
// (rightly) reverts direct steps-file edits — so workers append `## Proposal`
// sections to PROPOSALS.md instead, and each is judged mechanically here,
// between iterations: recognized action, valid index against the current
// parsed steps, a well-formed unchecked step line where one is required, and
// no effect on any checked step or on any other step's content (judged
// against the parsed model, exactly as replan validation judges by effect).
// A valid proposal is applied by rewriting the steps file from the parsed
// model — so shifting positions can never alter another step — announced on
// the terminal, and recorded in NOTES.md; an invalid one is rejected with the
// reason recorded in FIXES.md, never applied. cfg.MaxPlanChanges bounds the
// applied proposals per run, and the file is removed once processed so no
// proposal is judged twice. Applied changes are plan activity, not step
// progress: they leave the completed-step count untouched, so the stall
// counter (updated after this runs) never resets for them and a worker cannot
// dodge stall detection by proposing forever.
func (o *Orchestrator) processProposals() {
	if o.cfg.MaxPlanChanges <= 0 || !o.files.Exists(proposalsFile) {
		return
	}
	content, err := o.files.Read(proposalsFile)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", proposalsFile, err)
		return
	}
	proposals := parseProposals(content)
	if len(proposals) == 0 {
		return
	}
	raw, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		// Leave the proposals for the next iteration; the loop surfaces the
		// read failure itself.
		return
	}
	steps := ParseSteps(raw)
	applied := 0
	for _, p := range proposals {
		if o.planChangesUsed >= o.cfg.MaxPlanChanges {
			o.rejectProposal(p, "plan-change budget exhausted")
			continue
		}
		after, change, reason := judgeProposal(steps, p)
		if reason == "" && !completedStepsPreserved(steps, after) {
			// Unreachable by construction — the per-action rules forbid
			// touching a checked step — but judged by effect anyway, exactly
			// like a replan.
			reason = "applying it would lose a completed step"
		}
		if reason != "" {
			o.rejectProposal(p, reason)
			continue
		}
		steps = after
		o.planChangesUsed++
		applied++
		o.recordPlanChange(p, change)
	}
	if applied > 0 {
		if err := o.files.Write(o.cfg.StepsFile, renderSteps(steps)); err != nil {
			fmt.Fprintf(o.terminal, "determined: could not write the plan changes to %s: %v\n",
				o.cfg.StepsFile, err)
		}
	}
	if err := o.files.Remove(proposalsFile); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not remove %s: %v\n", proposalsFile, err)
	}
}

// recordPlanChange announces an applied proposal on the terminal and appends
// it to NOTES.md. NOTES.md over FIXES.md deliberately: an applied change is
// knowledge later invocations need — the plan they are working on changed —
// which is exactly what the cross-iteration memory file carries and where
// every step prompt already tells the worker to look first, while FIXES.md
// records rejected and reopened work, which an accepted proposal is not. A
// file error is noted and ignored: recording is an observer, never a gate.
func (o *Orchestrator) recordPlanChange(p proposal, change string) {
	reason := p.reason
	if reason == "" {
		reason = "no reason given"
	}
	fmt.Fprintf(o.terminal, "determined: applied plan change `%s` (%d of %d): %s; reason: %s\n",
		p.action, o.planChangesUsed, o.cfg.MaxPlanChanges, change, reason)
	entry := fmt.Sprintf("## Plan change\n\nApplied a worker proposal to %s: `%s` — %s. Reason: %s\n\n",
		o.cfg.StepsFile, p.action, change, reason)
	if err := o.files.Append("NOTES.md", entry); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the plan change in NOTES.md: %v\n", err)
	}
}

// rejectProposal declines a proposal without applying it, appending which
// proposal failed and why to FIXES.md — the file the next worker is told to
// read first — so the worker can correct or drop the proposal instead of
// re-submitting it blind. A file error is noted and ignored.
func (o *Orchestrator) rejectProposal(p proposal, reason string) {
	fmt.Fprintf(o.terminal, "determined: rejected plan proposal `%s`: %s\n", p.action, reason)
	entry := fmt.Sprintf(
		"## Proposal\n\nThe plan proposal `%s` (step: %q, reason given: %q) was rejected and not applied: %s.\n\n",
		p.action, p.step, p.reason, reason)
	if err := o.files.Append("FIXES.md", entry); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the rejected proposal in FIXES.md: %v\n", err)
	}
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

// doneWhenCommand extracts the executable command from a step's Done-when
// criterion: exactly one non-empty backtick span (`cmd ...`) is the step's
// mechanical check. The rule is deliberately conservative — no backticks,
// more than one span, an unpaired backtick, or an empty span all yield no
// command — so the step falls back to the AI reviewer rather than the
// orchestrator running a fragment it cannot be sure is the whole check.
func doneWhenCommand(doneWhen string) (string, bool) {
	parts := strings.Split(doneWhen, "`")
	if len(parts) != 3 { // exactly two backticks delimit exactly one span
		return "", false
	}
	cmd := strings.TrimSpace(parts[1])
	return cmd, cmd != ""
}

// verifyNewSteps verifies every step the last iteration newly checked,
// comparing the steps file against its pre-iteration snapshot. Verification
// is two-tier: a step whose Done-when criterion quotes an executable command
// in backticks is verified mechanically — the orchestrator runs the command
// itself, with no AI judgment and no reviewer invocation spent — while every
// other step falls back to the independent AI reviewer (gated by cfg.Verify),
// which unchecks a step whose acceptance criterion is not genuinely met and
// records why in FIXES.md. The returned map names, per step index, the
// command that mechanically rejected it, so recordRejections can name the
// exact check. Ping-pong between worker and verifier is bounded because a
// rejection leaves the completed count unchanged, which the stall counter
// (checked right after this pass) treats as a no-progress iteration. A failed
// verifier invocation counts toward the consecutive-failure cap like any
// other; the step simply stays checked.
func (o *Orchestrator) verifyNewSteps(ctx context.Context, before []Step) (map[int]string, models.Outcome, bool) {
	var doneWhenFailed map[int]string
	for i, step := range o.parsedSteps() {
		if !step.Completed || (i < len(before) && before[i].Completed) {
			continue
		}
		if cmd, ok := doneWhenCommand(step.DoneWhen); ok {
			if !o.doneWhenCheckPassed(ctx, i, cmd) {
				if doneWhenFailed == nil {
					doneWhenFailed = map[int]string{}
				}
				doneWhenFailed[i] = cmd
			}
			continue
		}
		if !o.cfg.Verify {
			continue
		}
		fmt.Fprintf(o.terminal, "determined: verifying step %d\n", i+1)
		if outcome, stop := o.invoke(ctx, verifyPrompt(i+1, step)); stop {
			return doneWhenFailed, outcome, true
		}
	}
	return doneWhenFailed, models.OutcomeStopped, false // outcome ignored when stop is false
}

// doneWhenCheckPassed verifies one newly checked step mechanically by running
// the command its Done-when criterion quotes, via `sh -c` like the check
// gate, streamed live and bounded by the same 10-minute timeout, and reports
// whether the step is verified. A failure — non-zero exit or timeout — is a
// verdict on the work, never a tool failure, so it never counts toward the
// consecutive-failure cap: the step is unchecked again and the command's
// output tail recorded under a `## Step N` FIXES.md heading mirroring the
// gate's entry, so the re-run worker sees exactly what failed. File errors
// are noted and ignored: like the gate, the check delivers verdicts, it never
// ends the run.
func (o *Orchestrator) doneWhenCheckPassed(ctx context.Context, i int, cmd string) bool {
	fmt.Fprintf(o.terminal, "determined: running step %d's done-when check: %s\n", i+1, cmd)
	var output bytes.Buffer
	runCtx, cancel := context.WithTimeout(ctx, checkCmdTimeout)
	err := o.runner.Run(runCtx,
		models.Invocation{Binary: "sh", Args: []string{"-c", cmd}},
		io.MultiWriter(o.terminal, &output))
	cancel()
	if err == nil {
		return true
	}
	content, fileErr := o.files.Read(o.cfg.StepsFile)
	if fileErr == nil {
		fileErr = o.files.Write(o.cfg.StepsFile, UncheckSteps(content, []int{i}))
	}
	if fileErr != nil {
		fmt.Fprintf(o.terminal,
			"determined: could not uncheck step %d after its failed done-when check: %v\n", i+1, fileErr)
	}
	entry := fmt.Sprintf(
		"## Step %d\n\nDone-when check `%s` failed (%v). Output tail:\n\n```\n%s\n```\n\n",
		i+1, cmd, err, tail(output.String(), checkOutputTailLimit))
	if err := o.files.Append("FIXES.md", entry); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not record the done-when failure in FIXES.md: %v\n", err)
	}
	fmt.Fprintf(o.terminal,
		"determined: done-when check failed; unchecked step %d and recorded the output in FIXES.md\n", i+1)
	return false
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
		"the code and running the stated check. If NOTES.md exists, read %s, for the "+
		"conventions earlier steps recorded. "+
		"If not genuinely done, change the step's `[x]` back to `[ ]` in STEPS.md "+
		"and append an entry to FIXES.md under a `## Step %d` heading stating the "+
		"specific failing check and what would make it pass; if done, do nothing. "+
		"Do not fix anything yourself, do not modify code, and do not check or "+
		"uncheck any step other than step %d.", notesReadInstruction(), n, n)
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

// stashRecord is one stashed failed attempt: the immutable stash commit hash
// the re-run prompt and drop bookkeeping key off, plus the one-line diffstat
// summary the STALLED.md handoff shows per attempt.
type stashRecord struct {
	hash string
	stat string
}

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
// cross-iteration memory, PROPOSALS.md may hold plan proposals awaiting
// judgment, the planning files may sit uncommitted from a --plan session in
// the same directory, the run report and stall handoff are the orchestrator's
// own output, and the log directory holds the run's own iteration logs.
func (o *Orchestrator) protectedPaths() []string {
	paths := []string{
		o.cfg.PlanFile, o.cfg.StepsFile, o.cfg.StopFile,
		"NOTES.md", "FIXES.md", proposalsFile, "GOAL.md", "QUESTIONS.md", "ANSWERS.md", "OVERSIZED.md",
		runReportFile, stalledReportFile,
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

// recordRejections updates the per-step rejection records once the gate,
// verifier, and audit have delivered their verdicts, and stashes the failed
// attempt when the same step reaches stashAfterRejections. A rejection is a
// step this iteration's work checked that lost the check to the gate, the
// done-when check, or the verifier — a failed gate skips the verification
// pass and doneWhenFailed names the steps the mechanical check rejected, so
// which of the three rejected is known here and recorded as the reason; steps
// the audit reopened count toward the same totals (they were finished work
// that turned out wrong) but trigger no stash, since their work was committed
// at checkpoint time and the tree holds nothing to stash.
func (o *Orchestrator) recordRejections(ctx context.Context, intent iterationIntent, before, afterWork []Step, gatePassed bool, doneWhenFailed map[int]string) {
	final := o.parsedSteps()
	uncheckedNow := func(i int) bool { return i < len(final) && !final[i].Completed }
	stashTarget := -1
	for _, i := range newlyCheckedIndices(afterWork, before) {
		if uncheckedNow(i) {
			reason := "verifier rejected"
			switch {
			case !gatePassed:
				reason = "check command failed: " + o.cfg.CheckCmd
			case doneWhenFailed[i] != "":
				reason = "done-when check failed: " + doneWhenFailed[i]
			}
			key := stepKey(afterWork[i])
			o.rejections[key] = append(o.rejections[key], reason)
			if stashTarget == -1 {
				stashTarget = i
			}
		}
	}
	if intent.kind == invocationAudit {
		for i, s := range before {
			if s.Completed && uncheckedNow(i) {
				key := stepKey(s)
				o.rejections[key] = append(o.rejections[key], "audit reopened")
			}
		}
	}
	if stashTarget == -1 {
		return
	}
	if step := afterWork[stashTarget]; len(o.rejections[stepKey(step)]) >= stashAfterRejections {
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
		n, len(o.rejections[key]), strings.TrimSpace(step.Text))
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
	stat, _ := o.git(ctx, "stash", "show", "--include-untracked", "--stat", hash)
	o.stashes[key] = append(o.stashes[key], stashRecord{hash: hash, stat: statSummary(stat)})
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

// statSummary reduces `git stash show --stat` output to its trailing summary
// line ("4 files changed, 120 insertions(+), 30 deletions(-)"), the one-liner
// the STALLED.md handoff shows per stashed attempt.
func statSummary(stat string) string {
	lines := strings.Split(strings.TrimSpace(stat), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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
	records := o.stashes[key]
	if len(records) == 0 {
		return
	}
	out, err := o.git(ctx, "stash", "list", "--format=%H")
	if err != nil {
		return
	}
	owned := make(map[string]bool, len(records))
	for _, r := range records {
		owned[r.hash] = true
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

// notesTailLines is roughly how much of NOTES.md's chronological tail each
// invocation is told to read. NOTES.md grows without bound across iterations,
// so a whole-file read would inject every stale note as memory forever;
// instead every read instruction carries a pinned+tail contract — the durable
// `## Pinned` section at the top plus this recent tail. The contract lives
// entirely in the prompt text: the orchestrator never parses, truncates, or
// rewrites NOTES.md itself. The figure sits here so the read window is tuned
// in one place.
const notesTailLines = 60

// notesReadInstruction renders the pinned+tail read contract injected wherever
// a prompt tells the tool to read NOTES.md, so every invocation reads the same
// window: the durable pinned facts plus the recent tail, never the whole file.
func notesReadInstruction() string {
	return fmt.Sprintf("its `## Pinned` section at the top plus roughly the last %d lines, "+
		"not the whole file", notesTailLines)
}

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
// empty rubber stamp, and the required evidence block makes the report
// checkable: the orchestrator re-runs the listed commands itself before
// accepting the approval.
const auditPrompt = promptPreamble +
	"All steps in STEPS.md are checked complete. Read PLAN.md and STEPS.md. " +
	"Audit whether the implementation genuinely satisfies the plan. " +
	"Run the project's build and test suite as part of the audit, and check that the " +
	"steps work together as a whole, not just individually. " +
	"If a step is not actually satisfied, change its `[x]` back to `[ ]` in STEPS.md " +
	"and append the reason to FIXES.md; do not fix anything yourself. " +
	"If everything is satisfied, create STOP.md containing a short report: what was " +
	"built, what checks you ran, and their results. STOP.md must also contain a " +
	"fenced code block whose info string is `evidence`, listing one command per line " +
	"— the project's real build and test commands you actually ran, for example:\n\n" +
	"```evidence\ngo build ./...\ngo test ./...\n```\n\n" +
	"The orchestrator will re-run every listed command itself and reject STOP.md if " +
	"the block is missing or any command fails. " +
	"Do not start work beyond this audit."

// auditMissingEvidenceNote extends the audit prompt after a STOP.md was
// rejected for lacking the evidence block, so the re-run audit knows exactly
// why its previous approval did not stand.
const auditMissingEvidenceNote = " Note: a previous STOP.md was rejected and deleted " +
	"because it lacked the required ```evidence block; this time include the block " +
	"listing the commands you ran."

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
		prompt := auditPrompt
		if o.auditLackedEvidence {
			prompt += auditMissingEvidenceNote
		}
		return prompt, iterationIntent{kind: invocationAudit}, nil
	}
	i, step, ok := NextIncompleteStep(steps)
	if !ok {
		return noParsableStepsPrompt, iterationIntent{kind: invocationRewrite}, nil
	}
	prompt := stepPrompt(i+1, step)
	if records := o.stashes[stepKey(step)]; len(records) > 0 {
		prompt += stashNote(records[len(records)-1].hash)
	}
	if o.cfg.MaxPlanChanges > 0 {
		prompt += proposalNote
	}
	return prompt, iterationIntent{kind: invocationStep, target: i}, nil
}

// proposalNote extends a step prompt when the proposal channel is open
// (cfg.MaxPlanChanges > 0). The tamper guard makes direct plan edits futile,
// so without this note a worker that discovers the plan is wrong could only
// plough on; the note names the one legitimate way to change the plan, in the
// exact format the orchestrator judges mechanically — a free-form proposal
// would only earn a FIXES.md rejection.
const proposalNote = " Direct STEPS.md edits beyond checking your own step's box are " +
	"reverted. If you discover the plan itself needs to change — a step is obsolete, " +
	"missing, or wrongly specified — append a section to PROPOSALS.md in exactly this " +
	"format:\n\n## Proposal\naction: add-after 2\nstep: - [ ] <new step text>. " +
	"Done when: <checkable criterion>\nreason: <why the plan must change>\n\n" +
	"Supported actions: `add-after N` (insert the step: line after step N; 0 inserts at " +
	"the top), `remove N`, and `reword N` (step: holds the replacement line). The " +
	"orchestrator validates and applies proposals mechanically between iterations; only " +
	"unchecked steps may be changed, and rejected proposals are explained in FIXES.md."

// stepPrompt builds the execute instruction for a single step: work only that
// step, meet its acceptance criterion, and check its box when done. NOTES.md
// carries knowledge between otherwise-independent invocations: each iteration
// reads the pinned section plus the recent tail of what earlier steps recorded
// and appends what later steps need to know, promoting durable facts to
// `## Pinned` so they outlive the tail window.
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
	fmt.Fprintf(&b, "If NOTES.md exists, read %s, before starting. ", notesReadInstruction())
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
		"once it passes.", n)
	if cmd, ok := doneWhenCommand(step.DoneWhen); ok {
		fmt.Fprintf(&b, " The orchestrator will re-run `%s` itself after this invocation "+
			"to verify the step mechanically, so that exact command must exit 0 in this "+
			"working tree.", cmd)
	}
	b.WriteString(" Do not check or uncheck any other step, and do not create " +
		"STOP.md. Before finishing, append to NOTES.md any decisions, conventions, or " +
		"gotchas later steps need to know. Older entries scroll out of the read window, " +
		"so promote durable, always-relevant facts — project conventions, invariants, " +
		"gotchas every future step needs — into the `## Pinned` section at the top, " +
		"keeping it small; that section may be edited in place, everything below it " +
		"is append-only.")
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
	fmt.Fprintf(&b, " Read PLAN.md, plus FIXES.md if it exists, to see what has gone wrong "+
		"so far; if NOTES.md exists, read %s. Replace step %d in STEPS.md with 2-4 smaller `- [ ]` "+
		"checkbox steps, each ending with an indented `Done when:` line stating a "+
		"checkable acceptance condition — phrased as a single backtick-quoted executable "+
		"command whenever possible, since the orchestrator re-runs such commands itself "+
		"to verify the step — ordered so completing them all completes the "+
		"original step. Keep every other step exactly as it is, in order. Do not check "+
		"any box, do not implement anything, and do not create STOP.md.",
		notesReadInstruction(), n)
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

// runReportFile is where every execute run writes its machine-readable
// summary, alongside the protocol files in the working directory.
const runReportFile = "run-report.json"

// stalledReportFile is where a stalled run writes its human-readable handoff
// report — run-report.json's sibling for the person who inherits the stall.
const stalledReportFile = "STALLED.md"

// runReport is the summary written to runReportFile on every termination of
// the execute loop, so success and failure report symmetrically instead of
// success getting STOP.md and everything else just an exit code. Fields that
// do not apply to a run are omitted rather than written empty.
type runReport struct {
	// Outcome is a short machine string: "success", "stalled", or "failed".
	Outcome string `json:"outcome"`
	// Exit mirrors the process exit code the run returns.
	Exit int `json:"exit"`
	// Steps summarizes the final steps file; omitted when nothing parses.
	Steps *runReportSteps `json:"steps,omitempty"`
	// StuckStep is the 1-based step a stalled run could not get past; present
	// only when the run stalled.
	StuckStep int `json:"stuck_step,omitempty"`
	// Rejections counts, per step, how many times the check gate, done-when
	// check, verifier, or audit rejected a checked box, keyed by the step's
	// current number (or by its text, when a replan removed the step from the
	// file); STOP.md evidence rejections appear under the "audit" key.
	Rejections map[string]int `json:"rejections,omitempty"`
	// Report names the human-readable stall handoff written alongside this
	// report; present only when the run stalled and STALLED.md was written.
	Report string `json:"report,omitempty"`
	// ReplansUsed is how many replan invocations the run spent on stuck steps.
	ReplansUsed int `json:"replans_used,omitempty"`
	// Iterations is the run's total tool invocations: work, verify, audit,
	// and replan alike.
	Iterations int `json:"iterations"`
	// WallSeconds is the run's wall-clock duration.
	WallSeconds int `json:"wall_seconds"`
	// LogDir is where the per-iteration logs were written.
	LogDir string `json:"log_dir,omitempty"`
}

// runReportSteps is the report's step tally: how many steps the final steps
// file holds and how many of them are checked.
type runReportSteps struct {
	Total   int `json:"total"`
	Checked int `json:"checked"`
}

// reportOutcome maps the run's terminal outcome onto the report's machine
// string: "success" for a clean completion, "stalled" for a no-progress stop,
// and "failed" for every other termination (tool-failure abort, exhausted
// budget, interruption, missing protocol files) — the same grouping
// Outcome.ExitCode uses.
func reportOutcome(outcome models.Outcome) string {
	switch outcome {
	case models.OutcomeStopped:
		return "success"
	case models.OutcomeStalled:
		return "stalled"
	default:
		return "failed"
	}
}

// removeStaleReports deletes the run-report.json and STALLED.md a previous
// run may have left, so a fresh run never sits alongside reports describing
// an older one — and a stale PROPOSALS.md, so plan proposals meant for an
// older run are never applied to this one. Runs before anything else, so even
// a run that ends at the missing-files check replaces the stale reports with
// its own.
func (o *Orchestrator) removeStaleReports() {
	for _, f := range []string{runReportFile, stalledReportFile, proposalsFile} {
		if !o.files.Exists(f) {
			continue
		}
		if err := o.files.Remove(f); err != nil {
			fmt.Fprintf(o.terminal, "determined: warning: could not remove stale %s: %v\n",
				f, err)
		}
	}
}

// writeRunReport writes the machine-readable run summary, built entirely from
// state the loop already tracks; report names the STALLED.md handoff when one
// was written, "" otherwise. Reporting is an observer, never a participant: a
// write failure is warned to the terminal and ignored, so it can never change
// the run's outcome.
func (o *Orchestrator) writeRunReport(outcome models.Outcome, start time.Time, stalledReport string) {
	steps := o.parsedSteps()
	report := runReport{
		Outcome:     reportOutcome(outcome),
		Exit:        outcome.ExitCode(),
		Rejections:  o.reportRejections(steps),
		Report:      stalledReport,
		ReplansUsed: o.replansUsed,
		Iterations:  o.iteration,
		WallSeconds: int(o.clock.Now().Sub(start) / time.Second),
		LogDir:      o.cfg.LogDir,
	}
	if len(steps) > 0 {
		report.Steps = &runReportSteps{Total: len(steps), Checked: CompletedStepCount(steps)}
	}
	if outcome == models.OutcomeStalled {
		if i, _, ok := NextIncompleteStep(steps); ok {
			report.StuckStep = i + 1
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = o.files.Write(runReportFile, string(data)+"\n")
	}
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: warning: could not write %s: %v\n", runReportFile, err)
	}
}

// reportRejections rekeys the per-step rejection counts for the report: the
// loop tracks them by step text and criterion so they survive replans, but a
// step's current number is what a reader of the report can act on. A rejected
// step a replan removed from the file falls back to its text as the key.
func (o *Orchestrator) reportRejections(steps []Step) map[string]int {
	if len(o.rejections) == 0 {
		return nil
	}
	numbers := make(map[string]string, len(steps))
	for i, s := range steps {
		numbers[stepKey(s)] = strconv.Itoa(i + 1)
	}
	rejections := make(map[string]int, len(o.rejections))
	for key, reasons := range o.rejections {
		label, ok := numbers[key]
		if !ok {
			label = strings.SplitN(key, "\x00", 2)[0]
		}
		rejections[label] += len(reasons)
	}
	return rejections
}

// notifyCmdTimeout bounds the notify command so a hung notification hook
// cannot keep a finished run from exiting.
const notifyCmdTimeout = time.Minute

// runNotifyCmd runs the --notify-cmd exit hook once, after the reports are
// written, exporting the run's terminal state as DET_* environment variables
// so an unattended run can announce how it ended. It deliberately ignores the
// run context: an interrupted run must still notify, so the command gets a
// fresh context bounded by notifyCmdTimeout instead of the already-cancelled
// one. Like reporting, the hook is an observer: a failing or timed-out
// command is warned to the terminal and ignored, never changing the outcome.
func (o *Orchestrator) runNotifyCmd(outcome models.Outcome, start time.Time) {
	if o.cfg.NotifyCmd == "" {
		return
	}
	env := []string{
		"DET_OUTCOME=" + reportOutcome(outcome),
		"DET_EXIT=" + strconv.Itoa(outcome.ExitCode()),
		"DET_WALL=" + formatDuration(o.clock.Now().Sub(start)),
		"DET_DIR=" + o.cfg.WorkDir,
	}
	if outcome == models.OutcomeStalled {
		// DET_STEP mirrors the run report's stuck_step: the first unchecked
		// step, set only when a stalled run has one to name.
		if i, _, ok := NextIncompleteStep(o.parsedSteps()); ok {
			env = append(env, "DET_STEP="+strconv.Itoa(i+1))
		}
	}
	fmt.Fprintf(o.terminal, "determined: running notify command: %s\n", o.cfg.NotifyCmd)
	runCtx, cancel := context.WithTimeout(context.Background(), notifyCmdTimeout)
	defer cancel()
	err := o.runner.Run(runCtx,
		models.Invocation{Binary: "sh", Args: []string{"-c", o.cfg.NotifyCmd}, Env: env},
		o.terminal)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: warning: notify command failed: %v\n", err)
	}
}

// writeStalledReport writes the STALLED.md handoff on a stalled termination
// and removes any STALLED.md on every other one (a tool invocation could have
// created its own), so the file's presence always means "this run stalled".
// It reports whether the file was written. The handoff is purely mechanical —
// built from state the loop already tracks, no extra AI invocation — and,
// like the run report, an observer: a write failure is warned and ignored.
func (o *Orchestrator) writeStalledReport(outcome models.Outcome, start time.Time) bool {
	if outcome != models.OutcomeStalled {
		if o.files.Exists(stalledReportFile) {
			if err := o.files.Remove(stalledReportFile); err != nil {
				fmt.Fprintf(o.terminal, "determined: warning: could not remove %s: %v\n",
					stalledReportFile, err)
			}
		}
		return false
	}
	if err := o.files.Write(stalledReportFile, o.stalledReport(start)); err != nil {
		fmt.Fprintf(o.terminal, "determined: warning: could not write %s: %v\n",
			stalledReportFile, err)
		return false
	}
	fmt.Fprintf(o.terminal, "determined: wrote the stall handoff report to %s\n", stalledReportFile)
	return true
}

// stalledReport renders the handoff markdown: the stuck step (the first
// unchecked one) with its criterion, one line per recorded rejection reason,
// the stashed attempts to inspect, and the run's counters. FIXES.md keeps the
// full rejection entries; this file is the map to them.
func (o *Orchestrator) stalledReport(start time.Time) string {
	var b strings.Builder
	steps := o.parsedSteps()
	switch i, step, ok := NextIncompleteStep(steps); {
	case ok:
		fmt.Fprintf(&b, "# Run stalled at step %d\n\n", i+1)
		fmt.Fprintf(&b, "step: %q\n", step.Text)
		if step.DoneWhen != "" {
			fmt.Fprintf(&b, "done when: %s\n", step.DoneWhen)
		}
		key := stepKey(step)
		if reasons := o.rejections[key]; len(reasons) > 0 {
			fmt.Fprintf(&b, "rejections: %d (full entries in FIXES.md)\n", len(reasons))
			for n, reason := range reasons {
				fmt.Fprintf(&b, "  %d. %s\n", n+1, reason)
			}
		} else {
			b.WriteString("rejections: none recorded (the step was never checked)\n")
		}
		if records := o.stashes[key]; len(records) > 0 {
			b.WriteString("stashed attempts:\n")
			for _, r := range records {
				fmt.Fprintf(&b, "  %s  %s\n", r.hash, r.stat)
			}
		}
	case len(steps) > 0:
		// Audit ping-pong: every box is checked but the whole-plan audit
		// neither approved nor reopened anything — or its approvals kept
		// failing evidence validation — so no single step is stuck.
		b.WriteString("# Run stalled at the whole-plan audit\n\n")
		fmt.Fprintf(&b, "Every step is checked, but the audit neither approved (no %s) "+
			"nor reopened a step.\n", o.cfg.StopFile)
		if reasons := o.rejections[auditRejectionKey]; len(reasons) > 0 {
			fmt.Fprintf(&b, "rejections: %d (full entries in FIXES.md)\n", len(reasons))
			for n, reason := range reasons {
				fmt.Fprintf(&b, "  %d. %s\n", n+1, reason)
			}
		}
	default:
		b.WriteString("# Run stalled\n\n")
		fmt.Fprintf(&b, "%s holds no parseable checkbox steps, so no stuck step can be named.\n",
			o.cfg.StepsFile)
	}
	fmt.Fprintf(&b, "iterations: %d\n", o.iteration)
	wall := formatDuration(o.clock.Now().Sub(start))
	if o.cfg.Budget > 0 {
		fmt.Fprintf(&b, "wall time: %s of %s budget\n", wall, formatDuration(o.cfg.Budget))
	} else {
		fmt.Fprintf(&b, "wall time: %s (no budget)\n", wall)
	}
	if o.cfg.MaxReplans > 0 {
		fmt.Fprintf(&b, "replans used: %d of %d\n", o.replansUsed, o.cfg.MaxReplans)
	}
	return b.String()
}

// formatDuration renders a duration compactly for the handoff ("42m",
// "1h30m", "45s"), dropping the zero trailing units Duration.String prints.
func formatDuration(d time.Duration) string {
	s := d.Round(time.Second).String()
	if s != "0s" {
		s = strings.TrimSuffix(s, "0s")
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
