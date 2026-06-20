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
)

// ExitCode maps an outcome to a process exit code: 0 only when the AI tool
// signalled completion via the stop file, 1 for every other termination.
func (o Outcome) ExitCode() int {
	if o == OutcomeStopped {
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
	default:
		return "unknown"
	}
}
