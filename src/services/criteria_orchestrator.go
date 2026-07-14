package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"determined/src/models"
)

// criteriaAction is the user's verdict on one proposed BDD test.
type criteriaAction int

const (
	actionAccept criteriaAction = iota // keep this test, ask for another journey
	actionModify                       // record a revision note and redraft
	actionSkip                         // forget this draft, ask for another journey
	actionEnd                          // keep this test and every prior one, finish
	actionCancel                       // discard every test from this session
)

// CriteriaOrchestrator runs the attended criteria session: the user describes
// a user journey, the tool proposes one BDD test for it, and the user accepts,
// modifies, skips, ends, or cancels. Accepted tests accumulate in the criteria
// file, which the planning prompt and the final execution audit treat as
// required acceptance tests.
type CriteriaOrchestrator struct {
	runner   CommandRunner
	files    FileStore
	prompter Prompter
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.CriteriaConfig

	iteration int
	accepted  int
	snapshot  string // criteria file content at session start, restored on cancel
	hadFile   bool
}

// NewCriteriaOrchestrator wires a CriteriaOrchestrator from its dependencies.
func NewCriteriaOrchestrator(
	runner CommandRunner,
	files FileStore,
	prompter Prompter,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.CriteriaConfig,
) *CriteriaOrchestrator {
	return &CriteriaOrchestrator{
		runner:   runner,
		files:    files,
		prompter: prompter,
		clock:    clock,
		logs:     logs,
		terminal: terminal,
		cfg:      cfg,
	}
}

// Run executes the criteria session and returns the terminal outcome. Each
// round asks for a journey description, has the tool draft one BDD test, and
// relays the proposal to the user for a verdict. An empty description ends
// the session, keeping whatever has been accepted so far.
func (o *CriteriaOrchestrator) Run(ctx context.Context) models.Outcome {
	deadline := o.deadline()
	if outcome, stop := o.recordSnapshot(); stop {
		return outcome
	}
	defer o.removeTransientFiles()
	for {
		switch {
		case ctx.Err() != nil:
			return models.OutcomeInterrupted
		case o.budgetExceeded(deadline):
			return models.OutcomeBudgetExceeded
		}
		description, err := o.prompter.Ask(
			"Describe a user journey to protect with a BDD test (press Enter to finish)")
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
			return models.OutcomeInterrupted
		}
		if strings.TrimSpace(description) == "" {
			return o.finish()
		}
		if outcome, stop := o.draftAndReview(ctx, deadline, description); stop {
			return outcome
		}
	}
}

// recordSnapshot captures the pre-session criteria file so cancel can restore
// it: accepted tests are appended durably as the session goes, and cancel
// must discard only what this session added.
func (o *CriteriaOrchestrator) recordSnapshot() (models.Outcome, bool) {
	if !o.files.Exists(o.cfg.CriteriaFile) {
		return models.OutcomeCriteriaReady, false
	}
	content, err := o.files.Read(o.cfg.CriteriaFile)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.CriteriaFile, err)
		return models.OutcomeDroidFailed, true
	}
	o.snapshot = content
	o.hadFile = true
	return models.OutcomeCriteriaReady, false
}

// draftAndReview turns one journey description into a reviewed BDD test: the
// tool proposes a draft and the loop repeats through modify verdicts until
// the user accepts, skips, ends, or cancels. It reports whether the session
// should stop.
func (o *CriteriaOrchestrator) draftAndReview(
	ctx context.Context,
	deadline time.Time,
	description string,
) (models.Outcome, bool) {
	request := "# Journey\n\n" + strings.TrimSpace(description) + "\n"
	if err := o.files.Write(o.cfg.RequestFile, request); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.RequestFile, err)
		return models.OutcomeDroidFailed, true
	}
	o.files.Remove(o.cfg.DraftFile)
	for {
		switch {
		case ctx.Err() != nil:
			return models.OutcomeInterrupted, true
		case o.budgetExceeded(deadline):
			return models.OutcomeBudgetExceeded, true
		}
		if outcome, stop := o.runInvocation(ctx, o.cfg.Invocation, "proposing BDD test"); stop {
			return outcome, true
		}
		draft, ok := o.readDraft()
		if !ok {
			return models.OutcomeCriteriaStalled, true
		}
		fmt.Fprintf(o.terminal, "\nProposed BDD test:\n\n%s\n", strings.TrimSpace(draft))
		action, interrupted := o.reviewAction()
		if interrupted {
			return models.OutcomeInterrupted, true
		}
		switch action {
		case actionAccept:
			if outcome, stop := o.acceptDraft(draft); stop {
				return outcome, true
			}
			return models.OutcomeCriteriaReady, false // ask for the next journey
		case actionModify:
			if outcome, stop := o.recordRevision(); stop {
				return outcome, true
			}
			// Loop: rerun the tool against the appended revision request.
		case actionSkip:
			return models.OutcomeCriteriaReady, false // forget this draft, ask for another
		case actionEnd:
			if outcome, stop := o.acceptDraft(draft); stop {
				return outcome, true
			}
			return o.finish(), true
		case actionCancel:
			return o.cancel(), true
		}
	}
}

