package services

import (
	"context"
	"io"
	"time"

	"determined/src/models"
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
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.Config

	iteration int
}

// NewOrchestrator wires an orchestrator from its dependencies.
func NewOrchestrator(
	runner CommandRunner,
	stop StopSignal,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	cfg models.Config,
) *Orchestrator {
	return &Orchestrator{
		runner:   runner,
		stop:     stop,
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
	if err := o.runner.Run(ctx, o.cfg.Invocation, out); err != nil {
		return o.classifyFailure(ctx), true
	}
	return models.OutcomeStopped, false // outcome ignored when stop is false
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
