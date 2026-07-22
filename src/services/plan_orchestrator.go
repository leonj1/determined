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

// PlanStatusReporter receives the planning session's observable events for the
// interactive status page. The real implementation is PlanStatusService; a nil
// reporter disables reporting.
type PlanStatusReporter interface {
	ProgressSink
	Start()
	BeginLogEntry(message string)
	AppendLogOutput(text string)
	SetGoal(goal string)
	SetPlan(plan string)
	SetDemo(demo string)
	SetTests(tests string)
	SetTaskSteps(steps []models.TaskStep)
	WaitForInput()
	Finish(succeeded bool)
	TakeAnnotation() (models.Annotation, bool)
	AnnotationSignal() <-chan struct{}
	ImplementSignal() <-chan struct{}
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
	status   PlanStatusReporter
	control  TaskController
	cfg      models.PlanConfig
	docs     *PlanDocumentPublisher

	iteration  int
	goalSeeded bool
	failures   int
	// lastSkipped reports whether the most recent invocation was aborted by
	// the user's Skip on the status page: the loop treats it as done, and
	// callers that need the invocation's artifact decide what its absence
	// means.
	lastSkipped bool
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
		docs:     NewPlanDocumentPublisher(files, cfg),
	}
}

// WithStatusReporter attaches the interactive status reporter and returns the
// orchestrator for chaining. Without one the session runs terminal-only.
func (o *PlanOrchestrator) WithStatusReporter(status PlanStatusReporter) *PlanOrchestrator {
	o.status = status
	return o
}

// WithTaskControl attaches the status page's task controller and returns the
// orchestrator for chaining. Without one the page cannot skip or stop tasks.
func (o *PlanOrchestrator) WithTaskControl(control TaskController) *PlanOrchestrator {
	o.control = control
	return o
}

// Run executes the planning loop and returns the terminal outcome.
func (o *PlanOrchestrator) Run(ctx context.Context) models.Outcome {
	o.reportStart()
	deadline := o.deadline()
	var outcome models.Outcome
	switch o.cfg.Operation {
	case models.PlanOperationCreate:
		outcome = o.create(ctx, deadline)
	case models.PlanOperationReview:
		outcome = o.review(ctx, deadline)
	default:
		fmt.Fprintf(o.terminal, "determined: unsupported plan operation %q\n", o.cfg.Operation)
		outcome = models.OutcomeDroidFailed
	}
	if planSucceeded(outcome) {
		o.refreshDemo(ctx)
	}
	o.reportFinish(outcome)
	return outcome
}

func planSucceeded(outcome models.Outcome) bool {
	return outcome == models.OutcomePlanReady || outcome == models.OutcomePlanReviewed
}

// refreshDemo clears any prior artifact after a finished plan. Interactive
// sessions then run the eligibility gate as a distinct post-plan step.
func (o *PlanOrchestrator) refreshDemo(ctx context.Context) {
	if o.cfg.DemoInvocation.Binary == "" || o.cfg.DemoFile == "" {
		return
	}
	if o.status != nil {
		o.status.SetDemo("")
	}
	o.files.Remove(o.cfg.DemoFile)
	if o.status == nil {
		return
	}
	if _, stop := o.runInvocation(ctx, o.cfg.DemoInvocation, "generating UI demo"); stop {
		return
	}
}

