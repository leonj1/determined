package main

import (
	"context"
	"strings"

	"determined/src/models"
	"determined/src/services"
)

// postPlanAction says what an interactive session does after planning.
type postPlanAction int

// completedPageHolder keeps a finished interactive page available for review.
type completedPageHolder func(ctx context.Context)

type planDocumentPublisher interface {
	Publish(services.PlanDocumentSink)
}

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

// inputWaiter is the slice of the status page the approval gate needs: it
// surfaces the terminal wait so the browser user returns to the terminal.
// A nil waiter (headless chains without a page) disables the notice.
type inputWaiter interface {
	WaitForInput()
}

// approveExecution gates chained execution on explicit user consent: only a
// y/yes answer starts the execute loop. Anything else — bare Enter, any other
// text, or a failed read — declines, leaving the plan files intact.
func approveExecution(prompter services.Prompter, status inputWaiter) bool {
	if status != nil {
		status.WaitForInput()
	}
	answer, err := prompter.Ask("Plan approved — begin execution? [y/N]")
	if err != nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(answer))
	return normalized == "y" || normalized == "yes"
}

// runAutoExec starts execution with the live status reporter, then holds the
// completed page. The holder starts only after execution returns, so terminal
// input during execution cannot dismiss the finished page.
func runAutoExec(ctx context.Context, status services.ExecStatusReporter, execute planExecutor, hold completedPageHolder) models.Outcome {
	outcome := execute(ctx, status)
	hold(ctx)
	return outcome
}

// seedResumedSession presents protocol files as a completed planning phase.
func seedResumedSession(status *services.PlanStatusService, docs planDocumentPublisher) {
	status.Start()
	docs.Publish(status)
	status.Finish(true)
}
