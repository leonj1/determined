package models

import "time"

// Invocation is a single AI-coding-tool command the orchestrator runs.
type Invocation struct {
	Binary string
	Args   []string
}

// Config holds everything one orchestrator run needs.
type Config struct {
	StopFile  string
	PlanFile  string // must exist at startup; execute mode refuses to run without a plan
	StepsFile string
	Tool      Tool          // builds each iteration's invocation from the injected prompt
	Budget    time.Duration // wall-clock budget; 0 means unlimited
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
	// Verify runs an independent reviewer invocation after each newly checked
	// step, which unchecks the step (and records why in FIXES.md) when its
	// acceptance criterion is not genuinely met.
	Verify bool
	// GitCheckpoint commits the working tree after each step survives
	// verification, when the working directory is a git repository. Runs that
	// go wrong can then be rewound step by step.
	GitCheckpoint bool
	// CheckCmd, when non-empty, is a shell command (run via `sh -c`) that must
	// succeed after any iteration that newly checks a step, before the AI
	// verifier runs. On failure the newly checked steps are mechanically
	// unchecked and the command's output recorded in FIXES.md; the failure is
	// a verdict on the work, never a tool failure. Empty disables the gate.
	CheckCmd string
	// MaxReplans bounds how many times a run that hit the stall cap may spend
	// one invocation replanning the stuck step into smaller steps instead of
	// exiting stalled. Each attempt consumes one regardless of outcome, so a
	// replan that changes nothing cannot be retried forever. 0 disables
	// replanning; the run then stalls exactly as without it.
	MaxReplans int
	// StashAttempts git-stashes the working tree's rejected attempt once the
	// same step has been rejected a second time (by the check gate, verifier,
	// or audit), so the retry starts from the last verified checkpoint instead
	// of on top of repeatedly failed work. The stash's immutable hash and
	// diffstat are recorded in FIXES.md and the re-run prompt, and a step's
	// stashes are dropped once it finally passes. Needs GitCheckpoint, a git
	// repository, and a working tree that starts the run clean apart from the
	// protocol files; otherwise the run degrades to retrying in place.
	StashAttempts bool
	// LogDir is the per-iteration log directory; attempt stashes and the
	// startup cleanliness check exclude it alongside the protocol files.
	LogDir string
}