func (o *PlanOrchestrator) create(ctx context.Context, deadline time.Time) models.Outcome {
	for {
		switch {
		case ctx.Err() != nil:
			return models.OutcomeInterrupted
		case o.planDrafted():
			if outcome, stop := o.ensureTests(ctx); stop {
				return outcome
			}
			return o.refine(ctx, deadline)
		case o.budgetExceeded(deadline):
			return models.OutcomeBudgetExceeded
		}
		if outcome, stop := o.seedGoal(); stop {
			return outcome
		}

		if outcome, stop := o.runInvocation(ctx, o.cfg.Invocation, "planning project"); stop {
			return outcome
		}

		if o.planDrafted() {
			if outcome, stop := o.ensureTests(ctx); stop {
				return outcome
			}
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

func (o *PlanOrchestrator) review(ctx context.Context, deadline time.Time) models.Outcome {
	if ctx.Err() != nil {
		return models.OutcomeInterrupted
	}
	if !o.planDrafted() {
		fmt.Fprintf(o.terminal, "determined: review requires %s and %s\n", o.cfg.PlanFile, o.cfg.StepsFile)
		return models.OutcomeMissingFiles
	}
	if outcome, stop := o.ensureTests(ctx); stop {
		return outcome
	}
	if o.files.Exists(o.cfg.QuestionsFile) {
		if outcome, stop := o.relayQuestions(); stop {
			return outcome
		}
	}
	outcome := o.refine(ctx, deadline)
	if outcome == models.OutcomePlanReady {
		return models.OutcomePlanReviewed
	}
	return outcome
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
		o.reportGoal()
	}
	return useExisting, models.OutcomePlanReady, false
}

func (o *PlanOrchestrator) writeGoal() (models.Outcome, bool) {
	writeProgress(o.terminal, o.clock, "writing planning goal")
	notifyProgress(o.status, "writing planning goal")
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
	o.reportGoal()
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
	o.reportPlan()
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

		if outcome, stop := o.runInvocation(
			ctx, o.cfg.AssessInvocation, o.assessmentProgress()); stop {
			return outcome
		}
		if o.lastSkipped {
			// The user skipped the assessment: accept the plan as it stands
			// rather than failing on the report the assessor never wrote.
			o.files.Remove(o.cfg.AssessmentFile)
			return models.OutcomePlanReady
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
		if o.cfg.Operation == models.PlanOperationReview && o.files.Exists(o.cfg.QuestionsFile) {
			if outcome, stop := o.relayQuestions(); stop {
				return outcome
			}
		}
		if pass >= o.cfg.MaxRefinePasses {
			fmt.Fprintf(o.terminal,
				"determined: %d planning issue(s) remain after %d refine pass(es); leaving the plan as-is\n",
				len(issues), pass)
			o.files.Remove(o.cfg.AssessmentFile)
			return models.OutcomePlanReady
		}

		if outcome, stop := o.runInvocation(
			ctx, o.cfg.RefineInvocation, "refining plan"); stop {
			return outcome
		}
		o.reportPlan()
		o.files.Remove(o.cfg.AssessmentFile)
	}
}

// runInvocation runs a tool invocation, retrying transient failures until the
// consecutive-failure cap is hit. It reports whether the loop should stop. A
// Stop from the status page ends the session; a Skip records the invocation
// as user-skipped and returns as though it completed, leaving each caller's
// artifact checks to decide what the missing work means.
func (o *PlanOrchestrator) runInvocation(
	ctx context.Context,
	inv models.Invocation,
	progress progressMessage,
) (models.Outcome, bool) {
	o.lastSkipped = false
	for {
		err := o.attemptInvocation(ctx, inv, progress)
		action := o.settleTask()
		if action == models.TaskActionStop {
			fmt.Fprintln(o.terminal, "determined: run stopped from the status page")
			return models.OutcomeUserStopped, true
		}
		if err == nil {
			o.failures = 0
			return models.OutcomePlanReady, false // outcome ignored when stop is false
		}
		if action == models.TaskActionSkip {
			o.lastSkipped = true
			fmt.Fprintf(o.terminal, "determined: task %q skipped from the status page\n", progress)
			return models.OutcomePlanReady, false
		}
		if outcome, stop := o.recordFailure(ctx, err); stop {
			return outcome, stop
		}
	}
}

// settleTask deregisters the finished invocation with the task controller and
// returns the Skip or Stop verdict the page recorded against it while it ran.
func (o *PlanOrchestrator) settleTask() models.TaskAction {
	if o.control == nil {
		return models.TaskActionNone
	}
	o.control.EndTask()
	return o.control.TakeTaskAction()
}

// attemptInvocation runs one tool invocation, teeing its output to the
// terminal, a per-iteration log, and the status page. A failure is written to
// all three so the reason survives in the iteration log and on the page.
func (o *PlanOrchestrator) attemptInvocation(
	ctx context.Context,
	inv models.Invocation,
	progress progressMessage,
) error {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return err
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	writeProgress(out, o.clock, progress)
	notifyProgress(o.status, progress)
	if o.status != nil {
		o.status.BeginLogEntry(string(progress))
		statusLog := newLogEntryWriter(o.status)
		defer statusLog.Flush()
		out = io.MultiWriter(out, statusLog)
	}
	// The invocation gets its own cancellable context, registered with the
	// task controller so the status page's Skip and Stop can kill just this
	// child process.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.control != nil {
		o.control.BeginTask(cancel)
	}
	if err := o.runner.Run(runCtx, inv, out); err != nil {
		fmt.Fprintf(out, "determined: tool invocation failed: %v\n", err)
		return err
	}
	return nil
}

// recordFailure decides what a failed invocation means for the run. An
// interruption stops immediately, since a cancelled context kills the child
// and surfaces as an error too. A genuine tool failure (rate limit, crash) is
// often transient, so the same invocation is retried until
// cfg.MaxConsecutiveFailures failures occur with no success in between.
func (o *PlanOrchestrator) recordFailure(ctx context.Context, err error) (models.Outcome, bool) {
	if ctx.Err() != nil {
		return models.OutcomeInterrupted, true
	}
	o.failures++
	if o.failures >= o.cfg.MaxConsecutiveFailures {
		fmt.Fprintf(o.terminal,
			"determined: tool invocation failed %d consecutive time(s); stopping: %v\n",
			o.failures, err)
		return models.OutcomeDroidFailed, true
	}
	fmt.Fprintf(o.terminal,
		"determined: tool invocation failed (%d of %d consecutive before aborting): %v; retrying\n",
		o.failures, o.cfg.MaxConsecutiveFailures, err)
	return models.OutcomeStopped, false // outcome ignored when stop is false
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
	writeProgress(o.terminal, o.clock, o.questionProgress())
	if o.status != nil {
		o.status.WaitForInput()
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

func (o *PlanOrchestrator) assessmentProgress() progressMessage {
	if o.cfg.Operation == models.PlanOperationReview {
		return "reviewing plan"
	}
	return "assessing plan"
}

func (o *PlanOrchestrator) questionProgress() progressMessage {
	if o.cfg.Operation == models.PlanOperationReview {
		return "answering review questions"
	}
	return "answering planning questions"
}

// planComplete reports whether every finished-plan file now exists: the plan,
// the step list, and the recommended journey/BDD tests.
func (o *PlanOrchestrator) planComplete() bool {
	return o.planDrafted() && o.files.Exists(o.cfg.TestsFile)
}

// planDrafted reports whether the plan and step list exist, regardless of
// whether the recommended tests were produced yet.
func (o *PlanOrchestrator) planDrafted() bool {
	return o.files.Exists(o.cfg.PlanFile) && o.files.Exists(o.cfg.StepsFile)
}

// ensureTests backfills the recommended journey/BDD tests when the plan and
// steps exist but the tests file is missing, so planning never completes
// without them. Every journey test must carry a mermaid sequence diagram; a
// tests file missing one triggers one regeneration pass before stalling.
// It reports whether the loop should stop.
func (o *PlanOrchestrator) ensureTests(ctx context.Context) (models.Outcome, bool) {
	for pass := 0; pass < 2; pass++ {
		if outcome, stop := o.produceTests(ctx); stop {
			return outcome, stop
		}
		missing, err := o.journeyTestsMissingDiagrams()
		if err != nil {
			fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.TestsFile, err)
			return models.OutcomePlanStalled, true
		}
		if len(missing) == 0 {
			return o.ensureAlignment(ctx)
		}
		if pass+1 >= 2 {
			break
		}
		fmt.Fprintf(o.terminal,
			"determined: %d journey test(s) missing a sequence diagram in %s; regenerating\n",
			len(missing), o.cfg.TestsFile)
		o.files.Remove(o.cfg.TestsFile)
	}
	fmt.Fprintf(o.terminal,
		"determined: journey tests in %s still lack sequence diagrams\n", o.cfg.TestsFile)
	return models.OutcomePlanStalled, true
}

// produceTests runs the tests invocation when the tests file is absent and
// verifies the tool produced it. It reports whether the loop should stop.
func (o *PlanOrchestrator) produceTests(ctx context.Context) (models.Outcome, bool) {
	if o.files.Exists(o.cfg.TestsFile) {
		return models.OutcomePlanReady, false
	}
	if outcome, stop := o.runInvocation(ctx, o.cfg.TestsInvocation, "recommending tests"); stop {
		return outcome, stop
	}
	if !o.files.Exists(o.cfg.TestsFile) {
		fmt.Fprintf(o.terminal, "determined: the tool did not produce %s\n", o.cfg.TestsFile)
		return models.OutcomePlanStalled, true
	}
	return models.OutcomePlanReady, false
}

// ensureAlignment makes every recommended test carry an alignment verdict
// judging it against the plan's functional goal, so the status page can colour
// each test by how well it proves that goal. Tests without a verdict trigger one
// alignment invocation; a still-missing verdict after it stalls the plan.
// It reports whether the loop should stop.
func (o *PlanOrchestrator) ensureAlignment(ctx context.Context) (models.Outcome, bool) {
	missing, err := o.testsMissingAlignment()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.TestsFile, err)
		return models.OutcomePlanStalled, true
	}
	if len(missing) == 0 {
		return models.OutcomePlanReady, false
	}
	if outcome, stop := o.runInvocation(ctx, o.cfg.AlignInvocation, "assessing test alignment"); stop {
		return outcome, stop
	}
	missing, err = o.testsMissingAlignment()
	if err != nil {
		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.TestsFile, err)
		return models.OutcomePlanStalled, true
	}
	if len(missing) > 0 {
		fmt.Fprintf(o.terminal,
			"determined: %d test(s) in %s still lack an alignment verdict\n", len(missing), o.cfg.TestsFile)
		return models.OutcomePlanStalled, true
	}
	return models.OutcomePlanReady, false
}

