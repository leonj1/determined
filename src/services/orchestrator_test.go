package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// --- Hand-written Fakes (no mocking frameworks) ---

// fakeClock is a controllable clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeLog is an in-memory iteration log.
type fakeLog struct{ buf bytes.Buffer }

func (l *fakeLog) Write(p []byte) (int, error) { return l.buf.Write(p) }
func (l *fakeLog) Close() error                { return nil }

// fakeLogSink records every iteration log it opens.
type fakeLogSink struct{ opened []*fakeLog }

func (s *fakeLogSink) OpenIteration(int) (io.WriteCloser, error) {
	l := &fakeLog{}
	s.opened = append(s.opened, l)
	return l, nil
}

// fakeRunner runs a scripted behaviour and records its invocations.
type fakeRunner struct {
	calls       int
	invocations []models.Invocation
	script      func(call int, out io.Writer) error
}

func (r *fakeRunner) Run(_ context.Context, inv models.Invocation, out io.Writer) error {
	r.calls++
	r.invocations = append(r.invocations, inv)
	if r.script == nil {
		return nil
	}
	return r.script(r.calls, out)
}

// prompt extracts the prompt embedded in the call-th recorded invocation,
// relying on the droid argument shape ("exec", prompt, "--auto", ...).
func (r *fakeRunner) prompt(call int) string {
	return r.invocations[call-1].Args[1]
}

// hangingRunner simulates a hung tool: it never returns until the invocation
// context expires, like a real child killed by exec.CommandContext.
type hangingRunner struct{ calls int }

func (r *hangingRunner) Run(ctx context.Context, _ models.Invocation, _ io.Writer) error {
	r.calls++
	<-ctx.Done()
	return ctx.Err()
}

func config(budget time.Duration) models.Config {
	return models.Config{
		StopFile:  "STOP.md",
		PlanFile:  "PLAN.md",
		StepsFile: "STEPS.md",
		Tool:      models.DroidTool{},
		Budget:    budget,
	}
}

// plannedFileStore returns a file store seeded with a PLAN.md and the given
// STEPS.md content, the state a real run inherits from planning.
func plannedFileStore(steps string) *fakeFileStore {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "# Plan\n")
	fs.Write("STEPS.md", steps)
	return fs
}

// The two-step STEPS.md used across tests, in its three progress states.
const (
	twoStepsNoneChecked = "- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n\n" +
		"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n"
	twoStepsFirstChecked = "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n\n" +
		"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n"
	twoStepsAllChecked = "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n\n" +
		"- [x] 2. Document the widget.\n  Done when: README mentions the widget.\n"
)

// stepsFileStore returns a file store seeded with a two-step STEPS.md whose
// steps are both unchecked.
func stepsFileStore() *fakeFileStore {
	return plannedFileStore(twoStepsNoneChecked)
}

// --- Functional tests: what can the user achieve? ---

func TestRunEndsWhenAllBoxesAreChecked(t *testing.T) {
	fs := stepsFileStore()
	logs := &fakeLogSink{}
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		fmt.Fprintf(out, "working on step %d\n", call)
		switch call {
		case 1:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected the tool to run until every box is checked plus the audit (3 iterations), got %d", runner.calls)
	}
	if len(logs.opened) != 3 || !strings.Contains(logs.opened[0].buf.String(), "working on step 1") {
		t.Fatalf("expected a reviewable per-iteration log for each run, got %d logs", len(logs.opened))
	}
	if !fs.Exists("STOP.md") {
		t.Fatal("expected STOP.md present on completion")
	}
}

func TestPrematureStopFileIsDeletedAndLoopContinues(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("STOP.md", "premature")
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the tool checks step 1 but also declares completion early
			fs.Write("STEPS.md", twoStepsFirstChecked)
			fs.Write("STOP.md", "still premature")
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the run to end only when all boxes are checked, got %v", outcome)
	}
	if runner.calls != 3 {
		t.Fatalf("expected the loop to continue past each premature STOP.md, got %d invocations", runner.calls)
	}
	if got := strings.Count(terminal.String(), "unchecked steps remain"); got != 2 {
		t.Fatalf("expected a warning for each deleted premature STOP.md (2), got %d in:\n%s", got, terminal.String())
	}
}

func TestRunAbortsWhenToolFails(t *testing.T) {
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			return errors.New("droid: rate limited")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the loop to abort on the failing iteration, got %d", runner.calls)
	}
}

func TestFailedInvocationsAreRetriedUntilSuccess(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 3
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1, 2:
			return errors.New("droid: rate limited")
		case 3:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 4:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected retries to carry the run to completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 5 {
		t.Fatalf("expected 2 failed attempts, a retry that succeeds, the final step, then the audit (5 runs), got %d", runner.calls)
	}
	for call := 1; call <= 3; call++ {
		if !strings.Contains(runner.prompt(call), "1. Add the widget.") {
			t.Fatalf("expected attempt %d to retry the same step, got:\n%s", call, runner.prompt(call))
		}
	}
	if got := strings.Count(terminal.String(), "retrying"); got != 2 {
		t.Fatalf("expected a retry notice per failure (2), got %d in:\n%s", got, terminal.String())
	}
}

func TestRunAbortsAfterConsecutiveFailureCapExhausted(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 3
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(int, io.Writer) error {
		return errors.New("droid: rate limited")
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort with exit 1 once the cap is exhausted, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected exactly the cap of 3 consecutive attempts, got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "failed 3 consecutive times") {
		t.Fatalf("expected a terminal message explaining the abort, got:\n%s", terminal.String())
	}
}

func TestSuccessfulInvocationResetsTheFailureCounter(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 2
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 { // one failure, a success, then two more failures
			fs.Write("STEPS.md", twoStepsFirstChecked)
			return nil
		}
		return errors.New("droid: rate limited")
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed {
		t.Fatalf("expected the second failure streak to exhaust the cap, got %v", outcome)
	}
	if runner.calls != 4 {
		t.Fatalf("expected the success to reset the counter (fail + success + 2 fails = 4 runs), got %d", runner.calls)
	}
}

func TestHungInvocationTimesOutAndCountsAsFailure(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 2
	cfg.MaxIterationDuration = 5 * time.Millisecond
	var terminal bytes.Buffer
	runner := &hangingRunner{}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected timed-out invocations to exhaust the failure cap, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected each timeout to be retried as a failure until the cap (2 runs), got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "retrying") {
		t.Fatalf("expected the first timeout to be reported as a retryable failure, got:\n%s", terminal.String())
	}
}

func TestRunStopsWhenTimeBudgetExhausted(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		clock.advance(4 * time.Minute)
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), clock, &fakeLogSink{}, io.Discard, config(10*time.Minute))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeBudgetExceeded || outcome.ExitCode() != 1 {
		t.Fatalf("expected a budget stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected the in-flight iteration to finish before stopping, got %d", runner.calls)
	}
}

