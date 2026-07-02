package services

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"determined/src/models"
)

var (
	stepPartsPattern = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)]\s+)(?:\[([ xX])\]\s*)?(.*\S)\s*$`)
)

const defaultMaxVerificationRetries = 3

// CommandRunner runs one AI-coding-tool invocation, streaming its combined
// stdout and stderr to out. The real implementation is clients.ExecCommandRunner.
type CommandRunner interface {
	Run(ctx context.Context, inv models.Invocation, out io.Writer) error
}

// StopSignal reports whether the completion sentinel file exists.
type StopSignal interface {
	Exists(path string) bool
}

// ChangeCommitter commits repository changes created by a completed task.
type ChangeCommitter interface {
	Commit(ctx context.Context, out io.Writer) error
}

// ChangeVerifier approves or rejects repository changes for a completed step.
type ChangeVerifier interface {
	Verify(ctx context.Context, step models.Step, out io.Writer) (models.VerificationResult, error)
}

// StepStore manages protocol files used by the execute loop.
type StepStore interface {
	Read(path string) (string, error)
	Write(path, content string) error
	Remove(path string) error
}

// Clock reads wall-clock time.
type Clock interface {
	Now() time.Time
}

// LogSink opens a fresh, closable log writer for each iteration.
type LogSink interface {
	OpenIteration(iteration int) (io.WriteCloser, error)
}

// VerificationLogSink optionally writes verifier output to a separate log.
type VerificationLogSink interface {
	OpenVerification(iteration int) (io.WriteCloser, error)
}

// Orchestrator runs the AI coding tool in a loop until it signals completion,
// an invocation fails, the time budget is exhausted, or a signal interrupts it.
type Orchestrator struct {
	runner   CommandRunner
	stop     StopSignal
	commits  ChangeCommitter
	verifier ChangeVerifier
	steps    StepStore
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.Config

	iteration         int
	completedDuration time.Duration
	verifyRetries     map[int]int
}

// NewOrchestrator wires an orchestrator from its dependencies.
func NewOrchestrator(
	runner CommandRunner,
	stop StopSignal,
	commits ChangeCommitter,
	steps StepStore,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.Config,
) *Orchestrator {
	return NewVerifiedOrchestrator(runner, stop, commits, nil, steps, clock, logs, terminal, cfg)
}

// NewVerifiedOrchestrator wires an orchestrator with a completed-step verifier.
func NewVerifiedOrchestrator(
	runner CommandRunner,
	stop StopSignal,
	commits ChangeCommitter,
	verifier ChangeVerifier,
	steps StepStore,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.Config,
) *Orchestrator {
	return &Orchestrator{
		runner:        runner,
		stop:          stop,
		commits:       commits,
		verifier:      verifier,
		steps:         steps,
		clock:         clock,
		logs:          logs,
		terminal:      terminal,
		cfg:           cfg,
		verifyRetries: map[int]int{},
	}
}

// Run executes the loop and returns the terminal outcome.
func (o *Orchestrator) Run(ctx context.Context) models.Outcome {
	deadline := o.deadline()
	for {
		if outcome, stop := o.preIteration(ctx, deadline); stop {
			return outcome
		}
		if outcome, stop := o.runOnce(ctx); stop {
			return outcome
		}
	}
}

// preIteration applies the between-iteration guards before starting more work.
// The budget is checked here, so a running invocation always finishes first.
func (o *Orchestrator) preIteration(ctx context.Context, deadline time.Time) (models.Outcome, bool) {
	switch {
	case ctx.Err() != nil:
		return models.OutcomeInterrupted, true
	case o.stop.Exists(o.cfg.StopFile):
		return models.OutcomeStopped, true
	case o.budgetExceeded(deadline):
		return models.OutcomeBudgetExceeded, true
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

// runOnce runs a single invocation, teeing its output to the terminal and a
// per-iteration log. It reports whether the loop should stop.
func (o *Orchestrator) runOnce(ctx context.Context) (models.Outcome, bool) {
	o.iteration++
	log, err := o.logs.OpenIteration(o.iteration)
	if err != nil {
		return models.OutcomeDroidFailed, true
	}
	defer log.Close()
	out := io.MultiWriter(o.terminal, log)
	before, beforeOK := o.stepSnapshot()
	step := o.nextStep(before.progress)
	fmt.Fprintln(out, step.Started())
	started := o.clock.Now()
	if err := o.runner.Run(ctx, o.cfg.Invocation, out); err != nil {
		return o.classifyFailure(ctx), true
	}
	o.completedDuration += o.clock.Now().Sub(started)
	fmt.Fprintln(out, step.Completed())
	if eta, ok := o.eta(step); ok {
		fmt.Fprintf(out, "ETA: %s remaining (%s)\n", formatDuration(eta), step.RemainingText())
	}
	if outcome, stop := o.finishCompletedTask(ctx, beforeOK, before, out); stop {
		return outcome, true
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
}

func (o *Orchestrator) nextStep(progress stepProgress) stepRun {
	number := o.iteration
	if progress.completed+1 > number {
		number = progress.completed + 1
	}
	if progress.total > 0 && number > progress.total {
		number = progress.total
	}
	return stepRun{number: number, total: progress.total}
}

func (o *Orchestrator) finishCompletedTask(
	ctx context.Context,
	beforeOK bool,
	before stepSnapshot,
	out io.Writer,
) (models.Outcome, bool) {
	after, afterOK := o.stepSnapshot()
	if !beforeOK || !afterOK {
		return models.OutcomeStopped, false
	}
	if !before.hasIncomplete {
		return models.OutcomeStopped, false
	}
	if !after.stepCompleted(before.incomplete.Number) {
		if after.progress.completed <= before.progress.completed {
			return models.OutcomeStopped, false
		}
		reason := "The coding run marked a different STEPS.md item complete. Rework the intended step instead."
		return o.rejectCompletedTask(ctx, before, reason, out)
	}
	outcome, stop, approved := o.verifyCompletedTask(ctx, before, out)
	if stop {
		return outcome, true
	}
	if !approved {
		return models.OutcomeStopped, false
	}
	if err := o.clearVerificationFeedback(); err != nil {
		return models.OutcomeCommitFailed, true
	}
	fmt.Fprintln(out, "Committing completed task changes")
	if err := o.commits.Commit(ctx, out); err != nil {
		return models.OutcomeCommitFailed, true
	}
	return models.OutcomeStopped, false
}

func (o *Orchestrator) verifyCompletedTask(
	ctx context.Context,
	before stepSnapshot,
	out io.Writer,
) (models.Outcome, bool, bool) {
	if o.verifier == nil {
		return models.OutcomeStopped, false, true
	}
	fmt.Fprintf(out, "Verifying Step %d\n", before.incomplete.Number)
	verifyOut, closeLog, err := o.verifierOutput()
	if err != nil {
		return models.OutcomeVerificationFailed, true, false
	}
	defer closeLog()
	result, err := o.verifier.Verify(ctx, before.incomplete, verifyOut)
	if err != nil {
		return o.classifyFailure(ctx), true, false
	}
	if result.Passed() {
		fmt.Fprintf(out, "Verified Step %d\n", before.incomplete.Number)
		o.verifyRetries[before.incomplete.Number] = 0
		return models.OutcomeStopped, false, true
	}
	outcome, stop := o.rejectCompletedTask(ctx, before, result.Feedback, out)
	return outcome, stop, false
}

func (o *Orchestrator) rejectCompletedTask(
	ctx context.Context,
	before stepSnapshot,
	feedback string,
	out io.Writer,
) (models.Outcome, bool) {
	o.verifyRetries[before.incomplete.Number]++
	if o.verifyRetries[before.incomplete.Number] > o.maxVerificationRetries() {
		fmt.Fprintf(out, "Verification failed for Step %d:\n%s\n", before.incomplete.Number, feedback)
		return models.OutcomeVerificationFailed, true
	}
	if err := o.restoreTaskForRepair(ctx, before, feedback); err != nil {
		fmt.Fprintf(out, "Could not prepare Step %d for repair: %v\n", before.incomplete.Number, err)
		return models.OutcomeVerificationFailed, true
	}
	fmt.Fprintf(out, "Verification rejected Step %d; resubmitting with feedback\n", before.incomplete.Number)
	return models.OutcomeStopped, false
}

func (o *Orchestrator) restoreTaskForRepair(ctx context.Context, before stepSnapshot, feedback string) error {
	if err := o.steps.Write(o.cfg.StepsFile, before.content); err != nil {
		return err
	}
	if o.cfg.VerificationFeedbackFile != "" {
		if err := o.steps.Write(o.cfg.VerificationFeedbackFile, feedbackFileContent(before.incomplete, feedback)); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return o.steps.Remove(o.cfg.StopFile)
}

func (o *Orchestrator) stepSnapshot() (stepSnapshot, bool) {
	if o.steps == nil || o.cfg.StepsFile == "" {
		return stepSnapshot{}, false
	}
	content, err := o.steps.Read(o.cfg.StepsFile)
	if err != nil {
		return stepSnapshot{}, false
	}
	return parseStepSnapshot(content), true
}

func (o *Orchestrator) eta(step stepRun) (time.Duration, bool) {
	if step.total == 0 {
		return 0, false
	}
	average := o.completedDuration / time.Duration(o.iteration)
	return average * time.Duration(step.Remaining()), true
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

func (o *Orchestrator) verifierOutput() (io.Writer, func(), error) {
	sink, ok := o.logs.(VerificationLogSink)
	if !ok {
		return o.terminal, func() {}, nil
	}
	log, err := sink.OpenVerification(o.iteration)
	if err != nil {
		return nil, func() {}, err
	}
	return io.MultiWriter(o.terminal, log), func() { log.Close() }, nil
}

func (o *Orchestrator) clearVerificationFeedback() error {
	if o.cfg.VerificationFeedbackFile == "" {
		return nil
	}
	return o.steps.Remove(o.cfg.VerificationFeedbackFile)
}

func (o *Orchestrator) maxVerificationRetries() int {
	if o.cfg.MaxVerificationRetries > 0 {
		return o.cfg.MaxVerificationRetries
	}
	return defaultMaxVerificationRetries
}

type stepRun struct {
	number int
	total  int
}

func (s stepRun) Started() string {
	if s.total == 0 {
		return fmt.Sprintf("Starting Step %d", s.number)
	}
	return fmt.Sprintf("Starting Step %d of %d", s.number, s.total)
}

func (s stepRun) Completed() string {
	return fmt.Sprintf("Completed Step %d", s.number)
}

func (s stepRun) Remaining() int {
	if s.total == 0 || s.number >= s.total {
		return 0
	}
	return s.total - s.number
}

func (s stepRun) RemainingText() string {
	if s.Remaining() == 1 {
		return "1 step left"
	}
	return fmt.Sprintf("%d steps left", s.Remaining())
}

type stepProgress struct {
	total     int
	completed int
}

type stepSnapshot struct {
	content       string
	progress      stepProgress
	incomplete    models.Step
	hasIncomplete bool
	completed     map[int]bool
}

func (s stepSnapshot) stepCompleted(number int) bool {
	return s.completed[number]
}

func parseStepSnapshot(content string) stepSnapshot {
	snapshot := stepSnapshot{content: content, completed: map[int]bool{}}
	for _, line := range strings.Split(content, "\n") {
		snapshot = snapshot.withStepLine(line)
	}
	return snapshot
}

func (s stepSnapshot) withStepLine(line string) stepSnapshot {
	parts := stepPartsPattern.FindStringSubmatch(line)
	if parts == nil {
		return s
	}
	s.progress.total++
	number := s.progress.total
	completed := strings.EqualFold(parts[1], "x")
	if completed {
		s.progress.completed++
		s.completed[number] = true
	}
	if !completed && !s.hasIncomplete {
		s.incomplete = models.Step{Number: number, Text: strings.TrimSpace(parts[2])}
		s.hasIncomplete = true
	}
	return s
}

func feedbackFileContent(step models.Step, feedback string) string {
	return fmt.Sprintf("# Verification feedback\n\nStep %d: %s\n\n%s\n", step.Number, step.Text, feedback)
}

func formatDuration(d time.Duration) string {
	rounded := d.Round(time.Second)
	if rounded == 0 && d > 0 {
		return time.Second.String()
	}
	return rounded.String()
}
