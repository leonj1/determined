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
	stepLinePattern          = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)]\s+)(?:\[[ xX]\]\s*)?\S`)
	completedStepLinePattern = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)]\s+)\[[xX]\]\s+\S`)
)

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

// StepReader reads the step list used for progress labels.
type StepReader interface {
	Read(path string) (string, error)
}

// Clock reads wall-clock time.
type Clock interface {
	Now() time.Time
}

// LogSink opens a fresh, closable log writer for each iteration.
type LogSink interface {
	OpenIteration(iteration int) (io.WriteCloser, error)
}

// Orchestrator runs the AI coding tool in a loop until it signals completion,
// an invocation fails, the time budget is exhausted, or a signal interrupts it.
type Orchestrator struct {
	runner   CommandRunner
	stop     StopSignal
	commits  ChangeCommitter
	steps    StepReader
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.Config

	iteration         int
	completedDuration time.Duration
}

// NewOrchestrator wires an orchestrator from its dependencies.
func NewOrchestrator(
	runner CommandRunner,
	stop StopSignal,
	commits ChangeCommitter,
	steps StepReader,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.Config,
) *Orchestrator {
	return &Orchestrator{
		runner:   runner,
		stop:     stop,
		commits:  commits,
		steps:    steps,
		clock:    clock,
		logs:     logs,
		terminal: terminal,
		cfg:      cfg,
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
	progressBefore, beforeOK := o.stepProgress()
	step := o.nextStep(progressBefore)
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
	if err := o.commitCompletedTask(ctx, beforeOK, progressBefore, out); err != nil {
		return models.OutcomeCommitFailed, true
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

func (o *Orchestrator) commitCompletedTask(ctx context.Context, beforeOK bool, before stepProgress, out io.Writer) error {
	after, afterOK := o.stepProgress()
	if !beforeOK || !afterOK || after.completed <= before.completed {
		return nil
	}
	fmt.Fprintln(out, "Committing completed task changes")
	return o.commits.Commit(ctx, out)
}

func (o *Orchestrator) stepProgress() (stepProgress, bool) {
	if o.steps == nil || o.cfg.StepsFile == "" {
		return stepProgress{}, false
	}
	content, err := o.steps.Read(o.cfg.StepsFile)
	if err != nil {
		return stepProgress{}, false
	}
	return parseStepProgress(content), true
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

func parseStepProgress(content string) stepProgress {
	progress := stepProgress{}
	for _, line := range strings.Split(content, "\n") {
		if !stepLinePattern.MatchString(line) {
			continue
		}
		progress.total++
		if completedStepLinePattern.MatchString(line) {
			progress.completed++
		}
	}
	return progress
}

func formatDuration(d time.Duration) string {
	rounded := d.Round(time.Second)
	if rounded == 0 && d > 0 {
		return time.Second.String()
	}
	return rounded.String()
}
