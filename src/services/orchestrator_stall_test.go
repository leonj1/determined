package services_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// fakeStallResolver stands in for the status page's tiebreak modal. It records
// the step title each stall presents and returns a scripted verdict for each
// call, so a test can drive the resolved loop deterministically.
type fakeStallResolver struct {
	verdicts []models.StallGuidance
	calls    int
	titles   []string
	prompts  []models.StallPrompt
}

func (r *fakeStallResolver) AwaitStallChoice(_ context.Context, prompt models.StallPrompt) models.StallGuidance {
	r.titles = append(r.titles, prompt.StepTitle)
	r.prompts = append(r.prompts, prompt)
	i := r.calls
	r.calls++
	if i < len(r.verdicts) {
		return r.verdicts[i]
	}
	return models.StallGuidance{Decision: models.StallDecisionCancel}
}

func stalledConfig() models.Config {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	return cfg
}

func TestStallCancelStopsTheRunAsBefore(t *testing.T) {
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionCancel},
	}}
	runner := &fakeRunner{} // never checks a step
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stalledConfig()).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("cancel should stop the run with OutcomeStalled, got %v", outcome)
	}
	if resolver.calls != 1 {
		t.Fatalf("expected exactly one tiebreak prompt before cancel, got %d", resolver.calls)
	}
	if resolver.titles[0] != "1. Add the widget." {
		t.Fatalf("expected the stalled step's title in the prompt, got %q", resolver.titles[0])
	}
	opts := resolver.prompts[0].Options
	if len(opts) != 2 {
		t.Fatalf("expected exactly two side-by-side recommendations, got %d", len(opts))
	}
	if opts[0].Decision != models.StallDecisionAcceptWorker || opts[1].Decision != models.StallDecisionHoldReviewer {
		t.Fatalf("expected accept-worker then hold-reviewer, got %q then %q", opts[0].Decision, opts[1].Decision)
	}
	for _, opt := range opts {
		if opt.Title == "" || opt.Synopsis == "" {
			t.Fatalf("every recommendation needs a title and synopsis, got %+v", opt)
		}
		if !strings.Contains(opt.Synopsis, "1. Add the widget.") {
			t.Fatalf("synopsis should name the stalled step, got %q", opt.Synopsis)
		}
	}
}

func TestStallAcceptWorkerChecksStepAndResumes(t *testing.T) {
	fs := stepsFileStore()
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionAcceptWorker}, // check step 1, resume
		{Decision: models.StallDecisionCancel},       // then stall on step 2, cancel out
	}}
	runner := &fakeRunner{} // never checks a step itself
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stalledConfig()).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the second stall to cancel out, got %v", outcome)
	}
	steps, _ := fs.Read("STEPS.md")
	if !strings.Contains(steps, "- [x] 1. Add the widget.") {
		t.Fatalf("accept-worker should have checked step 1, got:\n%s", steps)
	}
	if strings.Contains(steps, "- [x] 2. Document the widget.") {
		t.Fatalf("only the stalled step should be checked, not step 2:\n%s", steps)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected a fresh stall after resuming (counter reset), got %d prompts", resolver.calls)
	}
	if resolver.titles[1] != "2. Document the widget." {
		t.Fatalf("second prompt should name the now-stalled step 2, got %q", resolver.titles[1])
	}
}

func TestStallHoldForReviewerRetriesWithoutChecking(t *testing.T) {
	fs := stepsFileStore()
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionHoldReviewer}, // leave unchecked, resume
		{Decision: models.StallDecisionCancel},
	}}
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stalledConfig()).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected an eventual cancel stop, got %v", outcome)
	}
	steps, _ := fs.Read("STEPS.md")
	if strings.Contains(steps, "[x]") {
		t.Fatalf("hold-for-reviewer must not check any step, got:\n%s", steps)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected the counter to reset and stall again, got %d prompts", resolver.calls)
	}
}

func TestStallOtherQueuesGuidanceToNotes(t *testing.T) {
	fs := stepsFileStore()
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionOther, Comment: "Skip the SQLite check for now."},
		{Decision: models.StallDecisionCancel},
	}}
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stalledConfig()).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected an eventual cancel stop, got %v", outcome)
	}
	notes, err := fs.Read("NOTES.md")
	if err != nil {
		t.Fatalf("other should have written NOTES.md: %v", err)
	}
	if !strings.Contains(notes, "Skip the SQLite check for now.") {
		t.Fatalf("expected the freehand guidance in NOTES.md, got:\n%s", notes)
	}
}

func TestStallWithoutResolverStopsImmediately(t *testing.T) {
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stalledConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("a nil resolver keeps the terminal stop behavior, got %v", outcome)
	}
	if runner.calls != 2 {
		t.Fatalf("expected the run to end after the stall cap without pausing, got %d", runner.calls)
	}
}
