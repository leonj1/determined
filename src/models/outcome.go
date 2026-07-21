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
	// OutcomeMissingFiles means execute mode started without the protocol
	// files a run needs (PLAN.md / STEPS.md), so no tool was ever invoked.
	OutcomeMissingFiles
	// OutcomeStalled means too many consecutive iterations completed without
	// a newly checked step, so the run ended instead of looping forever.
	OutcomeStalled
	// OutcomeInvalidGoal means plan mode received an empty or incomplete goal,
	// so no tool was invoked.
	OutcomeInvalidGoal
	// OutcomePlanReviewed means review mode assessed and revised an existing plan.
	OutcomePlanReviewed
	// OutcomeCriteriaReady means a criteria session recorded accepted BDD tests.
	OutcomeCriteriaReady
	// OutcomeCriteriaCancelled means the user ended a criteria session without
	// keeping any tests, restoring the criteria file to its pre-session state.
	OutcomeCriteriaCancelled
	// OutcomeCriteriaStalled means the tool produced no BDD test draft, so the
	// criteria session could not make progress.
	OutcomeCriteriaStalled
	// OutcomeStepTimeout means a single step's cumulative runtime exceeded the
	// per-step cap, so the run stopped instead of grinding on one step forever.
	OutcomeStepTimeout
)

// ExitCode maps an outcome to a process exit code: 0 only when the work
// completed cleanly (stop file created, or a plan was produced), 3 when the
// run stalled without progress, 1 for every other termination. Stalling gets
// its own code so callers can tell "stuck" apart from "failed".
func (o Outcome) ExitCode() int {
	switch o {
	case OutcomeStopped, OutcomePlanReady, OutcomePlanReviewed,
		OutcomeCriteriaReady, OutcomeCriteriaCancelled:
		return 0
	case OutcomeStalled:
		return 3
	default:
		return 1
	}
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
	case OutcomeMissingFiles:
		return "aborted (required protocol file missing)"
	case OutcomeStalled:
		return "stalled (consecutive iterations checked no new step)"
	case OutcomeInvalidGoal:
		return "aborted (planning goal is empty or incomplete)"
	case OutcomePlanReviewed:
		return "plan reviewed (PLAN.md and STEPS.md ready)"
	case OutcomeCriteriaReady:
		return "criteria ready (accepted BDD tests recorded in CRITERIA.md)"
	case OutcomeCriteriaCancelled:
		return "criteria cancelled (no BDD tests kept from this session)"
	case OutcomeCriteriaStalled:
		return "aborted (tool produced no BDD test draft)"
	case OutcomeStepTimeout:
		return "stopped (a single step exceeded its max runtime)"
	default:
		return "unknown"
	}
}
