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

// fakeStepReader serves STEPS.md content to the execute orchestrator.
type fakeStepReader struct{ content string }

func (r fakeStepReader) Read(string) (string, error) {
	if r.content == "" {
		return "", errors.New("missing steps")
	}
	return r.content, nil
}

// fakeRunner runs a scripted behaviour and counts its invocations.
type fakeRunner struct {
	calls  int
	script func(call int, out io.Writer) error
}

func (r *fakeRunner) Run(_ context.Context, _ models.Invocation, out io.Writer) error {
	r.calls++
	if r.script == nil {
		return nil
	}
	return r.script(r.calls, out)
}

func config(budget time.Duration) models.Config {
	return models.Config{
		StopFile:   "STOP.md",
		StepsFile:  "STEPS.md",
		Invocation: models.Invocation{Binary: "droid", Args: []string{"exec", "go"}},
		Budget:     budget,
	}
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
	o := services.NewOrchestrator(runner, stop, fakeStepReader{}, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), fakeStepReader{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), fakeStepReader{}, clock, &fakeLogSink{}, io.Discard, config(10*time.Minute))

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
	o := services.NewOrchestrator(runner, stop, fakeStepReader{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), fakeStepReader{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(ctx)

	if outcome != models.OutcomeInterrupted || outcome.ExitCode() != 1 {
		t.Fatalf("expected an interrupted stop with exit 1, got %v (exit %d)", outcome, outcome.ExitCode())
	}
}

func TestRunEchoesStepProgressAndUpdatesEta(t *testing.T) {
	stop := newFakeStopSignal()
	clock := &fakeClock{now: time.Now()}
	terminal := &bytes.Buffer{}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		clock.advance(time.Duration(call) * time.Minute)
		if call == 3 {
			stop.create("STOP.md")
		}
		return nil
	}}
	steps := fakeStepReader{content: "1. [ ] Add storage\n2. [ ] Add CLI\n3. [ ] Wire up\n"}
	o := services.NewOrchestrator(runner, stop, steps, clock, &fakeLogSink{}, terminal, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	want := []string{
		"Starting Step 1 of 3",
		"Completed Step 1",
		"ETA: 2m0s remaining (2 steps left)",
		"Starting Step 2 of 3",
		"Completed Step 2",
		"ETA: 1m30s remaining (1 step left)",
		"Starting Step 3 of 3",
		"Completed Step 3",
		"ETA: 0s remaining (0 steps left)",
	}
	for _, line := range want {
		if !strings.Contains(terminal.String(), line) {
			t.Fatalf("expected terminal output to contain %q, got:\n%s", line, terminal.String())
		}
	}
}