// testsMissingAlignment lists tests in the tests file with no alignment verdict.
func (o *PlanOrchestrator) testsMissingAlignment() ([]string, error) {
	content, err := o.files.Read(o.cfg.TestsFile)
	if err != nil {
		return nil, err
	}
	return NewTestsDocument(content).TestsMissingAlignment(), nil
}

// journeyTestsMissingDiagrams lists journey tests in the tests file that lack
// a mermaid sequence diagram.
func (o *PlanOrchestrator) journeyTestsMissingDiagrams() ([]string, error) {
	content, err := o.files.Read(o.cfg.TestsFile)
	if err != nil {
		return nil, err
	}
	return NewTestsDocument(content).JourneyTestsMissingDiagrams(), nil
}

// ServeAnnotations keeps the finished session responsive to page feedback: it
// applies queued annotations as they arrive until the user dismisses the page
// (Enter on the terminal) or the run is interrupted. Each annotation triggers
// one tool invocation that adjusts the referenced plan document.
func (o *PlanOrchestrator) ServeAnnotations(ctx context.Context, dismissed <-chan struct{}) {
	if o.status == nil {
		return
	}
	o.drainAnnotations(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-dismissed:
			return
		case <-o.status.AnnotationSignal():
			o.drainAnnotations(ctx)
		}
	}
}