func TestRunStallsAfterConsecutiveIterationsWithoutProgress(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 3
	var terminal bytes.Buffer
	runner := &fakeRunner{} // never checks a step
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected a stalled stop with exit 3, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected the run to end after 3 no-progress iterations, got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "no step checked in 3 consecutive iterations") {
		t.Fatalf("expected a terminal message explaining the stall, got:\n%s", terminal.String())
	}
}

func TestCheckedStepResetsTheStallCounter(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 { // one no-progress iteration, then a step is checked
			fs.Write("STEPS.md", twoStepsFirstChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the run to stall eventually, got %v", outcome)
	}
	if runner.calls != 4 {
		t.Fatalf("expected progress to reset the counter (1 stall + progress + 2 stalls = 4 iterations), got %d", runner.calls)
	}
}

func TestStallDetectionDisabledByZeroCap(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		clock.advance(time.Minute) // never checks a step; only the budget can end the run
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), clock, &fakeLogSink{}, io.Discard, config(10*time.Minute))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeBudgetExceeded {
		t.Fatalf("expected no stall with a zero cap (budget ends the run), got %v", outcome)
	}
	if runner.calls != 10 {
		t.Fatalf("expected all 10 budgeted no-progress iterations to run, got %d", runner.calls)
	}
}

func TestRunEndsImmediatelyWhenAllStepsCheckedAndAuditApproved(t *testing.T) {
	fs := plannedFileStore("- [x] 1. Done already.\n  Done when: nothing remains.\n")
	fs.Write("STOP.md", "audit: plan satisfied")
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected an immediate clean exit, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no work when every step is checked and STOP.md exists, got %d invocations", runner.calls)
	}
}

func TestRunStopsWhenInterrupted(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 3 // an interruption must stop the run, never be retried
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(ctx)

	if outcome != models.OutcomeInterrupted || outcome.ExitCode() != 1 {
		t.Fatalf("expected an interrupted stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected no retry after an interruption, got %d runs", runner.calls)
	}
}

func TestEachIterationTargetsTheNextIncompleteStep(t *testing.T) {
	fs := plannedFileStore(
		"- [x] 1. Add the parser.\n  Done when: parser tests pass.\n\n" +
			"- [ ] 2. Wire the parser into the loop.\n  Done when: go test ./... passes.\n\n" +
			"- [ ] 3. Update the docs.\n  Done when: README describes the loop.\n")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the tool completes step 2 and checks its box
			fs.Write("STEPS.md",
				"- [x] 1. Add the parser.\n  Done when: parser tests pass.\n\n"+
					"- [x] 2. Wire the parser into the loop.\n  Done when: go test ./... passes.\n\n"+
					"- [ ] 3. Update the docs.\n  Done when: README describes the loop.\n")
		case 2: // the tool completes the final step
			fs.Write("STEPS.md",
				"- [x] 1. Add the parser.\n  Done when: parser tests pass.\n\n"+
					"- [x] 2. Wire the parser into the loop.\n  Done when: go test ./... passes.\n\n"+
					"- [x] 3. Update the docs.\n  Done when: README describes the loop.\n")
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	o.Run(context.Background())

	if runner.calls != 3 {
		t.Fatalf("expected 2 work iterations plus the audit, got %d", runner.calls)
	}
	first := runner.prompt(1)
	for _, want := range []string{
		"You are one invocation of an orchestrated loop",
		"Read NOTES.md if it exists before starting.",
		"If FIXES.md exists, read it too",
		"Work on exactly step 2 and no other: 2. Wire the parser into the loop.",
		"Its acceptance criterion: go test ./... passes.",
		"mark step 2 `[x]` in STEPS.md only once it passes",
		"do not create STOP.md",
		"append to NOTES.md any decisions, conventions, or gotchas later steps need to know",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("expected iteration 1's prompt to contain %q, got:\n%s", want, first)
		}
	}
	if !strings.Contains(runner.prompt(2), "3. Update the docs.") {
		t.Fatalf("expected iteration 2 to target the next unchecked step, got:\n%s", runner.prompt(2))
	}
}

func TestStepsFileWithoutCheckboxesFallsBackToStopSentinel(t *testing.T) {
	fs := plannedFileStore("1. Prose steps only, nothing the parser can track.\n")
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("STOP.md", "confirmed done")
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if runner.calls != 1 {
		t.Fatalf("expected the tool-created STOP.md honored when no steps parse, got %d invocations", runner.calls)
	}
	if !strings.Contains(runner.prompt(1), "no checkbox-format steps") {
		t.Fatalf("expected the prompt to explain the unparseable steps file, got:\n%s", runner.prompt(1))
	}
}

func TestExecuteFailsFastWhenProtocolFilesMissing(t *testing.T) {
	cases := []struct {
		name    string
		seed    map[string]string
		missing []string
	}{
		{"no plan and no steps", nil, []string{"PLAN.md", "STEPS.md"}},
		{"plan without steps", map[string]string{"PLAN.md": "# Plan\n"}, []string{"STEPS.md"}},
		{"steps without plan", map[string]string{"STEPS.md": twoStepsNoneChecked}, []string{"PLAN.md"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := newFakeFileStore()
			for path, content := range c.seed {
				fs.Write(path, content)
			}
			var terminal bytes.Buffer
			runner := &fakeRunner{}
			o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

			outcome := o.Run(context.Background())

			if outcome != models.OutcomeMissingFiles || outcome.ExitCode() == 0 {
				t.Fatalf("expected a non-zero missing-files abort, got %v (exit %d)", outcome, outcome.ExitCode())
			}
			if runner.calls != 0 {
				t.Fatalf("expected no tool runs without the protocol files, got %d", runner.calls)
			}
			for _, f := range c.missing {
				if !strings.Contains(terminal.String(), f) {
					t.Fatalf("expected the error to name missing %s, got:\n%s", f, terminal.String())
				}
			}
		})
	}
}

func TestVerifierApprovalLetsTheLoopAdvance(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // verifier approves step 1 by doing nothing
		case 3: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 4: // verifier approves step 2
		case 5: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 5 {
		t.Fatalf("expected work + verify per step plus the audit (5 runs), got %d", runner.calls)
	}
	verify := runner.prompt(2)
	for _, want := range []string{
		"Step 1 claims complete: 1. Add the widget.",
		"Acceptance criterion: widget tests pass.",
		"Assume the step is incomplete until you have run the check and seen it pass",
		"verify by reading the code and running the stated check",
		"append an entry to FIXES.md under a `## Step 1` heading",
		"Do not fix anything yourself, do not modify code",
		"uncheck any step other than step 1",
	} {
		if !strings.Contains(verify, want) {
			t.Fatalf("expected the verifier prompt to contain %q, got:\n%s", want, verify)
		}
	}
	if !strings.Contains(runner.prompt(3), "2. Document the widget.") {
		t.Fatalf("expected the loop to advance to step 2 after approval, got:\n%s", runner.prompt(3))
	}
}

