package models

// PlanMode controls how much certainty the attended planner requires before it
// produces an executable plan.
type PlanMode string

const (
	PlanModeStandard  PlanMode = "standard"
	PlanModeMVP       PlanMode = "mvp"
	PlanModePrototype PlanMode = "prototype"
)
