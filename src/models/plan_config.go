package models

import "time"

// PlanConfig holds everything one interactive planning run needs. Planning is
// attended: the tool asks clarifying questions via QuestionsFile, determined
// relays them to the user and records the replies in AnswersFile, and the run
// completes once the tool has written both PlanFile and StepsFile.
//
// Once a plan exists, determined runs a quality-refinement loop. It checks plan
// completeness, task-template coverage, step size and acceptance criteria,
// then resolves findings until the plan passes or MaxRefinePasses is reached.
type PlanConfig struct {
	Goal       string        // the user's goal or a file reference used to seed GoalFile
	Invocation Invocation    // the planning tool command (print mode) run each round
	Budget     time.Duration // wall-clock budget; 0 means unlimited

	AssessInvocation Invocation // reviews plan and step quality, writing AssessmentFile
	RefineInvocation Invocation // resolves assessment findings in PlanFile and StepsFile
	MaxRefinePasses  int        // cap on assess/refine rounds; 0 disables refinement

	GoalFile       string // where the goal is written (GOAL.md)
	QuestionsFile  string // where the tool writes clarifying questions (QUESTIONS.md)
	AnswersFile    string // where determined appends the Q&A history (ANSWERS.md)
	PlanFile       string // a finished plan output (PLAN.md)
	StepsFile      string // a finished step list output (STEPS.md)
	AssessmentFile string // where the assessor lists planning issues (REFINEMENTS.md)
}
