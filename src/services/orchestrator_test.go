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
func (f *fakeStopSignal) remove(path string)      { delete(f.present, path) }

// fakeClock is a controllable clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeLog is an in-memory iteration log.
type fakeLog struct{ buf bytes.Buffer }

func (l *fakeLog) Write(p []byte) (int, error) { return l.buf.Write(p) }
func (l *fakeLog) Close() error                { return nil }

// fakeLogSink records every iteration and verification log it opens.
type fakeLogSink struct {
	opened        []*fakeLog
	verifications []*fakeLog
}

func (s *fakeLogSink) OpenIteration(int) (io.WriteCloser, error) {
	l := &fakeLog{}
	s.opened = append(s.opened, l)
	return l, nil
}

func (s *fakeLogSink) OpenVerification(int) (io.WriteCloser, error) {
	l := &fakeLog{}
	s.verifications = append(s.verifications, l)
	return l, nil
}

// fakeStepStore serves protocol file content to the execute orchestrator.
type fakeStepStore struct {
	files    map[string]string
	err      error
	removed  []string
	onRemove func(path string)
}

func newFakeStepStore(content string) *fakeStepStore {
	return &fakeStepStore{files: map[string]string{"STEPS.md": content}}
}

func (r *fakeStepStore) Read(path string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.files[path], nil
}

func (r *fakeStepStore) Write(path, content string) error {
	if r.files == nil {
		r.files = map[string]string{}
	}
	r.files[path] = content
	return nil
}

func (r *fakeStepStore) Remove(path string) error {
	delete(r.files, path)
	r.removed = append(r.removed, path)
	if r.onRemove != nil {
		r.onRemove(path)
	}
	return nil
}

// fakeRunner runs a scripted behaviour and counts its invocations.
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

// fakeVerifier returns scripted completed-step verification results.
type fakeVerifier struct {
	calls   int
	steps   []models.Step
	results []models.VerificationResult
}

func (v *fakeVerifier) Verify(_ context.Context, step models.Step, out io.Writer) (models.VerificationResult, error) {
	v.calls++
	v.steps = append(v.steps, step)
	fmt.Fprintf(out, "reviewing step %d\n", step.Number)
	if len(v.results) >= v.calls {
		return v.results[v.calls-1], nil
	}
	return models.VerificationResult{Status: models.VerificationPassed, Feedback: "PASS"}, nil
}

// fakeChangeCommitter records completed-task commit attempts.
type fakeChangeCommitter struct {
	calls int
	err   error
}

func (c *fakeChangeCommitter) Commit(_ context.Context, out io.Writer) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	fmt.Fprintln(out, "committed changes")
	return nil
}

