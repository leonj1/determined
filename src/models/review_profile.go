package models

// ReviewProfile is the deterministic review policy recorded in PLAN.md.
type ReviewProfile struct {
	Size                PlanSize
	TaskTypes           []string
	RiskTags            []string
	RequiredSpecialists []string
}

// RemediationBudget is the shared number of review-caused retries remaining.
type RemediationBudget int
