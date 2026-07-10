package models

import "time"

// PlanConfig holds everything one attended plan operation needs. During plan
// creation or review, the tool asks questions via QuestionsFile, determined
// relays them to the user, and records the replies in AnswersFile.
//
// Once a plan exists, determined checks its quality and resolves findings until
// it passes or MaxRefinePasses is reached.
type PlanConfig struct {
	Operation  PlanOperation // create a plan or review an existing one
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
