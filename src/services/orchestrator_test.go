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
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 2 {
		t.Fatalf("expected the tool to run until every box is checked (2 iterations), got %d", runner.calls)
	}
	if len(logs.opened) != 2 || !strings.Contains(logs.opened[0].buf.String(), "working on step 1") {
		t.Fatalf("expected a reviewable per-iteration log for each run, got %d logs", len(logs.opened))
	}
	if !fs.Exists("STOP.md") {
		t.Fatal("expected STOP.md written on completion for compatibility")
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
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected the run to end only when all boxes are checked, got %v", outcome)
	}
	if runner.calls != 2 {
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

func TestRunEndsImmediatelyWhenAllStepsAlreadyChecked(t *testing.T) {
	fs := plannedFileStore("- [x] 1. Done already.\n  Done when: nothing remains.\n")
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected an immediate clean exit, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no work when every step is already checked, got %d invocations", runner.calls)
	}
	if !fs.Exists("STOP.md") {
		t.Fatal("expected STOP.md written on completion for compatibility")
	}
}

func TestRunStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(ctx)

	if outcome != models.OutcomeInterrupted || outcome.ExitCode() != 1 {
		t.Fatalf("expected an interrupted stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
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
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	o.Run(context.Background())

	if runner.calls != 2 {
		t.Fatalf("expected 2 iterations, got %d", runner.calls)
	}
	first := runner.prompt(1)
	for _, want := range []string{
		"Work on exactly this step and no other: 2. Wire the parser into the loop.",
		"Its acceptance criterion: go test ./... passes.",
		"Mark it `[x]` in STEPS.md when done.",
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
