package models

// Outcome is the terminal reason a run ended.
type Outcome int

const (
	// OutcomeStopped means the AI tool signalled completion via the stop file.
	OutcomeStopped Outcome = iota
	// OutcomeDroidFailed means an invocation exited non-zero and the run aborted.
	OutcomeDroidFailed
	// OutcomeBudgetExceeded means the wall-clock budget was exhausted.
	OutcomeBudgetExceeded
	// OutcomeInterrupted means a signal (SIGINT/SIGTERM) ended the run.
	OutcomeInterrupted
	// OutcomePlanReady means a plan run produced PLAN.md and STEPS.md.
	OutcomePlanReady
	// OutcomePlanStalled means a plan iteration produced neither clarifying
	// questions nor a finished plan, so the loop could not make progress.
	OutcomePlanStalled
	// OutcomeCommitFailed means a task was marked complete, but committing the
	// resulting repository changes failed.
	OutcomeCommitFailed
)

// ExitCode maps an outcome to a process exit code: 0 only when the work
// completed cleanly (stop file created, or a plan was produced), 1 otherwise.
func (o Outcome) ExitCode() int {
	if o == OutcomeStopped || o == OutcomePlanReady {
		return 0
	}
	return 1
}

func (o Outcome) String() string {
	switch o {
	case OutcomeStopped:
		return "completed (stop file created)"
	case OutcomeDroidFailed:
		return "aborted (AI tool exited non-zero)"
	case OutcomeBudgetExceeded:
		return "stopped (time budget exhausted)"
	case OutcomeInterrupted:
		return "interrupted (signal received)"
	case OutcomePlanReady:
		return "plan ready (PLAN.md and STEPS.md created)"
	case OutcomePlanStalled:
		return "aborted (tool produced neither questions nor a plan)"
	case OutcomeCommitFailed:
		return "aborted (failed to commit completed task changes)"
	default:
		return "unknown"
	}
}