func TestVerifierRejectionRerunsTheSameStep(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	fs := stepsFileStore()
	fixesAtRerun := false
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // verifier rejects: uncheck step 1 and record why
			fs.Write("STEPS.md", twoStepsNoneChecked)
			fs.Write("FIXES.md", "widget tests actually fail\n")
		case 3: // work re-runs step 1 with FIXES.md present
			fixesAtRerun = fs.Exists("FIXES.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 4: // verifier approves this time
		case 5: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 6: // verifier approves
		case 7: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the rejected step to be redone and the run to complete, got %v", outcome)
	}
	if runner.calls != 7 {
		t.Fatalf("expected the rejected step to cost an extra work+verify round (7 runs with the audit), got %d", runner.calls)
	}
	if !strings.Contains(runner.prompt(3), "1. Add the widget.") {
		t.Fatalf("expected the loop to re-run the unchecked step, got:\n%s", runner.prompt(3))
	}
	if !fixesAtRerun {
		t.Fatal("expected FIXES.md to exist when the step is re-run")
	}
}

func TestVerifierRejectionsCountTowardTheStallCap(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.MaxStalledIterations = 2
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call%2 == 1 { // work always claims step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		} else { // verifier always rejects it
			fs.Write("STEPS.md", twoStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected worker/verifier ping-pong to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 4 {
		t.Fatalf("expected 2 rejected rounds (work + verify each) before stalling, got %d", runner.calls)
	}
}

func TestVerificationDisabledRunsNoVerifier(t *testing.T) {
	cfg := config(0)
	cfg.Verify = false
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if runner.calls != 3 {
		t.Fatalf("expected only the work invocations plus the audit with --verify off, got %d", runner.calls)
	}
	for call := 1; call <= 3; call++ {
		if strings.Contains(runner.prompt(call), "claims complete") {
			t.Fatalf("expected no verifier prompts with --verify off, got:\n%s", runner.prompt(call))
		}
	}
}

func TestAuditApprovalEndsTheRunSuccessfully(t *testing.T) {
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // audit approves the whole plan
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the audited run to end cleanly, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected 2 work iterations plus 1 audit, got %d", runner.calls)
	}
	audit := runner.prompt(3)
	for _, want := range []string{
		"Read PLAN.md and STEPS.md.",
		"Audit whether the implementation genuinely satisfies the plan.",
		"Run the project's build and test suite",
		"append the reason to FIXES.md",
		"do not fix anything yourself",
		"create STOP.md containing a short report",
	} {
		if !strings.Contains(audit, want) {
			t.Fatalf("expected the audit prompt to contain %q, got:\n%s", want, audit)
		}
	}
	if !strings.Contains(terminal.String(), "auditing the whole plan") {
		t.Fatalf("expected a terminal note announcing the audit, got:\n%s", terminal.String())
	}
}

func TestAuditReopeningAStepResumesTheLoop(t *testing.T) {
	fs := stepsFileStore()
	fixesAtRerun := false
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // audit reopens step 2 and records why
			fs.Write("STEPS.md", twoStepsFirstChecked)
			fs.Write("FIXES.md", "step 2 does not satisfy the plan\n")
		case 4: // work redoes the reopened step with FIXES.md present
			fixesAtRerun = fs.Exists("FIXES.md")
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // audit approves this time
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the reopened step to be redone and the run to complete, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 5 {
		t.Fatalf("expected the audit rejection to cost an extra work+audit round (5 runs), got %d", runner.calls)
	}
	if !strings.Contains(runner.prompt(4), "2. Document the widget.") {
		t.Fatalf("expected the loop to resume on the step the audit unchecked, got:\n%s", runner.prompt(4))
	}
	if !fixesAtRerun {
		t.Fatal("expected FIXES.md to exist when the reopened step is re-run")
	}
}

func TestAuditRejectionsCountTowardTheStallCap(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	fs := plannedFileStore(twoStepsAllChecked) // work already done, only the audit remains
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		return nil // the audit neither approves nor reopens anything
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected do-nothing audits to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the stall cap to bound repeated audits (2 runs), got %d", runner.calls)
	}
}

// gitInvocations returns the recorded invocations that ran the git binary.
func (r *fakeRunner) gitInvocations() []models.Invocation {
	var git []models.Invocation
	for _, inv := range r.invocations {
		if inv.Binary == "git" {
			git = append(git, inv)
		}
	}
	return git
}

func TestVerifiedStepIsGitCheckpointed(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = true
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // verifier approves step 1
		// 3, 4: git add + git commit checkpoint step 1
		case 5: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 6: // verifier approves step 2
			// 7, 8: git add + git commit checkpoint step 2
		case 9: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	git := runner.gitInvocations()
	if len(git) != 4 {
		t.Fatalf("expected add+commit per verified step (4 git runs), got %d", len(git))
	}
	wantArgs := [][]string{
		{"add", "-A"},
		{"commit", "-m", "determined: step 1: 1. Add the widget."},
		{"add", "-A"},
		{"commit", "-m", "determined: step 2: 2. Document the widget."},
	}
	for i, want := range wantArgs {
		if got := strings.Join(git[i].Args, " "); got != strings.Join(want, " ") {
			t.Fatalf("git invocation %d: expected %q, got %q", i+1, strings.Join(want, " "), got)
		}
	}
	if runner.invocations[2].Binary != "git" || runner.invocations[3].Binary != "git" {
		t.Fatal("expected the checkpoint to run right after the verifier approves the step")
	}
}

func TestGitCheckpointDisabledIssuesNoGitCommands(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = false
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // work: check step 2 (2 and 4 are verifier approvals)
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if got := runner.gitInvocations(); len(got) != 0 {
		t.Fatalf("expected no git invocations with --git-checkpoint off, got %d", len(got))
	}
	if runner.calls != 5 {
		t.Fatalf("expected only work + verify per step plus the audit (5 runs), got %d", runner.calls)
	}
}

func TestGitCheckpointSkippedOutsideGitRepository(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = true
	fs := stepsFileStore() // no .git seeded
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // work: check step 2 (2 and 4 are verifier approvals)
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected the run to complete without a repository, got %v", outcome)
	}
	if got := runner.gitInvocations(); len(got) != 0 {
		t.Fatalf("expected no git invocations outside a git repository, got %d", len(got))
	}
	if !strings.Contains(terminal.String(), "not a git repository; skipping git checkpoint") {
		t.Fatalf("expected a terminal note about the skipped checkpoint, got:\n%s", terminal.String())
	}
}

func TestRejectedStepIsNotCheckpointed(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = true
	cfg.MaxStalledIterations = 1
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // verifier rejects it
			fs.Write("STEPS.md", twoStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the rejected round to end as a stall, got %v", outcome)
	}
	if got := runner.gitInvocations(); len(got) != 0 {
		t.Fatalf("expected no checkpoint for a step the verifier rejected, got %d git invocations", len(got))
	}
}

