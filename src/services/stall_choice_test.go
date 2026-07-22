package services_test

import (
	"context"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func newStallService() *services.PlanStatusService {
	return services.NewPlanStatusService(&fakeClock{now: time.Now()}, models.GitContext{}, models.ToolIdentity{})
}

func TestAwaitStallChoiceReturnsSubmittedGuidance(t *testing.T) {
	svc := newStallService()
	prompt := models.StallPrompt{
		StepTitle: "2. Verify migrations",
		Options: []models.StallOption{
			{Decision: models.StallDecisionAcceptWorker, Title: "Accept", Synopsis: "trust the worker"},
			{Decision: models.StallDecisionHoldReviewer, Title: "Hold", Synopsis: "trust the reviewer"},
		},
	}
	got := make(chan models.StallGuidance, 1)
	go func() {
		got <- svc.AwaitStallChoice(context.Background(), prompt)
	}()

	// Wait until the run has parked and published the modal flag, then submit.
	waitFor(t, func() bool { return svc.Snapshot().AwaitingStallChoice })
	snap := svc.Snapshot()
	if snap.StallChoicePrompt != "2. Verify migrations" {
		t.Fatalf("expected the step title published to the page, got %q", snap.StallChoicePrompt)
	}
	if len(snap.StallChoiceOptions) != 2 || snap.StallChoiceOptions[0].Title != "Accept" {
		t.Fatalf("expected both recommendations published to the page, got %+v", snap.StallChoiceOptions)
	}
	if !svc.SubmitStallChoice(models.StallDecisionOther, "hold on the SQLite job") {
		t.Fatal("submit should report a pending wait")
	}

	guidance := <-got
	if guidance.Decision != models.StallDecisionOther || guidance.Comment != "hold on the SQLite job" {
		t.Fatalf("expected the exact submitted guidance, got %+v", guidance)
	}
	if snap := svc.Snapshot(); snap.AwaitingStallChoice || snap.StallChoicePrompt != "" || snap.StallChoiceOptions != nil {
		t.Fatalf("the modal flag should be cleared after a choice, got %+v", snap)
	}
}

func TestAwaitStallChoiceCancelsOnContextDone(t *testing.T) {
	svc := newStallService()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan models.StallGuidance, 1)
	go func() { got <- svc.AwaitStallChoice(ctx, models.StallPrompt{StepTitle: "step"}) }()

	waitFor(t, func() bool { return svc.Snapshot().AwaitingStallChoice })
	cancel()

	guidance := <-got
	if guidance.Decision != models.StallDecisionCancel {
		t.Fatalf("a cancelled run should yield StallDecisionCancel, got %+v", guidance)
	}
	if svc.Snapshot().AwaitingStallChoice {
		t.Fatal("the modal flag should be cleared after cancellation")
	}
}

func TestSubmitStallChoiceReportsFalseWhenNoWaitPending(t *testing.T) {
	svc := newStallService()
	if svc.SubmitStallChoice(models.StallDecisionAcceptWorker, "") {
		t.Fatal("submit should report false when no run is parked")
	}
}

// waitFor polls cond until true or the deadline passes, so a test never wedges
// on a state that never arrives.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
