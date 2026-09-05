package models

import "time"

// Invocation is a single AI-coding-tool command the orchestrator runs.
type Invocation struct {
	Binary string
	Args   []string
}

// Config holds everything one orchestrator run needs.
type Config struct {
	Milestones         bool
	MilestonesFile     string
	MilestoneStateFile string
	DivergenceFile     string
	MaxPlanRevisions   int
	MaxIntentRetries   int
	PlanTool           Tool
	StopFile           string
	PlanFile           string // must exist at startup; execute mode refuses to run without a plan
	StepsFile          string
	ExplanationFile    string
	QuizFile           string
	// ProtectedFiles lists the files that define the work's success criteria
	// (the plan, tests, and BDD criteria) and therefore must not change during
	// execution. Any modification a tool invocation makes to one of them is
	// reverted and recorded. The steps file must not be listed: the tool checks
	// boxes there by design.
	ProtectedFiles []string
	Tool           Tool          // builds each iteration's invocation from the injected prompt
	Budget         time.Duration // wall-clock budget; 0 means unlimited
	// MaxStalledIterations ends the run with OutcomeStalled after this many
	// consecutive iterations complete without a newly checked step; 0 disables
	// stall detection.
	MaxStalledIterations int
	// MaxConsecutiveFailures ends the run with OutcomeDroidFailed after this
	// many consecutive failed tool invocations; any success resets the count.
	// Values <= 1 abort on the first failure (no retries).
	MaxConsecutiveFailures int
	// MaxIterationDuration bounds a single tool invocation; one that runs
	// longer is killed and counts as a failed invocation toward
	// MaxConsecutiveFailures. 0 means unlimited.
	MaxIterationDuration time.Duration
	// StepMaxRuntime bounds the cumulative wall-clock time the loop spends on
	// a single step across its invocations. Like Budget it is checked between
	// invocations, so a running invocation always finishes first; a step still
	// unchecked past the cap ends the run with OutcomeStepTimeout. 0 means
	// unlimited.
	StepMaxRuntime time.Duration
	// RemediationBudget bounds every review-caused retry from one shared pool.
	// A negative value selects the plan-size default; zero permits no remediation.
	RemediationBudget int
	// Verify runs an independent correctness reviewer after each newly checked
	// step. The reviewer unchecks the step and records why in FIXES.md when its
	// acceptance criterion is not genuinely met.
	Verify bool
	// SimplicityReviews adds an opt-in simplicity reviewer before each step's
	// correctness review. Planning already performs one whole-plan simplicity
	// pass, so this extra per-step invocation defaults off.
	SimplicityReviews bool
	// SpecializedReviews runs independent security, performance, and
	// reliability/maintainability reviews before the final whole-plan audit.
	// A reviewer can reopen or add a remediation step in STEPS.md.
	SpecializedReviews bool
	// GitCheckpoint commits the working tree after each step survives
	// verification, when the working directory is a git repository. Runs that
	// go wrong can then be rewound step by step.
	GitCheckpoint bool
	// TieBreaker runs an independent AI invocation to break a coder/verifier
	// deadlock when MaxStalledIterations is hit. The tie-breaker evaluates the
	// step, goal, implementation, and verifier rejections, then chooses the
	// objectively correct answer or the most reasonable side. Its verdict is
	// final: the coder implements it without further verification.
	TieBreaker bool
}
