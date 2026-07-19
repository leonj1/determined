package services_test

import (
	"context"
	"fmt"
	"io"
	"reflect"
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
	quiz        []models.QuizQuestion
	// entries holds each opened execution log entry's message, indexed as the
	// orchestrator indexes them, and states holds the outcome each entry was
	// settled with, so tests can assert which entry got which state.
	entries []string
	states  map[int]models.EntryState
}

// stateOf reports the outcome the entry at i was settled with, or the empty
// state when it was never settled.
func (r *fakeExecReporter) stateOf(i int) models.EntryState { return r.states[i] }

func (r *fakeExecReporter) Progress(message string) {
	r.events = append(r.events, "progress: "+message)
}
func (r *fakeExecReporter) StartExecution() { r.events = append(r.events, "start-execution") }
func (r *fakeExecReporter) BeginExecLogEntry(message string) int {
	r.events = append(r.events, "exec-log-entry: "+message)
	r.entries = append(r.entries, message)
	return len(r.entries) - 1
}
func (r *fakeExecReporter) SettleExecLogEntryAt(i int, state models.EntryState) {
	if r.states == nil {
		r.states = map[int]models.EntryState{}
	}
	r.states[i] = state
	r.events = append(r.events, fmt.Sprintf("settle-exec-log-entry %d: %s", i, state))
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
func (r *fakeExecReporter) StartQuiz() { r.events = append(r.events, "start-quiz") }
func (r *fakeExecReporter) SetQuiz(questions []models.QuizQuestion) {
	r.quiz = questions
	r.events = append(r.events, "set-quiz")
}
func (r *fakeExecReporter) FinishQuiz(succeeded bool) {
	if succeeded {
		r.events = append(r.events, "finish-quiz: succeeded")
	} else {
		r.events = append(r.events, "finish-quiz: failed")
	}
}

// execConfig disables the review gates so tests exercise pure step reporting.
func execConfig() models.Config {
	cfg := config(0)
	cfg.Verify = false
	cfg.SpecializedReviews = false
	cfg.ExplanationFile = "EXPLANATION.md"
	cfg.QuizFile = "QUIZ.json"
	return cfg
}

var validQuizQuestions = []models.QuizQuestion{
	{Question: "What was added?", Choices: []string{"A widget", "A server", "A queue", "A cache"}, CorrectIndex: 0, Rationale: "The diff adds the widget."},
	{Question: "What confirms completion?", Choices: []string{"A comment", "The tests", "A timer", "A flag"}, CorrectIndex: 1, Rationale: "The tests exercise the completed widget."},
	{Question: "Where is behavior enabled?", Choices: []string{"README", "widget.go", "PLAN.md", "STOP.md"}, CorrectIndex: 1, Rationale: "The implementation change is in widget.go."},
	{Question: "Why show a diff?", Choices: []string{"To deploy", "To configure", "To highlight changes", "To erase history"}, CorrectIndex: 2, Rationale: "The diff highlights the important implementation."},
	{Question: "What remains unchanged?", Choices: []string{"The widget", "The design", "The tests", "The explanation"}, CorrectIndex: 1, Rationale: "The design is preserved while behavior is enabled."},
}

const validQuizJSON = `{"questions":[{"question":"What was added?","choices":["A widget","A server","A queue","A cache"],"correctIndex":0,"rationale":"The diff adds the widget."},{"question":"What confirms completion?","choices":["A comment","The tests","A timer","A flag"],"correctIndex":1,"rationale":"The tests exercise the completed widget."},{"question":"Where is behavior enabled?","choices":["README","widget.go","PLAN.md","STOP.md"],"correctIndex":1,"rationale":"The implementation change is in widget.go."},{"question":"Why show a diff?","choices":["To deploy","To configure","To highlight changes","To erase history"],"correctIndex":2,"rationale":"The diff highlights the important implementation."},{"question":"What remains unchanged?","choices":["The widget","The design","The tests","The explanation"],"correctIndex":1,"rationale":"The design is preserved while behavior is enabled."}]}`

// The status page paints each execution entry by the outcome the orchestrator
// settles it with, so these tests pin the state each kind of invocation earns:
// a clean run is ok, a failing run is error, and the two warning signals the
// orchestrator already detects — protected-file tampering and a verifier
// rejecting the step it reviewed — are warn rather than error, since the work
// completed but cannot be trusted as reported.

func TestFailedInvocationSettlesItsEntryAsError(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	cfg := execConfig()
	cfg.MaxConsecutiveFailures = 1 // the cap ends the run after the one failure
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		return fmt.Errorf("tool crashed")
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	o.Run(context.Background())

	if got := reporter.stateOf(0); got != models.EntryStateError {
		t.Errorf("entry 0 state = %q, want %q for an invocation that failed to run",
			got, models.EntryStateError)
	}
}

func TestTamperingInvocationSettlesItsEntryAsWarn(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	fs.Write("CRITERIA.md", "the original criteria\n")
	cfg := execConfig()
	cfg.ProtectedFiles = []string{"CRITERIA.md"}
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		if call == 1 {
			fs.Write("CRITERIA.md", "criteria rewritten to match the code\n")
			fs.Write("STEPS.md", "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n")
		}
		if call == 2 {
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	o.Run(context.Background())

	if got := reporter.stateOf(0); got != models.EntryStateWarn {
		t.Errorf("entry 0 state = %q, want %q: the step ran but rewrote a protected file",
			got, models.EntryStateWarn)
	}
	// The audit that follows tampered with nothing, so it stays clean: the
	// warning marks the offending invocation only, not the rest of the run.
	if got := reporter.stateOf(1); got != models.EntryStateOK {
		t.Errorf("entry 1 state = %q, want %q for the untainted audit that followed",
			got, models.EntryStateOK)
	}
}

func TestVerifierRejectionSettlesTheVerifyEntryAsWarn(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	cfg := execConfig()
	cfg.Verify = true
	// A rejection leaves the completed count unchanged, which the loop treats
	// as a stalled iteration; the cap is what ends this run rather than the
	// worker and verifier ping-ponging over the same step forever.
	cfg.MaxStalledIterations = 1
	checked := "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n"
	unchecked := "- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n"
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1: // the worker claims the step is done
			fs.Write("STEPS.md", checked)
		case 2: // the verifier disagrees and unchecks it
			fs.Write("STEPS.md", unchecked)
		}
		return nil
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	o.Run(context.Background())

	if got := reporter.stateOf(1); got != models.EntryStateWarn {
		t.Errorf("verify entry state = %q, want %q: the verifier rejected the step",
			got, models.EntryStateWarn)
	}
}

func TestVerifierApprovalLeavesTheVerifyEntryOK(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	cfg := execConfig()
	cfg.Verify = true
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1:
			fs.Write("STEPS.md", "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n")
		case 2: // the verifier approves, leaving the box checked
		default:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	reporter := &fakeExecReporter{}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg).
		WithStatusReporter(reporter)

	o.Run(context.Background())

	if got := reporter.stateOf(1); got != models.EntryStateOK {
		t.Errorf("verify entry state = %q, want %q when the verifier approved",
			got, models.EntryStateOK)
	}
}

