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

// --- Tie-breaker tests ---

// tieBreakerConfig returns a config with the AI tie-breaker enabled and no
// human resolver, so the tie-breaker is the only stall resolution path.
func tieBreakerConfig() models.Config {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.TieBreaker = true
	return cfg
}

// tieBreakerAcceptOutput is the output the tool writes when the tie-breaker
// sides with the worker.
const tieBreakerAcceptOutput = "VERDICT: ACCEPT\nRATIONALE: The acceptance criterion is clearly met.\n"

// tieBreakerRejectOutput is the output the tool writes when the tie-breaker
// sides with the verifier.
const tieBreakerRejectOutput = "VERDICT: REJECT\nRATIONALE: The implementation fails the criterion.\nGUIDANCE: Add error handling around the file read.\n"

func TestTieBreakerAcceptChecksStepAndResumesWithoutVerification(t *testing.T) {
	cfg := tieBreakerConfig()
	cfg.Verify = true
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		// calls 1-2: work invocations that check nothing (stall builds)
		// call 3: tie-breaker invocation (writes ACCEPT verdict)
		// call 4: work resumes, checks step 1
		// call 5: work checks step 2
		// call 6: docs update
		// call 7: audit
		switch call {
		case 3:
			io.WriteString(out, tieBreakerAcceptOutput)
		case 4:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 7:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion after tie-breaker resolved the stall, got %v", outcome)
	}
	// Tie-breaker check: step 1 should be checked and NOT have triggered a
	// verifier (no simplicity or correctness review calls).
	if runner.calls != 7 {
		t.Fatalf("expected 2 stall + tie-breaker + 2 work + docs + audit = 7 calls, got %d", runner.calls)
	}
	// Verify no reviewer invocations ran for step 1 (the tie-broken step).
	// Step 2's verifier should still run normally.
	for call := 1; call <= runner.calls; call++ {
		prompt := runner.prompt(call)
		if strings.Contains(prompt, "claims complete") && strings.Contains(prompt, "1. Add the widget.") {
			t.Fatalf("expected no verifier for step 1 after tie-breaker accepted, but call %d was: %s", call, prompt)
		}
	}
	steps, _ := fs.Read("STEPS.md")
	if !strings.Contains(steps, "- [x] 1. Add the widget.") {
		t.Fatalf("expected step 1 to be checked after tie-breaker accept, got:\n%s", steps)
	}
}

func TestTieBreakerRejectQueuesGuidanceAndSkipsVerification(t *testing.T) {
	cfg := tieBreakerConfig()
	cfg.Verify = true
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		// calls 1-2: work invocations that check nothing (stall builds)
		// call 3: tie-breaker invocation (writes REJECT verdict + guidance)
		// call 4: work implements fix following tie-breaker guidance, checks step 1
		// call 5: work checks step 2
		// call 6: docs update
		// call 7: audit
		switch call {
		case 3:
			io.WriteString(out, tieBreakerRejectOutput)
		case 4:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 7:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion after tie-breaker rejection and rework, got %v", outcome)
	}
	if runner.calls != 7 {
		t.Fatalf("expected 2 stall + tie-breaker + 2 work + docs + audit = 7 calls, got %d", runner.calls)
	}
	// Guidance should be in NOTES.md.
	notes, err := fs.Read("NOTES.md")
	if err != nil {
		t.Fatalf("expected NOTES.md after tie-breaker reject: %v", err)
	}
	if !strings.Contains(notes, "Add error handling around the file read.") {
		t.Fatalf("expected tie-breaker guidance in NOTES.md, got:\n%s", notes)
	}
	// No verifier should have run for step 1 (the tie-broken step).
	// Step 2's verifier runs normally.
	for call := 1; call <= runner.calls; call++ {
		prompt := runner.prompt(call)
		if strings.Contains(prompt, "claims complete") && strings.Contains(prompt, "1. Add the widget.") {
			t.Fatalf("expected no verifier for step 1 after tie-breaker reject, but call %d was: %s", call, prompt)
		}
	}
}

func TestTieBreakerUnparseableFallsThroughToStop(t *testing.T) {
	cfg := tieBreakerConfig()
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		if call == 3 {
			io.WriteString(out, "just some rambling text with no verdict format\n")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	// Unparseable tie-breaker output falls through to stop (no StallResolver).
	if outcome != models.OutcomeStalled {
		t.Fatalf("expected OutcomeStalled after unparseable tie-breaker, got %v", outcome)
	}
}

func TestTieBreakerFallsThroughToHumanWhenParseableOutputFails(t *testing.T) {
	cfg := tieBreakerConfig()
	fs := stepsFileStore()
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionCancel},
	}}
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		if call == 3 {
			// Tie-breaker invocation fails (non-zero exit).
			return io.ErrUnexpectedEOF
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected OutcomeStalled after failed tie-breaker falls through to human, got %v", outcome)
	}
	if resolver.calls != 1 {
		t.Fatalf("expected the human resolver to be consulted after tie-breaker failed, got %d calls", resolver.calls)
	}
}

func TestTieBreakerDisabledKeepsExistingBehavior(t *testing.T) {
	cfg := stalledConfig() // TieBreaker defaults to false
	fs := stepsFileStore()
	resolver := &fakeStallResolver{verdicts: []models.StallGuidance{
		{Decision: models.StallDecisionCancel},
	}}
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStallResolver(resolver)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected no tie-breaker call when disabled, got %v", outcome)
	}
	// No tie-breaker prompt should have been issued.
	for call := 1; call <= runner.calls; call++ {
		prompt := runner.prompt(call)
		if strings.Contains(prompt, "tie-breaker") || strings.Contains(prompt, "tieBreaker") {
			t.Fatalf("expected no tie-breaker prompt when disabled, but call %d was: %s", call, prompt)
		}
	}
	if resolver.calls != 1 {
		t.Fatalf("expected the human resolver to handle stall when tie-breaker disabled, got %d calls", resolver.calls)
	}
}
