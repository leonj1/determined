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

	iteration  int
	goalSeeded bool
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
		if outcome, stop := o.seedGoal(); stop {
			return outcome
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

// seedGoal ensures the planning tool has a goal to read without silently
// replacing a goal file the user may have prepared by hand.
func (o *PlanOrchestrator) seedGoal() (models.Outcome, bool) {
	if o.goalSeeded {
		return models.OutcomePlanReady, false
	}
	if useExisting, outcome, stop := o.resolveExistingGoal(); stop || useExisting {
		return outcome, stop
	}
	return o.writeGoal()
}

func (o *PlanOrchestrator) resolveExistingGoal() (bool, models.Outcome, bool) {
	if !o.files.Exists(o.cfg.GoalFile) {
		return false, models.OutcomePlanReady, false
	}
	content, err := o.files.Read(o.cfg.GoalFile)
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.GoalFile, err)
		return false, models.OutcomeDroidFailed, true
	}
	if incompleteGoal(content) {
		fmt.Fprintf(o.terminal, "determined: %s is empty or only a bare heading; replacing it with --plan input\n", o.cfg.GoalFile)
		return false, models.OutcomePlanReady, false
	}
	useExisting, err := o.useExistingGoal()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
		return false, models.OutcomeInterrupted, true
	}
	if useExisting {
		o.goalSeeded = true
	}
	return useExisting, models.OutcomePlanReady, false
}

func (o *PlanOrchestrator) writeGoal() (models.Outcome, bool) {
	goal, err := o.goalContent()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: %v\n", err)
		return models.OutcomeInvalidGoal, true
	}
	if err := o.files.Write(o.cfg.GoalFile, goal); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.GoalFile, err)
		return models.OutcomeDroidFailed, true
	}
	o.goalSeeded = true
	return models.OutcomePlanReady, false
}

func (o *PlanOrchestrator) goalContent() (string, error) {
	source := o.goalSourcePath()
	if source == "" {
		if incompleteGoal(o.cfg.Goal) {
			return "", incompleteGoalError("the --plan value")
		}
		return o.cfg.Goal + "\n", nil
	}
	content, err := o.files.Read(source)
	if err != nil {
		return "", fmt.Errorf("could not read goal source %s: %w", source, err)
	}
	if incompleteGoal(content) {
		return "", incompleteGoalError("goal source " + source)
	}
	return content, nil
}

func incompleteGoal(content string) bool {
	trimmed := strings.TrimSpace(content)
	return trimmed == "" || strings.Trim(trimmed, "#") == ""
}

func incompleteGoalError(source string) error {
	return fmt.Errorf("%s is empty or contains only a bare `#` heading; pass a goal sentence, a path like `--plan TODO.md`, `--plan \"Read TODO.md\"`, or quote command substitution as `--plan \"$(cat TODO.md)\"`", source)
}

func (o *PlanOrchestrator) goalSourcePath() string {
	goal := strings.TrimSpace(o.cfg.Goal)
	words := strings.Fields(goal)
	if len(words) == 1 && o.files.Exists(words[0]) {
		return words[0]
	}
	if len(words) > 1 && strings.EqualFold(words[0], "read") {
		return strings.TrimSpace(goal[len(words[0]):])
	}
	return ""
}

func (o *PlanOrchestrator) useExistingGoal() (bool, error) {
	for {
		answer, err := o.prompter.Ask(fmt.Sprintf("%s already exists. Use it for this plan? [y/N]", o.cfg.GoalFile))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			fmt.Fprintln(o.terminal, "determined: answer yes or no")
		}
	}
}

// refine independently checks the completed plan and resolves quality findings
// until it passes, the budget runs out, or the pass cap is hit.
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
		content, err := o.files.Read(o.cfg.AssessmentFile)
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.AssessmentFile, err)
			return models.OutcomeDroidFailed
		}
		issues := RefinementIssues(content)
		if len(issues) == 0 {
			o.files.Remove(o.cfg.AssessmentFile)
			return models.OutcomePlanReady
		}
		if pass >= o.cfg.MaxRefinePasses {
			fmt.Fprintf(o.terminal,
				"determined: %d planning issue(s) remain after %d refine pass(es); leaving the plan as-is\n",
				len(issues), pass)
			o.files.Remove(o.cfg.AssessmentFile)
			return models.OutcomePlanReady
		}

		if outcome, stop := o.runInvocation(ctx, o.cfg.RefineInvocation); stop {
			return outcome
		}
		o.files.Remove(o.cfg.AssessmentFile)
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
