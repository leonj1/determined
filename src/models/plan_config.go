package models

import "time"

// PlanConfig holds everything one interactive planning run needs. Planning is
// attended: the tool asks clarifying questions via QuestionsFile, determined
// relays them to the user and records the replies in AnswersFile, and the run
// completes once the tool has written both PlanFile and StepsFile.
//
// Once a plan exists, determined runs a granularity-refinement loop: it asks the
// tool to assess whether each step is small enough to implement in one pass
// (writing oversized steps to OversizedFile) and, if any are too large, to break
// them down further — repeating until every step is small enough, the budget is
// exhausted, or MaxRefinePasses is reached.
type PlanConfig struct {
	Goal       string        // the user's raw goal, written to GoalFile each run
	Invocation Invocation    // the planning tool command (print mode) run each round
	Budget     time.Duration // wall-clock budget; 0 means unlimited

	AssessInvocation    Invocation // judges step granularity, writing OversizedFile
	BreakdownInvocation Invocation // breaks oversized steps down, rewriting StepsFile
	MaxRefinePasses     int        // cap on assess/breakdown rounds; 0 disables refinement

	GoalFile      string // where the goal is written (GOAL.md)
	QuestionsFile string // where the tool writes clarifying questions (QUESTIONS.md)
	AnswersFile   string // where determined appends the Q&A history (ANSWERS.md)
	PlanFile      string // a finished plan output (PLAN.md)
	StepsFile     string // a finished step list output (STEPS.md)
	OversizedFile string // where the assessor lists too-large steps (OVERSIZED.md)
}
