package main

import (
	"context"

	"determined/src/models"
	"determined/src/services"
)

// postPlanAction says what an interactive session does after planning.
type postPlanAction int

// completedPageHolder keeps a finished interactive page available for review.
type completedPageHolder func(ctx context.Context)

const (
	postPlanDismiss postPlanAction = iota
	postPlanOffer
	postPlanAutoExec
)

// postPlanActionFor selects the only valid follow-on for a planning outcome.
func postPlanActionFor(executing bool, outcome models.Outcome) postPlanAction {
	if outcome != models.OutcomePlanReady {
		return postPlanDismiss
	}
	if executing {
		return postPlanAutoExec
	}
	return postPlanOffer
}

// runAutoExec starts execution with the live status reporter, then holds the
// completed page. The holder starts only after execution returns, so terminal
// input during execution cannot dismiss the finished page.
func runAutoExec(ctx context.Context, status services.ExecStatusReporter, execute planExecutor, hold completedPageHolder) models.Outcome {
	outcome := execute(ctx, status)
	hold(ctx)
	return outcome
}
