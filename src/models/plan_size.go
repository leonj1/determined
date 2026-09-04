package models

// PlanSize is the planner's auditable estimate of a goal's delivery scope.
type PlanSize string

const (
	PlanSizeTrivial PlanSize = "trivial"
	PlanSizeSmall   PlanSize = "small"
	PlanSizeMedium  PlanSize = "medium"
	PlanSizeLarge   PlanSize = "large"
)