// shInvocations returns the recorded invocations that ran the check shell.
func (r *fakeRunner) shInvocations() []models.Invocation {
	var sh []models.Invocation
	for _, inv := range r.invocations {
		if inv.Binary == "sh" {
			sh = append(sh, inv)
		}
	}
	return sh
}

// verifierPromptCount counts the recorded tool invocations that carry a
// verifier prompt, ignoring the git and sh subprocess invocations.
func (r *fakeRunner) verifierPromptCount() int {
	n := 0
	for _, inv := range r.invocations {
		if inv.Binary == "git" || inv.Binary == "sh" {
			continue
		}
		if strings.Contains(inv.Args[1], "claims complete") {
			n++
		}
	}
	return n
}

func TestCheckCommandPassKeepsVerifierAndCompletesTheRun(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.CheckCmd = "go test ./..."
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // check command passes
			fmt.Fprintln(out, "ok  determined")
		case 3: // verifier approves step 1
		case 4: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // check command passes again
		case 6: // verifier approves step 2
		case 7: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 7 {
		t.Fatalf("expected work + check + verify per step plus the audit (7 runs), got %d", runner.calls)
	}
	sh := runner.shInvocations()
	if len(sh) != 2 {
		t.Fatalf("expected one check run per step-checking iteration (2), got %d", len(sh))
	}
	for i, inv := range sh {
		if got := strings.Join(inv.Args, " "); got != "-c go test ./..." {
			t.Fatalf("sh invocation %d: expected `-c go test ./...`, got %q", i+1, got)
		}
	}
	if runner.invocations[1].Binary != "sh" {
		t.Fatal("expected the check command to run before the verifier")
	}
	if !strings.Contains(runner.prompt(3), "Step 1 claims complete") {
		t.Fatalf("expected the verifier to still run after a passing check, got:\n%s", runner.prompt(3))
	}
}

func TestCheckCommandFailureUnchecksStepAndSkipsVerifierAndCheckpoint(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = true
	cfg.CheckCmd = "go test ./..."
	fs := stepsFileStore()
	fs.Write(".git", "")
	stepsAtRerun, fixesAtRerun := "", ""
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // check command fails: the gate rejects step 1 mechanically
			fmt.Fprintln(out, "--- FAIL: TestWidget")
			return errors.New("exit status 1")
		case 3: // work re-runs step 1 with the rejection on record
			stepsAtRerun, _ = fs.Read("STEPS.md")
			fixesAtRerun, _ = fs.Read("FIXES.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 4: // check command passes this time
		case 5: // verifier approves step 1 (6, 7: git add + commit)
		case 8: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 9: // check command passes
		case 10: // verifier approves step 2 (11, 12: git add + commit)
		case 13: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the rejected step to be redone and the run to complete, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 13 {
		t.Fatalf("expected the failed check to cost one extra work+check round (13 runs), got %d", runner.calls)
	}
	if stepsAtRerun != twoStepsNoneChecked {
		t.Fatalf("expected the gate to uncheck step 1 preserving the file's exact content, got:\n%s", stepsAtRerun)
	}
	for _, want := range []string{"## Step 1", "go test ./...", "--- FAIL: TestWidget"} {
		if !strings.Contains(fixesAtRerun, want) {
			t.Fatalf("expected FIXES.md to contain %q at re-run, got:\n%s", want, fixesAtRerun)
		}
	}
	if !strings.Contains(runner.prompt(3), "1. Add the widget.") {
		t.Fatalf("expected the loop to re-run the unchecked step, got:\n%s", runner.prompt(3))
	}
	if got := runner.verifierPromptCount(); got != 2 {
		t.Fatalf("expected no verifier run for the rejected round (2 verifier runs total), got %d", got)
	}
	if got := runner.gitInvocations(); len(got) != 4 {
		t.Fatalf("expected no checkpoint for the rejected round (4 git runs total), got %d", len(got))
	}
}

func TestCheckCommandFailuresDoNotCountTowardTheFailureCap(t *testing.T) {
	cfg := config(0)
	cfg.MaxConsecutiveFailures = 1 // any counted failure aborts immediately
	cfg.CheckCmd = "go test ./..."
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1, 3: // work checks step 1; again after the gate rejects it
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // check command fails: a verdict, not a tool failure
			return errors.New("exit status 1")
		case 5: // work: check step 2 (4 was the passing re-check)
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 7: // the whole-plan audit approves (6 was the passing check)
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the check failure not to trip the failure cap, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 7 {
		t.Fatalf("expected the run to continue through the failed check (7 runs), got %d", runner.calls)
	}
}

func TestExtraCheckedBoxIsRevertedAndTheCheckGateCoversTheSurvivor(t *testing.T) {
	cfg := config(0)
	cfg.CheckCmd = "go test ./..."
	fs := stepsFileStore()
	var terminal bytes.Buffer
	stepsAtCheck := ""
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work on step 1 checks step 2's box too
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 2: // check command: the guard has already reverted the extra box
			stepsAtCheck, _ = fs.Read("STEPS.md")
		case 3: // work: check step 2 legitimately
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // the whole-plan audit approves (4 was step 2's check run)
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if stepsAtCheck != twoStepsFirstChecked {
		t.Fatalf("expected only the target step's check to survive into the gate, got:\n%s", stepsAtCheck)
	}
	if !strings.Contains(terminal.String(),
		"determined: warning: STEPS.md was altered beyond checking step 1 (step 2's checkbox changed); restoring it") {
		t.Fatalf("expected a tamper warning naming the extra box, got:\n%s", terminal.String())
	}
	if got := runner.shInvocations(); len(got) != 2 {
		t.Fatalf("expected one check run per surviving step (2), got %d", len(got))
	}
}

func TestCheckCommandDisabledRunsNoShellInvocations(t *testing.T) {
	fs := stepsFileStore() // config() leaves CheckCmd empty
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if got := runner.shInvocations(); len(got) != 0 {
		t.Fatalf("expected no sh invocations with --check-cmd unset, got %d", len(got))
	}
	if runner.calls != 3 {
		t.Fatalf("expected exactly today's 3 invocations with the gate disabled, got %d", runner.calls)
	}
}

// tamperGuardWarning is the terminal warning the STEPS.md tamper guard
// prints, parameterized by the target step and the named violation.
func tamperGuardWarning(target int, violation string) string {
	return fmt.Sprintf(
		"determined: warning: STEPS.md was altered beyond checking step %d (%s); restoring it",
		target, violation)
}

func TestRewordedTargetStepIsRestored(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 1
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(int, io.Writer) error {
		// The worker rewords the step it was asked to do instead of doing it.
		fs.Write("STEPS.md", strings.Replace(twoStepsNoneChecked, "Add the widget", "Add a stub", 1))
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the reverted iteration to end as a stall, got %v", outcome)
	}
	if got, _ := fs.Read("STEPS.md"); got != twoStepsNoneChecked {
		t.Fatalf("expected STEPS.md restored byte-for-byte, got:\n%s", got)
	}
	if !strings.Contains(terminal.String(), tamperGuardWarning(1, "step 1's text changed")) {
		t.Fatalf("expected a tamper warning naming the reworded step, got:\n%s", terminal.String())
	}
}

func TestWeakenedNonTargetDoneWhenIsRestored(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 1
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(int, io.Writer) error {
		// The worker weakens another step's acceptance criterion.
		fs.Write("STEPS.md", strings.Replace(twoStepsNoneChecked,
			"Done when: README mentions the widget.", "Done when: anything at all.", 1))
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the reverted iteration to end as a stall, got %v", outcome)
	}
	if got, _ := fs.Read("STEPS.md"); got != twoStepsNoneChecked {
		t.Fatalf("expected STEPS.md restored byte-for-byte, got:\n%s", got)
	}
	if !strings.Contains(terminal.String(), tamperGuardWarning(1, "step 2's Done-when criterion changed")) {
		t.Fatalf("expected a tamper warning naming the weakened criterion, got:\n%s", terminal.String())
	}
}

func TestDeletedStepIsRestored(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 1
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(int, io.Writer) error {
		// The worker deletes the second step outright.
		fs.Write("STEPS.md", "- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the reverted iteration to end as a stall, got %v", outcome)
	}
	if got, _ := fs.Read("STEPS.md"); got != twoStepsNoneChecked {
		t.Fatalf("expected STEPS.md restored byte-for-byte, got:\n%s", got)
	}
	if !strings.Contains(terminal.String(), tamperGuardWarning(1, "step count changed from 2 to 1")) {
		t.Fatalf("expected a tamper warning naming the deletion, got:\n%s", terminal.String())
	}
}

func TestLegitimateCheckSurvivesTamperRestoration(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	fs := stepsFileStore()
	var terminal bytes.Buffer
	stepsAtVerify := ""
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work checks step 1 legitimately but also rewords step 2
			fs.Write("STEPS.md", strings.Replace(twoStepsFirstChecked,
				"Document the widget", "Ship it undocumented", 1))
		case 2: // verifier sees the restored file with only the check applied
			stepsAtVerify, _ = fs.Read("STEPS.md")
		case 3: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 4: // verifier approves step 2
		case 5: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the run to complete after the revert, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if stepsAtVerify != twoStepsFirstChecked {
		t.Fatalf("expected the tampering reverted but the target's check kept, got:\n%s", stepsAtVerify)
	}
	if !strings.Contains(runner.prompt(2), "Step 1 claims complete") {
		t.Fatalf("expected the surviving check to still be verified, got:\n%s", runner.prompt(2))
	}
	if !strings.Contains(terminal.String(), tamperGuardWarning(1, "step 2's text changed")) {
		t.Fatalf("expected a tamper warning naming the reworded step, got:\n%s", terminal.String())
	}
}

func TestTamperedIterationsCountTowardTheStallCap(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		// Every iteration tampers instead of working; each revert is no progress.
		fs.Write("STEPS.md", strings.Replace(twoStepsNoneChecked, "Add the widget", "Add a stub", 1))
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected repeated tampering to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the stall cap to bound tampered iterations (2 runs), got %d", runner.calls)
	}
}