func config(budget time.Duration) models.Config {
	return models.Config{
		StopFile:                 "STOP.md",
		StepsFile:                "STEPS.md",
		VerificationFeedbackFile: "VERIFICATION.md",
		Invocation:               models.Invocation{Binary: "droid", Args: []string{"exec", "go"}},
		Budget:                   budget,
		MaxVerificationRetries:   3,
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
	o := services.NewOrchestrator(runner, stop, &fakeChangeCommitter{}, newFakeStepStore(""), &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), &fakeChangeCommitter{}, newFakeStepStore(""), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), &fakeChangeCommitter{}, newFakeStepStore(""), clock, &fakeLogSink{}, io.Discard, config(10*time.Minute))

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
	commits := &fakeChangeCommitter{}
	o := services.NewOrchestrator(runner, stop, commits, newFakeStepStore(""), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped || outcome.ExitCode() != 0 {
		t.Fatalf("expected an immediate clean exit, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if runner.calls != 0 {
		t.Fatalf("expected no work when already stopped, got %d invocations", runner.calls)
	}
	if commits.calls != 0 {
		t.Fatalf("expected no commit when no task was completed, got %d", commits.calls)
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
	o := services.NewOrchestrator(runner, newFakeStopSignal(), &fakeChangeCommitter{}, newFakeStepStore(""), &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

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
	steps := newFakeStepStore("1. [ ] Add storage\n2. [ ] Add CLI\n3. [ ] Wire up\n")
	o := services.NewOrchestrator(runner, stop, &fakeChangeCommitter{}, steps, clock, &fakeLogSink{}, terminal, config(0))

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

func TestRunCommitsRepoChangesWhenToolCompletesATask(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [ ] Add storage\n")
	commits := &fakeChangeCommitter{}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		steps.files["STEPS.md"] = "1. [x] Add storage\n"
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewOrchestrator(runner, stop, commits, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if commits.calls != 1 {
		t.Fatalf("expected completed task changes to be committed once, got %d", commits.calls)
	}
}

func TestRunVerifiesCompletedTaskBeforeCommit(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [ ] Add storage\n")
	commits := &fakeChangeCommitter{}
	verifier := &fakeVerifier{}
	logs := &fakeLogSink{}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		steps.files["STEPS.md"] = "1. [x] Add storage\n"
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewVerifiedOrchestrator(runner, stop, commits, verifier, steps, &fakeClock{now: time.Now()}, logs, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected verified changes to complete cleanly, got %v", outcome)
	}
	if verifier.calls != 1 || commits.calls != 1 {
		t.Fatalf("expected one verification before one commit, got %d verifications and %d commits", verifier.calls, commits.calls)
	}
	if verifier.steps[0].Text != "Add storage" {
		t.Fatalf("expected verifier to review the intended step, got %#v", verifier.steps[0])
	}
	if len(logs.verifications) != 1 || !strings.Contains(logs.verifications[0].buf.String(), "reviewing step 1") {
		t.Fatalf("expected verifier output in a separate log")
	}
}

func TestRunResubmitsRejectedChangesWithVerifierFeedback(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [ ] Add storage\n")
	steps.onRemove = stop.remove
	commits := &fakeChangeCommitter{}
	verifier := &fakeVerifier{results: []models.VerificationResult{
		{Status: models.VerificationFailed, Feedback: "FAIL: missing storage tests"},
		{Status: models.VerificationPassed, Feedback: "PASS"},
	}}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 2 && !strings.Contains(steps.files["VERIFICATION.md"], "missing storage tests") {
			t.Fatalf("expected repair run to receive verifier feedback, got %q", steps.files["VERIFICATION.md"])
		}
		steps.files["STEPS.md"] = "1. [x] Add storage\n"
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewVerifiedOrchestrator(runner, stop, commits, verifier, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected repair to complete cleanly, got %v", outcome)
	}
	if runner.calls != 2 || verifier.calls != 2 || commits.calls != 1 {
		t.Fatalf("expected one repair before commit, got %d runs, %d verifications, %d commits", runner.calls, verifier.calls, commits.calls)
	}
	if _, ok := steps.files["VERIFICATION.md"]; ok {
		t.Fatalf("expected stale verifier feedback to be removed after approval")
	}
}

func TestRunResubmitsWhenToolCompletesTheWrongStep(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [ ] Add storage\n2. [ ] Add CLI\n")
	commits := &fakeChangeCommitter{}
	verifier := &fakeVerifier{}
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			steps.files["STEPS.md"] = "1. [ ] Add storage\n2. [x] Add CLI\n"
			return nil
		}
		if !strings.Contains(steps.files["VERIFICATION.md"], "different STEPS.md item") {
			t.Fatalf("expected wrong-step feedback, got %q", steps.files["VERIFICATION.md"])
		}
		steps.files["STEPS.md"] = "1. [x] Add storage\n2. [ ] Add CLI\n"
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewVerifiedOrchestrator(runner, stop, commits, verifier, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected corrected intended step to complete, got %v", outcome)
	}
	if runner.calls != 2 || verifier.calls != 1 || commits.calls != 1 {
		t.Fatalf("expected wrong-step completion to be repaired before verification, got %d runs, %d verifications, %d commits", runner.calls, verifier.calls, commits.calls)
	}
}

func TestRunAbortsWhenVerifierRejectsTooManyRepairs(t *testing.T) {
	steps := newFakeStepStore("1. [ ] Add storage\n")
	commits := &fakeChangeCommitter{}
	cfg := config(0)
	cfg.MaxVerificationRetries = 1
	verifier := &fakeVerifier{results: []models.VerificationResult{
		{Status: models.VerificationFailed, Feedback: "FAIL: missing tests"},
		{Status: models.VerificationFailed, Feedback: "FAIL: still missing tests"},
	}}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		steps.files["STEPS.md"] = "1. [x] Add storage\n"
		return nil
	}}
	o := services.NewVerifiedOrchestrator(runner, newFakeStopSignal(), commits, verifier, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeVerificationFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected verification failure after retry cap, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if commits.calls != 0 {
		t.Fatalf("expected rejected changes not to be committed, got %d commits", commits.calls)
	}
	if runner.calls != 2 || verifier.calls != 2 {
		t.Fatalf("expected one retry before abort, got %d runs and %d verifications", runner.calls, verifier.calls)
	}
}

func TestRunSkipsCommitWhenTaskProgressCouldNotBeReadBeforeRun(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [x] Add storage\n")
	steps.err = errors.New("steps temporarily unavailable")
	commits := &fakeChangeCommitter{}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		steps.err = nil
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewOrchestrator(runner, stop, commits, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("expected a clean completion, got %v", outcome)
	}
	if commits.calls != 0 {
		t.Fatalf("expected no commit when the before-progress snapshot was unavailable, got %d", commits.calls)
	}
}

func TestRunAbortsWhenCompletedTaskCannotBeCommitted(t *testing.T) {
	stop := newFakeStopSignal()
	steps := newFakeStepStore("1. [ ] Add storage\n")
	commits := &fakeChangeCommitter{err: errors.New("git rejected the commit")}
	runner := &fakeRunner{script: func(int, io.Writer) error {
		steps.files["STEPS.md"] = "1. [x] Add storage\n"
		stop.create("STOP.md")
		return nil
	}}
	o := services.NewOrchestrator(runner, stop, commits, steps, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, config(0))

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeCommitFailed || outcome.ExitCode() != 1 {
		t.Fatalf("expected a commit failure, got %v (exit %d)", outcome, outcome.ExitCode())
	}
	if commits.calls != 1 {
		t.Fatalf("expected one commit attempt, got %d", commits.calls)
	}
}
