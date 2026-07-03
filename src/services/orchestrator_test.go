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

// fakeStopSignal is an in-memory stand-in for the filesystem sentinel.
type fakeStopSignal struct{ present map[string]bool }

func newFakeStopSignal() *fakeStopSignal          { return &fakeStopSignal{present: map[string]bool{}} }
func (f *fakeStopSignal) Exists(path string) bool { return f.present[path] }
func (f *fakeStopSignal) create(path string)      { f.present[path] = true }

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
		StepsFile: "STEPS.md",
		Tool:      models.DroidTool{},
		Budget:    budget,
	}
}

// stepsFileStore returns a file store seeded with a two-step STEPS.md whose
// first step is still unchecked.
func stepsFileStore() *fakeFileStore {
	fs := newFakeFileStore()
	fs.Write("STEPS.md",
		"- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n\n"+
			"- [ ] 2. Document the widget.\n  Done when: README mentions the widget.\n")
	return fs
}

// --- Functional tests: what can the user achieve? ---

func TestRunCompletesWhenToolSignalsDone(t *testing.T) {
	stop := newFakeStopSignal()
	logs := &fakeLogSink{}
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		fmt.Fprintf(out, "working on step %d\n", call)
		if call == 3 {
			stop.create("STOP.md")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), stop, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected a clean completion, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected the tool to run until done (3 iterations), got %d", runner.calls)
	}
	if len(logs.opened) != 3 || !strings.Contains(logs.opened[0].buf.String(), "working on step 1") {
		t.Fatalf("expected a reviewable per-iteration log for each run, got %d logs", len(logs.opened))
	}
}

func TestRunAbortsWhenToolFails(t *testing.T) {
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 {
			return errors.New("droid: rate limited")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, stepsFileStore(), newFakeStopSignal(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, stepsFileStore(), newFakeStopSignal(), clock, &fakeLogSink{}, io.Discard, config(10*time.Minute))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeBudgetExceeded || outcome.ExitCode() != 1 {
		t.Fatalf("expected a budget stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 3 {
		t.Fatalf("expected the in-flight iteration to finish before stopping, got %d", runner.calls)
	}
}

func TestRunDoesNothingWhenStopFileAlreadyPresent(t *testing.T) {
	stop := newFakeStopSignal()
	stop.create("STOP.md")
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, stepsFileStore(), stop, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected an immediate clean exit, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no work when already stopped, got %d invocations", runner.calls)
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
	o := services.NewOrchestrator(runner, stepsFileStore(), newFakeStopSignal(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(ctx)

	if outcome != models.OutcomeInterrupted || outcome.ExitCode() != 1 {
		t.Fatalf("expected an interrupted stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestEachIterationTargetsTheNextIncompleteStep(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("STEPS.md",
		"- [x] 1. Add the parser.\n  Done when: parser tests pass.\n\n"+
			"- [ ] 2. Wire the parser into the loop.\n  Done when: go test ./... passes.\n\n"+
			"- [ ] 3. Update the docs.\n  Done when: README describes the loop.\n")
	stop := newFakeStopSignal()
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // the tool completes step 2 and checks its box
			fs.Write("STEPS.md",
				"- [x] 1. Add the parser.\n  Done when: parser tests pass.\n\n"+
					"- [x] 2. Wire the parser into the loop.\n  Done when: go test ./... passes.\n\n"+
					"- [ ] 3. Update the docs.\n  Done when: README describes the loop.\n")
		case 2:
			stop.create("STOP.md")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, stop, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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

func TestPromptAsksForStopFileWhenAllStepsChecked(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("STEPS.md", "- [x] 1. Done already.\n  Done when: nothing remains.\n")
	stop := newFakeStopSignal()
	runner := &fakeRunner{script: func(int, io.Writer) error {
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, stop, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if !strings.Contains(runner.prompt(1), "create STOP.md") {
		t.Fatalf("expected the all-checked prompt to ask for STOP.md, got:\n%s", runner.prompt(1))
	}
}

func TestRunAbortsWhenStepsFileUnreadable(t *testing.T) {
	runner := &fakeRunner{}
	o := services.NewOrchestrator(runner, newFakeFileStore(), newFakeStopSignal(), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected an abort when STEPS.md cannot be read, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no tool runs without a readable STEPS.md, got %d", runner.calls)
	}
}