func TestAuditUncheckingStepsIsNotTreatedAsTampering(t *testing.T) {
	fs := plannedFileStore(twoStepsAllChecked) // work already done, the audit runs first
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // audit reopens step 2
			fs.Write("STEPS.md", twoStepsFirstChecked)
			fs.Write("FIXES.md", "step 2 does not satisfy the plan\n")
		case 2: // work redoes the reopened step
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // audit approves this time
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the reopened run to complete, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if !strings.Contains(runner.prompt(2), "2. Document the widget.") {
		t.Fatalf("expected the audit's uncheck to stand and be re-run, got:\n%s", runner.prompt(2))
	}
	if strings.Contains(terminal.String(), "altered beyond checking") {
		t.Fatalf("expected no tamper warning for the audit's uncheck, got:\n%s", terminal.String())
	}
}

func TestFallbackRewriteIsNotTreatedAsTampering(t *testing.T) {
	fs := plannedFileStore("1. Prose steps only, nothing the parser can track.\n")
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the fallback restores a checkbox-format step list
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 2: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the rewritten step list to carry the run to completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if !strings.Contains(runner.prompt(2), "1. Add the widget.") {
		t.Fatalf("expected the rewritten steps to stand and be worked, got:\n%s", runner.prompt(2))
	}
	if strings.Contains(terminal.String(), "altered beyond checking") {
		t.Fatalf("expected no tamper warning for the fallback rewrite, got:\n%s", terminal.String())
	}
}

// The stuck first step split into two smaller ones, in its progress states.
const (
	splitStepsNoneChecked = "- [ ] 1a. Add the widget skeleton.\n  Done when: the skeleton compiles.\n\n" +
		"- [ ] 1b. Wire the widget logic.\n  Done when: widget tests pass.\n\n" +
		"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n"
	splitStepsFirstChecked = "- [x] 1a. Add the widget skeleton.\n  Done when: the skeleton compiles.\n\n" +
		"- [ ] 1b. Wire the widget logic.\n  Done when: widget tests pass.\n\n" +
		"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n"
	splitStepsSecondChecked = "- [x] 1a. Add the widget skeleton.\n  Done when: the skeleton compiles.\n\n" +
		"- [x] 1b. Wire the widget logic.\n  Done when: widget tests pass.\n\n" +
		"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n"
	splitStepsAllChecked = "- [x] 1a. Add the widget skeleton.\n  Done when: the skeleton compiles.\n\n" +
		"- [x] 1b. Wire the widget logic.\n  Done when: widget tests pass.\n\n" +
		"- [x] 2. Document the widget.\n  Done when: README mentions the widget.\n"
)

// replanPrompts counts recorded invocations that carry the replan instruction.
func (r *fakeRunner) replanPrompts() int {
	n := 0
	for call := 1; call <= len(r.invocations); call++ {
		if r.invocations[call-1].Binary != "git" && r.invocations[call-1].Binary != "sh" &&
			strings.Contains(r.prompt(call), "repeatedly failed verification") {
			n++
		}
	}
	return n
}

func TestStallTriggersReplanThatResumesTheRun(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := stepsFileStore()
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1, 2: // two no-progress work iterations hit the stall cap
		case 3: // the replan splits the stuck step 1 into 1a and 1b
			fs.Write("STEPS.md", splitStepsNoneChecked)
		case 4:
			fs.Write("STEPS.md", splitStepsFirstChecked)
		case 5:
			fs.Write("STEPS.md", splitStepsSecondChecked)
		case 6:
			fs.Write("STEPS.md", splitStepsAllChecked)
		case 7: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the replanned run to complete cleanly, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 7 {
		t.Fatalf("expected 2 stalls + 1 replan + 3 split steps + the audit (7 runs), got %d", runner.calls)
	}
	replan := runner.prompt(3)
	for _, want := range []string{
		"Step 1 has repeatedly failed verification",
		"1. Add the widget.",
		"Its acceptance criterion: widget tests pass.",
		"Replace step 1 in STEPS.md with 2-4 smaller `- [ ]` checkbox steps",
		"FIXES.md",
		"Do not check any box, do not implement anything, and do not create STOP.md.",
	} {
		if !strings.Contains(replan, want) {
			t.Fatalf("expected the replan prompt to contain %q, got:\n%s", want, replan)
		}
	}
	if !strings.Contains(runner.prompt(4), "1a. Add the widget skeleton.") {
		t.Fatalf("expected the loop to resume on the first split step, got:\n%s", runner.prompt(4))
	}
	if !strings.Contains(terminal.String(), "step 1 replanned; STEPS.md now has 3 steps; resuming") {
		t.Fatalf("expected a terminal note announcing the replan, got:\n%s", terminal.String())
	}
}