// ServeFeedback keeps the finished session responsive like ServeAnnotations,
// and additionally returns true the moment the page's Implement button
// requests execution, letting the caller start the execute loop. Dismissal,
// interruption, or a missing reporter return false.
func (o *PlanOrchestrator) ServeFeedback(ctx context.Context, dismissed <-chan struct{}) bool {
	if o.status == nil {
		return false
	}
	o.drainAnnotations(ctx)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-dismissed:
			return false
		case <-o.status.ImplementSignal():
			return true
		case <-o.status.AnnotationSignal():
			o.drainAnnotations(ctx)
		}
	}
}

// drainAnnotations applies every queued annotation in arrival order.
func (o *PlanOrchestrator) drainAnnotations(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		annotation, ok := o.status.TakeAnnotation()
		if !ok {
			return
		}
		o.applyAnnotation(ctx, annotation)
	}
}

// applyAnnotation stages one annotation for the tool, runs the annotate
// invocation, and republishes the plan documents so the page shows the result.
// A goal annotation additionally rebuilds the plan, steps, and tests, since
// they were derived from the goal the annotation just changed.
func (o *PlanOrchestrator) applyAnnotation(ctx context.Context, annotation models.Annotation) {
	if err := o.files.Write(o.cfg.AnnotationFile, annotationDocument(annotation, o.cfg)); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not write %s: %v\n", o.cfg.AnnotationFile, err)
		return
	}
	if _, stop := o.runInvocation(ctx, o.cfg.AnnotateInvocation, "applying annotation"); stop {
		return
	}
	o.files.Remove(o.cfg.AnnotationFile)
	o.reportGoal()
	if annotation.Section == models.AnnotationSectionGoal {
		o.rebuildFromGoal(ctx)
		return
	}
	if annotation.Section == models.AnnotationSectionPlan {
		o.refreshDemo(ctx)
	}
	o.reportPlan()
}

