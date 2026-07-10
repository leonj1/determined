package models

// PlanOperation identifies the attended planning workflow to run.
type PlanOperation string

const (
	PlanOperationCreate PlanOperation = "create"
	PlanOperationReview PlanOperation = "review"
)
