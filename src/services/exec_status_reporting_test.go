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
	events    []string
	taskSteps []models.TaskStep
	logOutput string
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

// execConfig disables the review gates so tests exercise pure step reporting.
func execConfig() models.Config {
	cfg := config(0)
	cfg.Verify = false
	cfg.SpecializedReviews = false
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
	})
	if !strings.Contains(reporter.logOutput, "widget added\n") {
		t.Errorf("exec log output = %q, want the streamed tool output", reporter.logOutput)
	}
	if len(reporter.taskSteps) != 1 || !reporter.taskSteps[0].Completed {
		t.Errorf("final task steps = %+v, want the checked step published", reporter.taskSteps)
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