// rebuildFromGoal discards the plan documents and demo derived from the
// previous goal, then regenerates them from the revised one.
func (o *PlanOrchestrator) rebuildFromGoal(ctx context.Context) {
	fmt.Fprintf(o.terminal,
		"determined: the goal changed; rebuilding %s, %s, and %s\n",
		o.cfg.PlanFile, o.cfg.StepsFile, o.cfg.TestsFile)
	o.files.Remove(o.cfg.PlanFile)
	o.files.Remove(o.cfg.StepsFile)
	o.files.Remove(o.cfg.TestsFile)
	if o.cfg.DemoFile != "" {
		o.status.SetDemo("")
		o.files.Remove(o.cfg.DemoFile)
	}
	if _, stop := o.runInvocation(ctx, o.cfg.Invocation, "replanning for the revised goal"); stop {
		return
	}
	if !o.planDrafted() {
		fmt.Fprintf(o.terminal,
			"determined: the tool did not rebuild %s and %s for the revised goal\n",
			o.cfg.PlanFile, o.cfg.StepsFile)
		o.reportPlan()
		return
	}
	if _, stop := o.ensureTests(ctx); stop {
		o.reportPlan()
		return
	}
	o.refreshDemo(ctx)
	o.reportPlan()
}

// annotationDocument renders one annotation as the markdown the annotate
// prompt expects, naming the section, its file, the finer target, and the
// user's requested adjustment.
func annotationDocument(annotation models.Annotation, cfg models.PlanConfig) string {
	var doc strings.Builder
	fmt.Fprintf(&doc, "# Annotation\n\n")
	fmt.Fprintf(&doc, "**Section:** %s (%s)\n\n", annotation.Section, annotation.Section.File(cfg))
	if annotation.Target != "" {
		fmt.Fprintf(&doc, "**Target:** %s\n\n", annotation.Target)
	}
	fmt.Fprintf(&doc, "**Requested adjustment:**\n\n%s\n", strings.TrimSpace(annotation.Comment))
	return doc.String()
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

// reportStart marks the planning phase start on the status page.
func (o *PlanOrchestrator) reportStart() {
	if o.status != nil {
		o.status.Start()
	}
}

// reportGoal publishes GOAL.md contents to the status page.
func (o *PlanOrchestrator) reportGoal() {
	if o.status == nil {
		return
	}
	o.docs.PublishGoal(o.status)
}

// reportPlan publishes the current PLAN.md contents and the parsed STEPS.md
// checkbox items to the status page.
func (o *PlanOrchestrator) reportPlan() {
	if o.status == nil {
		return
	}
	o.docs.PublishPlan(o.status)
}

// reportFinish records the planning phase end and success state.
func (o *PlanOrchestrator) reportFinish(outcome models.Outcome) {
	if o.status == nil {
		return
	}
	o.reportPlan()
	succeeded := planSucceeded(outcome)
	o.status.Finish(succeeded)
}