func TestExecuteRunReportsFullStatusSequence(t *testing.T) {
	fs := plannedFileStore("- [ ] 1. Add the widget.\n  Done when: widget tests pass.\n")
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1:
			io.WriteString(out, "widget added\n")
			fs.Write("STEPS.md", "- [x] 1. Add the widget.\n  Done when: widget tests pass.\n")
		case 2: // the docs update
		case 3: // the whole-plan audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		case 4:
			fs.Write("EXPLANATION.md", "The widget now works.\n\n```diff\n--- a/widget.go\n+++ b/widget.go\n+enabled\n```")
		case 5:
			fs.Write("QUIZ.json", validQuizJSON)
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
		"settle-exec-log-entry 0: ok",
		"task-steps",
		"progress: updating project documentation",
		"exec-log-entry: updating project documentation",
		"settle-exec-log-entry 1: ok",
		"progress: auditing the whole plan",
		"exec-log-entry: auditing the whole plan",
		"settle-exec-log-entry 2: ok",
		"task-steps",
		"finish-execution: succeeded",
		"start-explanation",
		"progress: explaining the changes",
		"exec-log-entry: explaining the changes",
		"settle-exec-log-entry 3: ok",
		"set-explanation",
		"finish-explanation: succeeded",
		"start-quiz",
		"progress: writing the quiz",
		"exec-log-entry: writing the quiz",
		"settle-exec-log-entry 4: ok",
		"set-quiz",
		"finish-quiz: succeeded",
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
	if !reflect.DeepEqual(reporter.quiz, validQuizQuestions) {
		t.Errorf("quiz = %+v, want %+v", reporter.quiz, validQuizQuestions)
	}
	if runner.calls != 5 {
		t.Fatalf("tool calls = %d, want work, docs, audit, explanation, and quiz", runner.calls)
	}
	if prompt := runner.prompt(4); !strings.Contains(prompt, "Write only EXPLANATION.md") {
		t.Errorf("explanation prompt = %q, want configured filename", prompt)
	}
	if prompt := runner.prompt(5); !strings.Contains(prompt, "Write only QUIZ.json") ||
		!strings.Contains(prompt, "Read EXPLANATION.md") {
		t.Errorf("quiz prompt = %q, want configured artifact names", prompt)
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
	if events := strings.Join(reporter.events, "\n"); strings.Contains(events, "explanation") || strings.Contains(events, "quiz") {
		t.Errorf("events = %q, want no explanation or quiz after failed execution", reporter.events)
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
	if strings.Contains(strings.Join(reporter.events, "\n"), "quiz") {
		t.Errorf("events = %q, want no quiz after failed explanation", reporter.events)
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
	if strings.Contains(strings.Join(reporter.events, "\n"), "quiz") {
		t.Errorf("events = %q, want no quiz without an explanation file", reporter.events)
	}
}

func TestInvalidQuizArtifactDoesNotChangeSuccessfulRun(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "invalid JSON", json: `{"questions":`},
		{name: "wrong question count", json: `{"questions":[]}`},
		{name: "correct index out of range", json: strings.Replace(validQuizJSON, `"correctIndex":0`, `"correctIndex":4`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fs := plannedFileStore("- [x] 1. Add the widget.\n")
			fs.Write("STOP.md", "audit: plan satisfied")
			runner := &fakeRunner{script: func(call int, _ io.Writer) error {
				if call == 1 {
					fs.Write("EXPLANATION.md", "The widget now works.")
				}
				if call == 2 {
					fs.Write("QUIZ.json", test.json)
				}
				return nil
			}}
			reporter := &fakeExecReporter{}
			o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, execConfig()).WithStatusReporter(reporter)

			if outcome := o.Run(context.Background()); outcome != models.OutcomeStopped {
				t.Fatalf("outcome = %v, want successful execution", outcome)
			}
			if got := reporter.events[len(reporter.events)-1]; got != "finish-quiz: failed" {
				t.Errorf("last event = %q, want failed quiz", got)
			}
			if reporter.quiz != nil {
				t.Errorf("quiz = %+v, want no published questions", reporter.quiz)
			}
		})
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
