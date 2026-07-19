package services_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// fakeExecReporter records every execution status event the orchestrator
// emits, in order, so tests can assert the exact reporting sequence.
type fakeExecReporter struct {
	events      []string
	taskSteps   []models.TaskStep
	logOutput   string
	explanation string
}

func (r *fakeExecReporter) Progress(message string) {
	r.events = append(r.events, "progress: "+message)
}
func (r *fakeExecReporter) StartExecution() { r.events = append(r.events, "start-execution") }
func (r *fakeExecReporter) BeginExecLogEntry(message string) {
	r.events = append(r.events, "exec-log-entry: "+message)
}
func (r *fakeExecReporter) AppendExecLogOutput(text string) { r.logOutput += text }
func (r *fakeExecReporter) SetTaskSteps(steps []models.TaskStep) {
	r.taskSteps = steps
	r.events = append(r.events, "task-steps")
}
func (r *fakeExecReporter) FinishExecution(succeeded bool) {
	if succeeded {
		r.events = append(r.events, "finish-execution: succeeded")
	} else {
		r.events = append(r.events, "finish-execution: failed")
	}
}
func (r *fakeExecReporter) StartExplanation() {
	r.events = append(r.events, "start-explanation")
}
func (r *fakeExecReporter) SetExplanation(text string) {
	r.explanation = text
	r.events = append(r.events, "set-explanation")
}
func (r *fakeExecReporter) FinishExplanation(succeeded bool) {
	if succeeded {
		r.events = append(r.events, "finish-explanation: succeeded")
	} else {
		r.events = append(r.events, "finish-explanation: failed")
	}
}

// execConfig disables the review gates so tests exercise pure step reporting.
func execConfig() models.Config {
	cfg := config(0)
	cfg.Verify = false
	cfg.SpecializedReviews = false
	cfg.ExplanationFile = "EXPLANATION.md"
	return cfg
}

func TestExecuteRunReportsFullStatusSequence(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1:
			io.WriteString(out, "widget added\n")
			fs.Write("STEPS.md", "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n")
		case 2: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		case 3:
			fs.Write("EXPLANATION.md", "The widget now works.\n\n```diff\n--- a/widget.go\n+++ b/widget.go\n+enabled\n```")
		}
		return nil
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, execConfig()).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want a clean completion", outcome)
	}
	assertEventSequence(t, reporter.events, []string{
		"start-execution",
		"task-steps",
		"progress: executing step 1: 1. Add the widget.",
		"exec-log-entry: executing step 1: 1. Add the widget.",
		"task-steps",
		"progress: auditing the whole plan",
		"exec-log-entry: auditing the whole plan",
		"task-steps",
		"finish-execution: succeeded",
		"start-explanation",
		"progress: explaining the changes",
		"exec-log-entry: explaining the changes",
		"set-explanation",
		"finish-explanation: succeeded",
	})
	if !strings.Contains(reporter.logOutput, "widget added\n") {
		t.Errorf("exec log output = %q, want the streamed tool output", reporter.logOutput)
	}
	if len(reporter.taskSteps) != 1 || !reporter.taskSteps[0].Completed {
		t.Errorf("final task steps = %+v, want the checked step published", reporter.taskSteps)
	}
	if reporter.explanation != fs.data["EXPLANATION.md"] {
		t.Errorf("explanation = %q, want the generated file exactly", reporter.explanation)
	}
	if runner.calls != 3 {
		t.Fatalf("tool calls = %d, want work, audit, and one explanation", runner.calls)
	}
	if prompt := runner.prompt(3); !strings.Contains(prompt, "Write only EXPLANATION.md") {
		t.Errorf("explanation prompt = %q, want configured filename", prompt)
	}
}

func TestExecuteRunReportsFailureToStatusPage(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n")
	runner := &fakeRunner{script: func(int, io.Writer) error {
		return fmt.Errorf("tool crashed")
	}}
	cfg := execConfig()
	cfg.MaxConsecutiveFailures = 1
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeDroidFailed {
		t.Fatalf("outcome = %v, want a tool failure", outcome)
	}
	last := reporter.events[len(reporter.events)-1]
	if last != "finish-execution: failed" {
		t.Errorf("last event = %q, want the failed finish", last)
	}
	if strings.Contains(strings.Join(reporter.events, "\n"), "explanation") {
		t.Errorf("events = %q, want no explanation after failed execution", reporter.events)
	}
}

func TestExplanationFailureDoesNotChangeSuccessfulRun(t *testing.T) {
	fs := plannedFileStore("- [x] 1. Add the widget.\n")
	fs.Write("STOP.md", "audit: plan satisfied")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		if call == 1 {
			return fmt.Errorf("explanation tool crashed")
		}
		return nil
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, execConfig()).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want successful execution despite explanation failure", outcome)
	}
	if got := reporter.events[len(reporter.events)-1]; got != "finish-explanation: failed" {
		t.Errorf("last event = %q, want failed explanation", got)
	}
	if reporter.explanation != "" {
		t.Errorf("explanation = %q, want empty after invocation failure", reporter.explanation)
	}
}

func TestMissingExplanationFileDoesNotChangeSuccessfulRun(t *testing.T) {
	fs := plannedFileStore("- [x] 1. Add the widget.\n")
	fs.Write("STOP.md", "audit: plan satisfied")
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(&fakeRunner{}, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, execConfig()).
		WithStatusReporter(reporter)

	outcome := o.Run(context.Background())

	if outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want successful execution despite missing explanation", outcome)
	}
	if got := reporter.events[len(reporter.events)-1]; got != "finish-explanation: failed" {
		t.Errorf("last event = %q, want failed explanation", got)
	}
}

func TestExecuteRunWithoutReporterStaysSilent(t *testing.T) {
	fs := plannedFileStore("- [x] 1. Add the widget.\n")
	fs.Write("STOP.md", "audit: plan satisfied")
	o := services.NewOrchestrator(&fakeRunner{}, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, execConfig())

	if outcome := o.Run(context.Background()); outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v, want a clean completion", outcome)
	}
}

func assertEventSequence(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
}