// reviewAction asks for a verdict on the presented draft until the answer is
// one of the five recognised actions. It reports whether reading the answer
// was interrupted.
func (o *CriteriaOrchestrator) reviewAction() (criteriaAction, bool) {
	for {
		answer, err := o.prompter.Ask("Accept, modify, skip, end, or cancel? [a/m/s/e/c]")
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
			return actionCancel, true
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "a", "accept":
			return actionAccept, false
		case "m", "modify":
			return actionModify, false
		case "s", "skip":
			return actionSkip, false
		case "e", "end":
			return actionEnd, false
		case "c", "cancel":
			return actionCancel, false
		default:
			fmt.Fprintln(o.terminal, "determined: answer accept, modify, skip, end, or cancel")
		}
	}
}

// recordRevision appends the user's requested change to the request file so
// the next tool run revises the draft instead of starting over. It reports
// whether the session should stop.
func (o *CriteriaOrchestrator) recordRevision() (models.Outcome, bool) {
	note, err := o.prompter.Ask("How should the BDD test be updated?")
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
		return models.OutcomeInterrupted, true
	}
	revision := "\n## Revision\n\n" + strings.TrimSpace(note) + "\n"
	if err := o.files.Append(o.cfg.RequestFile, revision); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.RequestFile, err)
		return models.OutcomeDroidFailed, true
	}
	return models.OutcomeCriteriaReady, false
}

// readDraft returns the tool's proposed BDD test, reporting whether a usable
// draft exists.
func (o *CriteriaOrchestrator) readDraft() (string, bool) {
	if !o.files.Exists(o.cfg.DraftFile) {
		fmt.Fprintf(o.terminal, "determined: the tool wrote no BDD draft to %s\n", o.cfg.DraftFile)
		return "", false
	}
	draft, err := o.files.Read(o.cfg.DraftFile)
	if err != nil || strings.TrimSpace(draft) == "" {
		fmt.Fprintf(o.terminal, "determined: the tool wrote no usable BDD draft to %s\n", o.cfg.DraftFile)
		return "", false
	}
	return draft, true
}

// acceptDraft appends the draft to the criteria file immediately, so accepted
// tests survive an interrupt; cancel restores the pre-session snapshot. It
// reports whether the session should stop.
func (o *CriteriaOrchestrator) acceptDraft(draft string) (models.Outcome, bool) {
	existing := ""
	if o.files.Exists(o.cfg.CriteriaFile) {
		content, err := o.files.Read(o.cfg.CriteriaFile)
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.CriteriaFile, err)
			return models.OutcomeDroidFailed, true
		}
		existing = content
	}
	var entry strings.Builder
	if strings.TrimSpace(existing) == "" {
		entry.WriteString(criteriaHeader)
	}
	fmt.Fprintf(&entry, "## Test %d\n\n```gherkin\n%s\n```\n\n",
		strings.Count(existing, "## Test ")+1, strings.TrimSpace(draft))
	if err := o.files.Append(o.cfg.CriteriaFile, entry.String()); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.CriteriaFile, err)
		return models.OutcomeDroidFailed, true
	}
	o.accepted++
	o.files.Remove(o.cfg.DraftFile)
	return models.OutcomeCriteriaReady, false
}

// finish ends the session normally: with accepted tests the criteria file is
// the deliverable; with none the session is equivalent to a cancel.
func (o *CriteriaOrchestrator) finish() models.Outcome {
	if o.accepted == 0 {
		fmt.Fprintln(o.terminal, "determined: no BDD tests were accepted; nothing was recorded")
		return models.OutcomeCriteriaCancelled
	}
	fmt.Fprintf(o.terminal, "determined: %d BDD test(s) recorded in %s\n", o.accepted, o.cfg.CriteriaFile)
	return models.OutcomeCriteriaReady
}

// cancel discards every test accepted this session, restoring the criteria
// file to its pre-session state.
func (o *CriteriaOrchestrator) cancel() models.Outcome {
	if o.hadFile {
		if err := o.files.Write(o.cfg.CriteriaFile, o.snapshot); err != nil {
			fmt.Fprintf(o.terminal, "determined: could not restore %s: %v\n", o.cfg.CriteriaFile, err)
			return models.OutcomeDroidFailed
		}
	} else {
		o.files.Remove(o.cfg.CriteriaFile)
	}
	o.accepted = 0
	return models.OutcomeCriteriaCancelled
}

// removeTransientFiles clears the request and draft working files; only the
// criteria file itself outlives the session.
func (o *CriteriaOrchestrator) removeTransientFiles() {
	o.files.Remove(o.cfg.RequestFile)
	o.files.Remove(o.cfg.DraftFile)
}

// runInvocation runs a single tool invocation, teeing its output to the
// terminal and a per-iteration log. It reports whether the session should stop.
func (o *CriteriaOrchestrator) runInvocation(
	ctx context.Context,
	inv models.Invocation,
	progress progressMessage,
) (models.Outcome, bool) {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return models.OutcomeDroidFailed, true
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	writeProgress(out, o.clock, progress)
	if err := o.runner.Run(ctx, inv, out); err != nil {
		if ctx.Err() != nil {
			return models.OutcomeInterrupted, true
		}
		return models.OutcomeDroidFailed, true
	}
	return models.OutcomeCriteriaReady, false // outcome ignored when stop is false
}

// deadline returns the instant the session must stop by, or the zero time
// when the budget is unlimited.
func (o *CriteriaOrchestrator) deadline() time.Time {
	if o.cfg.Budget <= 0 {
		return time.Time{}
	}
	return o.clock.Now().Add(o.cfg.Budget)
}

func (o *CriteriaOrchestrator) budgetExceeded(deadline time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return !o.clock.Now().Before(deadline)
}
