package models

// PlanningPrompts contains the instructions used to create and refine a plan.
type PlanningPrompts struct {
	Plan     string
	Assess   string
	Refine   string
	Tests    string
	Annotate string
}
