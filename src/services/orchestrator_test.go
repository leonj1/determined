package services_test

import (
	"bytes"
	"context"
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
		case 3: // the docs update
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 4 {
		t.Fatalf("expected the tool to run until every box is checked plus the docs update and the audit (4 iterations), got %d", runner.calls)
	}
	if len(logs.opened) != 4 || !strings.Contains(logs.opened[0].buf.String(), "working on step 1") {
		t.Fatalf("expected a reviewable per-iteration log for each run, got %d logs", len(logs.opened))
	}
	if !fs.Exists("STOP.md") {
		t.Fatal("expected STOP.md present on completion")
	}
}

func TestUserCanSeeTimestampedExecutionStages(t *testing.T) {
	step := "Ship widget with automated integration tests today"
	fs := plannedFileStore("- [ ] " + step + "\n  Done when: tests pass.\n")
	cfg := config(0)
	cfg.Verify = true
	clock := &fakeClock{now: time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)}
	logs := &fakeLogSink{}
	var terminal strings.Builder
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			fs.Write("STEPS.md", "- [x] "+step+"\n  Done when: tests pass.\n")
		}
		if call == 5 { // 2 is simplicity, 3 is verify, 4 is the docs update, 5 is the audit
			fs.Write("STOP.md", "approved")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, clock, logs, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected timestamped run to complete, got %v", outcome)
	}
	prefix := "==> [2026-07-11 09:30:00] "
	for _, stage := range []string{
		"executing step 1: Ship widget with automated integration tests",
		"checking simplicity of step 1", "verifying step 1", "auditing the whole plan",
	} {
		if !strings.Contains(terminal.String(), prefix+stage) {
			t.Fatalf("expected visible stage %q, got:\n%s", stage, terminal.String())
		}
	}
	if strings.Contains(terminal.String(), "executing step 1: "+step) {
		t.Fatalf("expected execution status below ten words, got:\n%s", terminal.String())
	}
	for i, log := range logs.opened {
		if !strings.Contains(log.buf.String(), prefix) {
			t.Fatalf("expected timestamp in iteration log %d, got %q", i+1, log.buf.String())
		}
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
		case 3: // the docs update
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the run to end only when all boxes are checked, got %v", outcome)
	}
	if runner.calls != 4 {
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
		case 5: // the docs update
		case 6: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected retries to carry the run to completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 6 {
		t.Fatalf("expected 2 failed attempts, a retry that succeeds, the final step, then the docs update and the audit (6 runs), got %d", runner.calls)
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
		case 3: // the docs update
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	o.Run(context.Background())

	if runner.calls != 4 {
		t.Fatalf("expected 2 work iterations plus the docs update and the audit, got %d", runner.calls)
	}
	first := runner.prompt(1)
	for _, want := range []string{
		"Read NOTES.md if it exists before starting.",
		"Work on exactly this step and no other: 2. Wire the parser into the loop.",
		"Its acceptance criterion: go test ./... passes.",
		"Mark it `[x]` in STEPS.md when done.",
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

func TestStepPurposeIsCarriedIntoWorkAndVerifyPrompts(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	steps := "- [ ] 1. Add message payloads to a queue.\n" +
		"  Purpose: Email messages are throttled to prevent DDOS.\n" +
		"  Done when: burst of 100 sends drains at the configured rate.\n"
	checked := strings.Replace(steps, "- [ ]", "- [x]", 1)
	fs := plannedFileStore(steps)
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check the step
			fs.Write("STEPS.md", checked)
		case 2: // simplicity check approves
		case 3: // verifier approves
		case 4: // docs update
		case 5: // audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	work := runner.prompt(1)
	if !strings.Contains(work, "Its purpose: Email messages are throttled to prevent DDOS.") {
		t.Fatalf("expected the work prompt to carry the step's purpose, got:\n%s", work)
	}
	simplicity := runner.prompt(2)
	if !strings.Contains(simplicity, "Its purpose: Email messages are throttled to prevent DDOS.") {
		t.Fatalf("expected the simplicity prompt to carry the step's purpose, got:\n%s", simplicity)
	}
	verify := runner.prompt(3)
	if !strings.Contains(verify, "Its purpose: Email messages are throttled to prevent DDOS.") {
		t.Fatalf("expected the verifier prompt to carry the step's purpose, got:\n%s", verify)
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
		case 2: // simplicity check approves step 1 by doing nothing
		case 3: // verifier approves step 1 by doing nothing
		case 4: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 5: // simplicity check approves step 2
		case 6: // verifier approves step 2
		case 7: // the docs update
		case 8: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 8 {
		t.Fatalf("expected work + simplicity + verify per step plus the docs update and the audit (8 runs), got %d", runner.calls)
	}
	simplicity := runner.prompt(2)
	for _, want := range []string{
		"Step 1 claims complete: 1. Add the widget.",
		"Acceptance criterion: widget tests pass.",
		"materially simpler solution",
		"FIXES.md",
	} {
		if !strings.Contains(simplicity, want) {
			t.Fatalf("expected the simplicity prompt to contain %q, got:\n%s", want, simplicity)
		}
	}
	verify := runner.prompt(3)
	for _, want := range []string{
		"Step 1 claims complete: 1. Add the widget.",
		"Acceptance criterion: widget tests pass.",
		"Verify by reading the code and running the stated check.",
		"FIXES.md",
	} {
		if !strings.Contains(verify, want) {
			t.Fatalf("expected the verifier prompt to contain %q, got:\n%s", want, verify)
		}
	}
	if !strings.Contains(runner.prompt(4), "2. Document the widget.") {
		t.Fatalf("expected the loop to advance to step 2 after approval, got:\n%s", runner.prompt(4))
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
		case 2: // simplicity check approves
		case 3: // verifier rejects: uncheck step 1 and record why
			fs.Write("STEPS.md", twoStepsNoneChecked)
			fs.Write("FIXES.md", "widget tests actually fail\n")
		case 4: // work re-runs step 1 with FIXES.md present
			fixesAtRerun = fs.Exists("FIXES.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 5: // simplicity check approves again
		case 6: // verifier approves this time
		case 7: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 8: // simplicity check approves
		case 9: // verifier approves
		case 10: // the docs update
		case 11: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the rejected step to be redone and the run to complete, got %v", outcome)
	}
	if runner.calls != 11 {
		t.Fatalf("expected the rejected step to cost an extra work+simplicity+verify round (11 runs with the docs update and the audit), got %d", runner.calls)
	}
	if !strings.Contains(runner.prompt(4), "1. Add the widget.") {
		t.Fatalf("expected the loop to re-run the unchecked step, got:\n%s", runner.prompt(4))
	}
	if !fixesAtRerun {
		t.Fatal("expected FIXES.md to exist when the step is re-run")
	}
}

func TestSimplicityRejectionRerunsTheStepWithoutCorrectnessCheck(t *testing.T) {
	cfg := config(0)
	cfg.Verify = true
	fs := stepsFileStore()
	fixesAtRerun := false
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // work: check step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // simplicity check rejects: uncheck step 1 and record the simpler approach
			fs.Write("STEPS.md", twoStepsNoneChecked)
			fs.Write("FIXES.md", "reuse the existing widget factory instead\n")
		case 3: // work re-runs step 1 with FIXES.md present
			fixesAtRerun = fs.Exists("FIXES.md")
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 4: // simplicity check approves this time
		case 5: // verifier approves
		case 6: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 7: // simplicity check approves
		case 8: // verifier approves
		case 9: // the docs update
		case 10: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the over-complicated step to be redone and the run to complete, got %v", outcome)
	}
	if runner.calls != 10 {
		t.Fatalf("expected the simplicity rejection to skip the correctness check for that round (10 runs), got %d", runner.calls)
	}
	if !strings.Contains(runner.prompt(2), "materially simpler solution") {
		t.Fatalf("expected call 2 to be the simplicity check, got:\n%s", runner.prompt(2))
	}
	rerun := runner.prompt(3)
	if !strings.Contains(rerun, "1. Add the widget.") || strings.Contains(rerun, "claims complete") {
		t.Fatalf("expected the rejected step to go straight back to work, not a reviewer, got:\n%s", rerun)
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
		switch call % 3 {
		case 1: // work always claims step 1
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2: // the simplicity check approves
		case 0: // the verifier always rejects it
			fs.Write("STEPS.md", twoStepsNoneChecked)
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStalled || outcome.ExitCode() != 3 {
		t.Fatalf("expected worker/verifier ping-pong to end as a stall, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 6 {
		t.Fatalf("expected 2 rejected rounds (work + simplicity + verify each) before stalling, got %d", runner.calls)
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
		case 3: // the docs update
		case 4: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if runner.calls != 4 {
		t.Fatalf("expected only the work invocations plus the docs update and the audit with --verify off, got %d", runner.calls)
	}
	for call := 1; call <= 4; call++ {
		if strings.Contains(runner.prompt(call), "claims complete") {
			t.Fatalf("expected no verifier prompts with --verify off, got:\n%s", runner.prompt(call))
		}
	}
}

func TestSpecializedReviewsRunBeforeTheWholePlanAudit(t *testing.T) {
	cfg := config(0)
	cfg.SpecializedReviews = true
	fs := plannedFileStore(twoStepsAllChecked)
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 5 {
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected specialist-approved work to reach a successful audit, got %v", outcome)
	}
	if runner.calls != 5 {
		t.Fatalf("expected the docs update, three specialist reviews, then the audit, got %d runs", runner.calls)
	}
	wants := []string{
		"Update the project's existing documentation so it describes the work as it now stands",
		"independent security specialist",
		"independent performance specialist",
		"independent reliability and maintainability specialist",
		"Audit whether the implementation genuinely satisfies the plan",
	}
	for call, want := range wants {
		if !strings.Contains(runner.prompt(call+1), want) {
			t.Fatalf("review %d should contain %q, got:\n%s", call+1, want, runner.prompt(call+1))
		}
	}
	for _, want := range []string{"reopen the most relevant step", "new unchecked remediation step", "Do not implement fixes"} {
		if !strings.Contains(runner.prompt(2), want) {
			t.Fatalf("specialist prompt should contain %q, got:\n%s", want, runner.prompt(2))
		}
	}
	if !strings.Contains(terminal.String(), "running security review") {
		t.Fatalf("expected the specialist sequence to be announced, got:\n%s", terminal.String())
	}
}

func TestSpecialistFindingBlocksAuditUntilRemediated(t *testing.T) {
	cfg := config(0)
	cfg.SpecializedReviews = true
	fs := plannedFileStore(twoStepsAllChecked)
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the docs update
		case 2: // security review finds an issue and reopens its step
			fs.Write("STEPS.md", twoStepsFirstChecked)
			fs.Write("FIXES.md", "security: documentation example exposes a secret\n")
		case 3: // worker remediates the reopened step
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 8: // docs rerun, all three rerun specialists approve, then the audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected remediation to return through reviews and complete, got %v", outcome)
	}
	if runner.calls != 8 {
		t.Fatalf("expected docs + finding + remediation + docs + three clean reviews + audit, got %d runs", runner.calls)
	}
	if !strings.Contains(runner.prompt(3), "2. Document the widget") {
		t.Fatalf("expected the finding to resume the reopened step, got:\n%s", runner.prompt(3))
	}
	if strings.Contains(runner.prompt(3), "performance specialist") {
		t.Fatal("expected the first finding to block later specialist reviews and the audit")
	}
}

func TestSpecializedReviewsCanBeDisabled(t *testing.T) {
	cfg := config(0)
	cfg.SpecializedReviews = false
	fs := plannedFileStore(twoStepsAllChecked)
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("STOP.md", "audit: plan satisfied")
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || runner.calls != 2 {
		t.Fatalf("expected only the docs update and the general audit when specialist reviews are off, got %v and %d runs", outcome, runner.calls)
	}
	for call := 1; call <= 2; call++ {
		if strings.Contains(runner.prompt(call), "specialist") {
			t.Fatalf("expected no specialist prompt when disabled, got:\n%s", runner.prompt(call))
		}
	}
}

func TestExistingStopCannotBypassEnabledSpecializedReviews(t *testing.T) {
	cfg := config(0)
	cfg.SpecializedReviews = true
	fs := plannedFileStore(twoStepsAllChecked)
	fs.Write("STOP.md", "left by an earlier run")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 5 {
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || runner.calls != 5 {
		t.Fatalf("expected existing STOP.md to be replaced after the docs update and all review gates, got %v and %d runs", outcome, runner.calls)
	}
	if !strings.Contains(runner.prompt(2), "security specialist") {
		t.Fatalf("expected the security review to run first after the docs update, got:\n%s", runner.prompt(2))
	}
}

func TestRetryableSpecialistFailureCannotReachTheAudit(t *testing.T) {
	cfg := config(0)
	cfg.SpecializedReviews = true
	cfg.MaxConsecutiveFailures = 3
	fs := plannedFileStore(twoStepsAllChecked)
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 { // 1 is the docs update; the security review fails
			return errors.New("review tool unavailable")
		}
		if call == 7 {
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || runner.calls != 7 {
		t.Fatalf("expected a fresh docs update and specialist sequence after retryable failure, got %v and %d runs", outcome, runner.calls)
	}
	if !strings.Contains(runner.prompt(2), "security specialist") ||
		!strings.Contains(runner.prompt(4), "security specialist") {
		t.Fatal("expected the security gate to retry before performance or audit")
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
		case 3: // the docs update
		case 4: // audit approves the whole plan
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the audited run to end cleanly, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 4 {
		t.Fatalf("expected 2 work iterations plus 1 docs update and 1 audit, got %d", runner.calls)
	}
	audit := runner.prompt(4)
	for _, want := range []string{
		"Read PLAN.md and STEPS.md.",
		"Audit whether the implementation genuinely satisfies the plan.",
		"If TESTS.md exists, also audit that each of its journey and BDD tests exists as an automated test and passes.",
		"If a required CRITERIA.md or TESTS.md test is missing or failing",
		"append a new `- [ ]` step to STEPS.md with a `Done when:` requiring that test to be implemented and passing",
		"append the reason to FIXES.md",
		"create STOP.md",
	} {
		if !strings.Contains(audit, want) {
			t.Fatalf("expected the audit prompt to contain %q, got:\n%s", want, audit)
		}
	}
	if !strings.Contains(terminal.String(), "updating project documentation") {
		t.Fatalf("expected a terminal note announcing the docs update, got:\n%s", terminal.String())
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
		case 3: // the docs update
		case 4: // audit reopens step 2 and records why
			fs.Write("STEPS.md", twoStepsFirstChecked)
			fs.Write("FIXES.md", "step 2 does not satisfy the plan\n")
		case 5: // work redoes the reopened step with FIXES.md present
			fixesAtRerun = fs.Exists("FIXES.md")
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 6: // the docs update reruns
		case 7: // audit approves this time
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the reopened step to be redone and the run to complete, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 7 {
		t.Fatalf("expected the audit rejection to cost an extra work+docs+audit round (7 runs), got %d", runner.calls)
	}
	if !strings.Contains(runner.prompt(5), "2. Document the widget.") {
		t.Fatalf("expected the loop to resume on the step the audit unchecked, got:\n%s", runner.prompt(5))
	}
	if !fixesAtRerun {
		t.Fatal("expected FIXES.md to exist when the reopened step is re-run")
	}
}

func TestAuditAddedTestsRemediationStepResumesTheLoop(t *testing.T) {
	fs := plannedFileStore(twoStepsAllChecked)
	remediation := "- [ ] 3. Implement TESTS.md Test 1.\n  Done when: Test 1 is implemented and passing.\n"
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the docs update
		case 2: // audit adds the missing TESTS.md test
			fs.Write("STEPS.md", twoStepsAllChecked+"\n"+remediation)
			fs.Write("FIXES.md", "TESTS.md Test 1 is missing\n")
		case 3: // worker implements the audit-added step
			fs.Write("STEPS.md", twoStepsAllChecked+"\n"+strings.Replace(remediation, "[ ]", "[x]", 1))
		case 4: // the docs update reruns
		case 5: // audit approves after remediation
			fs.Write("STOP.md", "audit: required tests pass")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected audit-added test remediation to complete, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 5 || !strings.Contains(runner.prompt(3), "3. Implement TESTS.md Test 1.") {
		t.Fatalf("expected execution to target the audit-added test step, got %d calls and prompt:\n%s", runner.calls, runner.prompt(3))
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
	if runner.calls != 4 {
		t.Fatalf("expected the stall cap to bound repeated docs+audit passes (2 passes, 4 runs), got %d", runner.calls)
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
		case 2: // simplicity check approves step 1
		case 3: // verifier approves step 1
		// 4, 5: git add + git commit checkpoint step 1
		case 6: // work: check step 2
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 7: // simplicity check approves step 2
		case 8: // verifier approves step 2
			// 9, 10: git add + git commit checkpoint step 2
		// 11: the docs update
		case 12: // the whole-plan audit approves
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
	if runner.invocations[3].Binary != "git" || runner.invocations[4].Binary != "git" {
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
		case 4: // work: check step 2 (2-3 and 5-6 are simplicity + verifier approvals)
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 8: // the whole-plan audit approves (7 is the docs update)
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
	if runner.calls != 8 {
		t.Fatalf("expected only work + simplicity + verify per step plus the docs update and the audit (8 runs), got %d", runner.calls)
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
		case 4: // work: check step 2 (2-3 and 5-6 are simplicity + verifier approvals)
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 8: // the whole-plan audit approves (7 is the docs update)
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
		case 2: // simplicity check approves it
		case 3: // verifier rejects it
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
