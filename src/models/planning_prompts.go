package models

// PlanningPrompts contains the instructions used to create and refine a plan.
type PlanningPrompts struct {
	Plan     string
	Simplify string
	Demo     string
	Assess   string
	Refine   string
	Tests    string
	Align    string
	Realign  string
	Annotate string
}