func TestReplanThatChangesNothingStallsTheRun(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	var terminal bytes.Buffer
	runner := &fakeRunner{} // neither the work iterations nor the replan change anything
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected an ineffective replan to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected 2 stalled iterations plus the replan attempt (3 runs), got %d", runner.calls)
	}
	if !strings.Contains(terminal.String(), "replan left step 1 unchanged") {
		t.Fatalf("expected a terminal note about the ineffective replan, got:\n%s", terminal.String())
	}
}

func TestReplanThatDamagesStepsFileIsRestoredAndTheRunStalls(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := plannedFileStore(twoStepsFirstChecked) // step 1 is finished work the replan must not lose
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 3 { // the replan unchecks the completed step
			fs.Write("STEPS.md", twoStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected the damaging replan to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if got, _ := fs.Read("STEPS.md"); got != twoStepsFirstChecked {
		t.Fatalf("expected STEPS.md restored to its pre-replan content, got:\n%s", got)
	}
	if !strings.Contains(terminal.String(), "replan damaged STEPS.md; restoring it") {
		t.Fatalf("expected a terminal note about the damaged file, got:\n%s", terminal.String())
	}
}

func TestReplanningDisabledStallsExactlyAsBefore(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 0
	runner := &fakeRunner{} // never checks a step
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected a plain stall with replanning disabled, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 || runner.replanPrompts() != 0 {
		t.Fatalf("expected no replan invocation (2 runs), got %d runs and %d replans",
			runner.calls, runner.replanPrompts())
	}
}

func TestReplanBudgetIsConsumedOncePerRun(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 3 { // the only replan splits step 1; nothing else ever progresses
			fs.Write("STEPS.md", splitStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected the second stall to end the run once the budget is spent, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 5 {
		t.Fatalf("expected 2 stalls + replan + 2 more stalls (5 runs), got %d", runner.calls)
	}
	if runner.replanPrompts() != 1 {
		t.Fatalf("expected exactly one replan invocation, got %d", runner.replanPrompts())
	}
}

func TestAuditPingPongIsNeverReplanned(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := plannedFileStore(twoStepsAllChecked) // only the audit remains, and it never decides
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected audit ping-pong to stall without replanning, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 || runner.replanPrompts() != 0 {
		t.Fatalf("expected no replan with every box checked (2 audit runs), got %d runs and %d replans",
			runner.calls, runner.replanPrompts())
	}
}

// stashConfig returns a config with verification, checkpointing, and
// failed-attempt stashing all enabled, the wiring main.go produces by default.
func stashConfig() models.Config {
	cfg := config(0)
	cfg.Verify = true
	cfg.GitCheckpoint = true
	cfg.StashAttempts = true
	cfg.LogDir = "logs"
	return cfg
}

// stashPushInvocations returns the recorded `git stash push` invocations.
func (r *fakeRunner) stashPushInvocations() []models.Invocation {
	var pushes []models.Invocation
	for _, inv := range r.gitInvocations() {
		if len(inv.Args) >= 2 && inv.Args[0] == "stash" && inv.Args[1] == "push" {
			pushes = append(pushes, inv)
		}
	}
	return pushes
}

func TestSecondRejectionStashesTheAttemptAndTheRetryStartsClean(t *testing.T) {
	fs := stepsFileStore()
	fs.Write(".git", "")
	fixesAtRerun := ""
	runner := &fakeRunner{}
	runner.script = func(call int, out io.Writer) error {
		switch call {
		case 1: // startup: git status finds the tree clean (no output)
		case 2: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // verifier rejects step 1 (rejection #1: retry in place, no stash)
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 4: // work: check step 1 again
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5: // verifier rejects step 1 again (rejection #2: stash the attempt)
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 6: // git rev-parse -q --verify refs/stash: no stash exists yet
			return errors.New("exit status 1")
		case 7: // git stash push succeeds
		case 8: // git rev-parse refs/stash yields the new stash's hash
			fmt.Fprintln(out, "aaa111")
		case 9: // git stash show --stat
			fmt.Fprintln(out, " widget.go | 5 +")
		case 10: // work re-runs step 1 from the clean checkpoint
			fixesAtRerun, _ = fs.Read("FIXES.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 11: // verifier approves step 1
		case 12, 13: // git add + git commit checkpoint step 1
		case 14: // git stash list resolves the stash's position
			fmt.Fprintln(out, "aaa111")
		case 15: // git stash drop
		case 16: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 17: // verifier approves step 2
		case 18, 19: // git add + git commit checkpoint step 2
		case 20: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	var terminal bytes.Buffer
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, stashConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 20 {
		t.Fatalf("expected the second rejection to cost one stash round (20 runs), got %d", runner.calls)
	}
	pushes := runner.stashPushInvocations()
	if len(pushes) != 1 {
		t.Fatalf("expected exactly one stash push (second rejection only), got %d", len(pushes))
	}
	push := strings.Join(pushes[0].Args, " ")
	if !strings.Contains(push, "step 1 rejected attempt 2") {
		t.Fatalf("expected the stash message to name the step and attempt, got %q", push)
	}
	for _, protected := range []string{":(exclude)STEPS.md", ":(exclude)FIXES.md", ":(exclude)NOTES.md", ":(exclude)STALLED.md", ":(exclude)logs"} {
		if !strings.Contains(push, protected) {
			t.Fatalf("expected the stash push to exclude %s, got %q", protected, push)
		}
	}
	for _, want := range []string{"## Step 1", "aaa111", "widget.go | 5 +", "do not apply it wholesale"} {
		if !strings.Contains(fixesAtRerun, want) {
			t.Fatalf("expected FIXES.md to record %q for the re-run worker, got:\n%s", want, fixesAtRerun)
		}
	}
	if rerun := runner.prompt(10); !strings.Contains(rerun, "aaa111") || !strings.Contains(rerun, "do not apply it wholesale") {
		t.Fatalf("expected the re-run prompt to point at the stash as evidence, got:\n%s", rerun)
	}
	if drop := strings.Join(runner.invocations[14].Args, " "); drop != "stash drop stash@{0}" {
		t.Fatalf("expected the passing step to drop its stash, got %q", drop)
	}
	if !strings.Contains(terminal.String(), "stashed the rejected attempt at step 1") {
		t.Fatalf("expected a terminal note about the stash, got:\n%s", terminal.String())
	}
}

func TestFirstRejectionRetriesInPlaceWithoutStashing(t *testing.T) {
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{}
	runner.script = func(call int, _ io.Writer) error {
		switch call {
		case 1: // startup: git status finds the tree clean
		case 2: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // verifier rejects step 1 (rejection #1)
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 4: // work re-runs step 1 on top of the failed attempt
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5: // verifier approves (6, 7: git add + commit)
		case 8: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 9: // verifier approves (10, 11: git add + commit)
		case 12: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, stashConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if got := runner.stashPushInvocations(); len(got) != 0 {
		t.Fatalf("expected no stash on a first rejection, got %d stash pushes", len(got))
	}
	if strings.Contains(runner.prompt(4), "git stash") {
		t.Fatalf("expected the first retry's prompt to carry no stash note, got:\n%s", runner.prompt(4))
	}
}

func TestDirtyTreeAtStartupDisablesStashing(t *testing.T) {
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{}
	runner.script = func(call int, out io.Writer) error {
		switch call {
		case 1: // startup: git status reports the user's own uncommitted change
			fmt.Fprintln(out, " M main.go")
		case 2: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3: // verifier rejects step 1 (rejection #1)
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 4: // work: check step 1 again
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5: // verifier rejects again (rejection #2: would stash, but disabled)
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 6: // work: check step 1 once more
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 7: // verifier approves (8, 9: git add + commit)
		case 10: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 11: // verifier approves (12, 13: git add + commit)
		case 14: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	var terminal bytes.Buffer
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, stashConfig())

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the run to complete without stashing, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if got := runner.stashPushInvocations(); len(got) != 0 {
		t.Fatalf("expected pre-existing changes to disable stashing for the run, got %d stash pushes", len(got))
	}
	if !strings.Contains(terminal.String(), "changes that predate this run") {
		t.Fatalf("expected a warning that stashing is disabled, got:\n%s", terminal.String())
	}
}

func TestStashingRequiresGitCheckpointing(t *testing.T) {
	cfg := stashConfig()
	cfg.GitCheckpoint = false
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{}
	runner.script = func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1 (no startup git status without checkpointing)
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2, 4: // verifier rejects step 1 twice
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 3, 5: // work re-checks step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 6: // verifier approves step 1
		case 7: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 8: // verifier approves step 2
		case 9: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if got := runner.gitInvocations(); len(got) != 0 {
		t.Fatalf("expected no git commands at all with checkpointing off, got %d", len(got))
	}
}

// readRunReport decodes the run-report.json a run left in the file store.
func readRunReport(t *testing.T, fs services.FileStore) map[string]any {
	t.Helper()
	content, err := fs.Read("run-report.json")
	if err != nil {
		t.Fatalf("expected run-report.json to be written: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(content), &report); err != nil {
		t.Fatalf("run-report.json is not valid JSON: %v\n%s", err, content)
	}
	return report
}

// assertReportFields checks each named field of the decoded report; JSON
// numbers decode as float64.
func assertReportFields(t *testing.T, report map[string]any, want map[string]any) {
	t.Helper()
	for field, v := range want {
		if report[field] != v {
			t.Fatalf("expected report %s = %v, got %v", field, v, report[field])
		}
	}
}

// assertReportSteps checks the report's steps tally.
func assertReportSteps(t *testing.T, report map[string]any, total, checked int) {
	t.Helper()
	steps, ok := report["steps"].(map[string]any)
	if !ok || steps["total"] != float64(total) || steps["checked"] != float64(checked) {
		t.Fatalf("expected %d of %d steps checked in the report, got %v", checked, total, report["steps"])
	}
}

func TestRunReportWrittenOnSuccess(t *testing.T) {
	cfg := config(0)
	cfg.LogDir = "logs"
	fs := stepsFileStore()
	fs.Write("run-report.json", `{"outcome":"stalled"}`) // stale report from a previous run
	clock := &fakeClock{now: time.Now()}
	staleAtFirstIteration := false
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		clock.advance(time.Minute)
		switch call {
		case 1:
			staleAtFirstIteration = fs.Exists("run-report.json")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, clock, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if staleAtFirstIteration {
		t.Fatal("expected the stale run-report.json removed at startup")
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":      "success",
		"exit":         float64(0),
		"iterations":   float64(3),
		"wall_seconds": float64(180),
		"log_dir":      "logs",
	})
	assertReportSteps(t, report, 2, 2)
	for _, absent := range []string{"stuck_step", "rejections", "replans_used"} {
		if _, ok := report[absent]; ok {
			t.Fatalf("expected %s omitted from a clean run's report, got %v", absent, report[absent])
		}
	}
}

func TestRunReportWrittenOnStall(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 3
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 { // step 1 lands; step 2 never does
			fs.Write("STEPS.md", twoStepsFirstChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected a stalled stop, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":    "stalled",
		"exit":       float64(3),
		"stuck_step": float64(2),
		"iterations": float64(4),
	})
	assertReportSteps(t, report, 2, 1)
}

func TestRunReportWrittenOnToolFailureAbort(t *testing.T) {
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			return errors.New("droid: rate limited")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected a tool-failure abort, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":    "failed",
		"exit":       float64(1),
		"iterations": float64(2),
	})
	assertReportSteps(t, report, 2, 0)
}

func TestRunReportWrittenWhenProtocolFilesMissing(t *testing.T) {
	fs := newFakeFileStore()
	o := services.NewOrchestrator(&fakeRunner{}, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeMissingFiles {
		t.Fatalf("expected a missing-files abort, got %v", outcome)
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":    "failed",
		"exit":       float64(1),
		"iterations": float64(0),
	})
	if _, ok := report["steps"]; ok {
		t.Fatalf("expected the steps tally omitted with no steps file, got %v", report["steps"])
	}
}

func TestRunReportRecordsPerStepRejections(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // verifier rejects step 1
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 3: // work re-runs step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 4: // verifier approves this time
		case 5: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 6: // verifier approves
		case 7: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	report := readRunReport(t, fs)
	rejections, ok := report["rejections"].(map[string]any)
	if !ok || len(rejections) != 1 || rejections["1"] != float64(1) {
		t.Fatalf("expected the report to count step 1's rejection once, got %v", report["rejections"])
	}
	assertReportFields(t, report, map[string]any{"outcome": "success", "iterations": float64(7)})
}

func TestRunReportRecordsReplansUsed(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 3 { // the only replan splits step 1; nothing else ever progresses
			fs.Write("STEPS.md", splitStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the run to stall once the replan budget is spent, got %v", outcome)
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":      "stalled",
		"replans_used": float64(1),
		"stuck_step":   float64(1),
		"iterations":   float64(5),
	})
	assertReportSteps(t, report, 3, 0)
}

// failingWriteStore wraps a fakeFileStore, failing every write to one path.
type failingWriteStore struct {
	*fakeFileStore
	failPath string
}

func (s *failingWriteStore) Write(path, content string) error {
	if path == s.failPath {
		return errors.New("disk full")
	}
	return s.fakeFileStore.Write(path, content)
}

func TestRunReportWriteFailureDoesNotChangeTheOutcome(t *testing.T) {
	fs := &failingWriteStore{fakeFileStore: stepsFileStore(), failPath: "run-report.json"}
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the failed report write to leave the outcome alone, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if !strings.Contains(terminal.String(), "could not write run-report.json") {
		t.Fatalf("expected a terminal warning about the failed report write, got:\n%s", terminal.String())
	}
	if fs.Exists("run-report.json") {
		t.Fatal("expected no run-report.json after the failed write")
	}
}

func TestStalledRunWritesTheStallHandoffReport(t *testing.T) {
	cfg := config(time.Hour)
	cfg.Verify = true
	cfg.MaxStalledIterations = 2
	cfg.MaxReplans = 1
	fs := stepsFileStore()
	clock := &fakeClock{now: time.Now()}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		clock.advance(3 * time.Minute)
		switch call {
		case 1, 3: // work claims step 1, twice
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2, 4: // verifier rejects it both times
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 5: // the replan changes nothing, so the run stalls out
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, clock, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected a stalled stop, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	want := "# Run stalled at step 1\n\n" +
		"step: \"1. Add the widget.\"\n" +
		"done when: widget tests pass.\n" +
		"rejections: 2 (full entries in FIXES.md)\n" +
		"  1. verifier rejected\n" +
		"  2. verifier rejected\n" +
		"iterations: 5\n" +
		"wall time: 15m of 1h budget\n" +
		"replans used: 1 of 1\n"
	if got, err := fs.Read("STALLED.md"); err != nil || got != want {
		t.Fatalf("expected the stall handoff report (%v):\n%s\ngot:\n%s", err, want, got)
	}
	report := readRunReport(t, fs)
	assertReportFields(t, report, map[string]any{
		"outcome":    "stalled",
		"report":     "STALLED.md",
		"stuck_step": float64(1),
	})
}

func TestStallHandoffNamesCheckCommandRejections(t *testing.T) {
	cfg := config(0)
	cfg.CheckCmd = "go test ./..."
	cfg.MaxStalledIterations = 1
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // the check command fails; the gate rejects the step
			return errors.New("exit status 1")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected a stalled stop, got %v", outcome)
	}
	handoff, err := fs.Read("STALLED.md")
	if err != nil {
		t.Fatalf("expected STALLED.md to be written: %v", err)
	}
	for _, want := range []string{
		"# Run stalled at step 1",
		"  1. check command failed: go test ./...",
		"wall time: 0s (no budget)",
	} {
		if !strings.Contains(handoff, want) {
			t.Fatalf("expected the handoff to contain %q, got:\n%s", want, handoff)
		}
	}
	if strings.Contains(handoff, "replans used") {
		t.Fatalf("expected no replan line with replanning disabled, got:\n%s", handoff)
	}
}

func TestStallHandoffListsStashedAttempts(t *testing.T) {
	cfg := stashConfig()
	cfg.MaxStalledIterations = 2
	fs := stepsFileStore()
	fs.Write(".git", "")
	runner := &fakeRunner{}
	runner.script = func(call int, out io.Writer) error {
		switch call {
		case 1: // startup: git status finds the tree clean
		case 2, 4: // work claims step 1, twice
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 3, 5: // verifier rejects it both times; the second rejection stashes
			fs.Write("STEPS.md", twoStepsNoneChecked)
		case 6: // git rev-parse -q --verify refs/stash: no stash exists yet
			return errors.New("exit status 1")
		case 7: // git stash push succeeds
		case 8: // git rev-parse refs/stash yields the new stash's hash
			fmt.Fprintln(out, "aaa111")
		case 9: // git stash show --stat
			fmt.Fprintln(out, " widget.go | 5 +\n 1 file changed, 5 insertions(+)")
		}
		return nil
	}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled {
		t.Fatalf("expected the stashed round to end as a stall, got %v", outcome)
	}
	handoff, err := fs.Read("STALLED.md")
	if err != nil {
		t.Fatalf("expected STALLED.md to be written: %v", err)
	}
	if !strings.Contains(handoff, "stashed attempts:\n  aaa111  1 file changed, 5 insertions(+)\n") {
		t.Fatalf("expected the handoff to list the stash hash and diffstat, got:\n%s", handoff)
	}
}

func TestStallHandoffAbsentOnSuccessAndStaleOneRemovedAtStartup(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("STALLED.md", "# stale handoff from a previous run\n")
	staleAtFirstIteration := false
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			staleAtFirstIteration = fs.Exists("STALLED.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if staleAtFirstIteration {
		t.Fatal("expected the stale STALLED.md removed at startup")
	}
	if fs.Exists("STALLED.md") {
		t.Fatal("expected no STALLED.md after a successful run")
	}
	report := readRunReport(t, fs)
	if _, ok := report["report"]; ok {
		t.Fatalf("expected the report field omitted from a clean run, got %v", report["report"])
	}
}

func TestStallHandoffLeftMidRunIsRemovedOnNonStallExit(t *testing.T) {
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // a misbehaving tool invocation creates its own STALLED.md
			fs.Write("STALLED.md", "# not the orchestrator's\n")
		case 2: // the run aborts on a tool failure
			return errors.New("droid: rate limited")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed {
		t.Fatalf("expected a tool-failure abort, got %v", outcome)
	}
	if fs.Exists("STALLED.md") {
		t.Fatal("expected the mid-run STALLED.md removed on a non-stall exit")
	}
	report := readRunReport(t, fs)
	if _, ok := report["report"]; ok {
		t.Fatalf("expected the report field omitted from a failed run, got %v", report["report"])
	}
}

func TestStallHandoffWriteFailureDoesNotChangeTheOutcome(t *testing.T) {
	cfg := config(0)
	cfg.MaxStalledIterations = 1
	fs := &failingWriteStore{fakeFileStore: stepsFileStore(), failPath: "STALLED.md"}
	var terminal bytes.Buffer
	runner := &fakeRunner{} // never checks a step
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected the failed handoff write to leave the outcome alone, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if !strings.Contains(terminal.String(), "could not write STALLED.md") {
		t.Fatalf("expected a terminal warning about the failed handoff write, got:\n%s", terminal.String())
	}
	report := readRunReport(t, fs)
	if _, ok := report["report"]; ok {
		t.Fatalf("expected no report field when STALLED.md was not written, got %v", report["report"])
	}
}

func TestRunAbortsWhenStepsFileVanishesMidRun(t *testing.T) {
	fs := stepsFileStore()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Remove("STEPS.md")
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort when STEPS.md cannot be read, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 1 {
		t.Fatalf("expected the loop to abort once STEPS.md is unreadable, got %d runs", runner.calls)
	}
}
