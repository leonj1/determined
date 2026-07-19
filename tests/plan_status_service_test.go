package tests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// steppingClock is a controllable clock that advances on demand.
type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time               { return c.now }
func (c *steppingClock) advance(d time.Duration)      { c.now = c.now.Add(d) }
func newSteppingClock(start time.Time) *steppingClock { return &steppingClock{now: start} }

func planStart() time.Time { return time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC) }

func TestPlanStatusServiceInitialSnapshotCarriesGitContext(t *testing.T) {
	git := models.GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"}
	service := services.NewPlanStatusService(newSteppingClock(planStart()), git)

	snapshot := service.Snapshot()
	if snapshot.Git != git {
		t.Errorf("git = %+v, want %+v", snapshot.Git, git)
	}
	if snapshot.Phase != models.PlanPhaseRunning {
		t.Errorf("phase = %q, want running", snapshot.Phase)
	}
	if len(snapshot.Steps) != 0 {
		t.Errorf("steps = %+v, want empty", snapshot.Steps)
	}
}

func TestPlanStatusServiceStepsAreOrderedAndTimestamped(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{Remote: "no remote", Branch: "master"})

	service.AddStep("writing planning goal")
	clock.advance(time.Minute)
	service.AddStep("planning project")

	steps := service.Snapshot().Steps
	if len(steps) != 2 {
		t.Fatalf("steps = %+v, want 2 entries", steps)
	}
	if steps[0].Message != "writing planning goal" || steps[1].Message != "planning project" {
		t.Errorf("step order wrong: %+v", steps)
	}
	if !steps[0].At.Equal(planStart()) || !steps[1].At.Equal(planStart().Add(time.Minute)) {
		t.Errorf("timestamps wrong: %+v", steps)
	}
}

func TestPlanStatusServiceLateSubscriberReceivesCurrentState(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.SetGoal("build a todo CLI")
	service.AddStep("planning project")

	snapshots, cancel := service.Subscribe()
	defer cancel()

	first := <-snapshots
	if first.Goal != "build a todo CLI" {
		t.Errorf("late subscriber goal = %q, want the current goal", first.Goal)
	}
	if len(first.Steps) != 1 {
		t.Errorf("late subscriber steps = %+v, want the step so far", first.Steps)
	}
}

func TestPlanStatusServiceBroadcastsUpdatesToSubscribers(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	service.SetPlan("# Plan")

	updated := <-snapshots
	if updated.Plan != "# Plan" {
		t.Errorf("broadcast plan = %q, want %q", updated.Plan, "# Plan")
	}
}

func TestPlanStatusServiceBroadcastsTests(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	service.SetTests("### Test 1: journey")

	updated := <-snapshots
	if updated.Tests != "### Test 1: journey" {
		t.Errorf("broadcast tests = %q, want %q", updated.Tests, "### Test 1: journey")
	}
}

func TestPlanStatusServiceBroadcastsTaskSteps(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots // drain the primed snapshot

	steps := []models.TaskStep{
		{Text: "scaffold the CLI", DoneWhen: "`go build` passes.", Completed: true},
		{Text: "add the todo store"},
	}
	service.SetTaskSteps(steps)

	updated := <-snapshots
	if len(updated.TaskSteps) != 2 {
		t.Fatalf("task steps = %+v, want 2 entries", updated.TaskSteps)
	}
	for i, want := range steps {
		if updated.TaskSteps[i] != want {
			t.Errorf("task steps[%d] = %+v, want %+v", i, updated.TaskSteps[i], want)
		}
	}
}

func TestPlanStatusServiceFinishRecordsTimingAndPhase(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{})
	service.Start()
	clock.advance(5 * time.Minute)
	service.Finish(true)

	snapshot := service.Snapshot()
	if snapshot.Phase != models.PlanPhaseSucceeded {
		t.Errorf("phase = %q, want succeeded", snapshot.Phase)
	}
	if !snapshot.StartedAt.Equal(planStart()) {
		t.Errorf("startedAt = %v, want %v", snapshot.StartedAt, planStart())
	}
	if !snapshot.EndedAt.Equal(planStart().Add(5 * time.Minute)) {
		t.Errorf("endedAt = %v, want start+5m", snapshot.EndedAt)
	}
	if got := snapshot.Duration(clock.Now()); got != 5*time.Minute {
		t.Errorf("duration = %v, want 5m", got)
	}
}

func TestPlanStatusServiceFinishFailureMarksFailedPhase(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.Start()
	service.Finish(false)

	if phase := service.Snapshot().Phase; phase != models.PlanPhaseFailed {
		t.Errorf("phase = %q, want failed", phase)
	}
}

func TestPlanStatusServiceLogEntriesAccumulateStreamedOutput(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{})

	service.BeginLogEntry("planning project")
	service.AppendLogOutput("first line\n")
	service.AppendLogOutput("second line\n")
	clock.advance(time.Minute)
	service.BeginLogEntry("assessing plan")
	service.AppendLogOutput("findings written\n")

	log := service.Snapshot().Log
	if len(log) != 2 {
		t.Fatalf("log = %+v, want 2 entries", log)
	}
	first := models.LogEntry{At: planStart(), Message: "planning project", Body: "first line\nsecond line\n"}
	second := models.LogEntry{At: planStart().Add(time.Minute), Message: "assessing plan", Body: "findings written\n"}
	if log[0] != first {
		t.Errorf("log[0] = %+v, want %+v", log[0], first)
	}
	if log[1] != second {
		t.Errorf("log[1] = %+v, want %+v", log[1], second)
	}
}

func TestPlanStatusServiceLogOutputWithoutEntryIsDropped(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})

	service.AppendLogOutput("stray output\n")

	if log := service.Snapshot().Log; len(log) != 0 {
		t.Errorf("log = %+v, want empty when no entry is open", log)
	}
}

func TestPlanStatusServiceWaitForInputSetsFlagAndVisibleStep(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.WaitForInput()

	snapshot := service.Snapshot()
	if !snapshot.WaitingForInput {
		t.Error("waitingForInput = false, want true")
	}
	if len(snapshot.Steps) != 1 || snapshot.Steps[0].Message != "waiting for input on the terminal" {
		t.Errorf("steps = %+v, want a waiting step", snapshot.Steps)
	}

	service.AddStep("planning project")
	if service.Snapshot().WaitingForInput {
		t.Error("waitingForInput still true after next step")
	}
}

func pageAnnotation(comment string) models.Annotation {
	return models.Annotation{
		At:      planStart(),
		Section: models.AnnotationSectionPlan,
		Target:  "Step 1",
		Comment: comment,
	}
}

func TestPlanStatusServiceQueuesAnnotationsInOrder(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})

	service.SubmitAnnotation(pageAnnotation("first"))
	service.SubmitAnnotation(pageAnnotation("second"))

	pending := service.Snapshot().PendingAnnotations
	if len(pending) != 2 || pending[0].Comment != "first" || pending[1].Comment != "second" {
		t.Fatalf("pending = %+v, want first then second", pending)
	}

	taken, ok := service.TakeAnnotation()
	if !ok || taken.Comment != "first" {
		t.Fatalf("take = %+v ok=%v, want first", taken, ok)
	}
	if remaining := service.Snapshot().PendingAnnotations; len(remaining) != 1 || remaining[0].Comment != "second" {
		t.Errorf("remaining = %+v, want only second", remaining)
	}
}

func TestPlanStatusServiceTakeAnnotationOnEmptyQueueReportsNone(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	if _, ok := service.TakeAnnotation(); ok {
		t.Error("take on empty queue reported an annotation")
	}
}

func TestPlanStatusServiceSubmitSignalsOncePerBurst(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})

	service.SubmitAnnotation(pageAnnotation("first"))
	service.SubmitAnnotation(pageAnnotation("second"))

	select {
	case <-service.AnnotationSignal():
	default:
		t.Fatal("signal channel empty after submissions")
	}
	select {
	case <-service.AnnotationSignal():
		t.Fatal("signal fired twice for one burst; drain loops would spin")
	default:
	}
}

// implementReadyService returns a service whose session succeeded with the
// Implement button offered, the state a plan-only interactive run holds in.
func implementReadyService(clock *steppingClock) *services.PlanStatusService {
	service := services.NewPlanStatusService(clock, models.GitContext{})
	service.Start()
	service.Finish(true)
	service.OfferImplement()
	return service
}

func TestPlanStatusServiceOfferImplementIsVisibleInSnapshot(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	if service.Snapshot().ImplementOffered {
		t.Fatal("implementOffered = true before the offer")
	}
	service.OfferImplement()
	if !service.Snapshot().ImplementOffered {
		t.Error("implementOffered = false after the offer")
	}
}

func TestPlanStatusServiceImplementRequestSignalsOnce(t *testing.T) {
	service := implementReadyService(newSteppingClock(planStart()))

	service.RequestImplement()
	service.RequestImplement() // double click

	if phase := service.Snapshot().ExecPhase; phase != models.ExecPhaseRequested {
		t.Fatalf("execPhase = %q, want requested", phase)
	}
	select {
	case <-service.ImplementSignal():
	default:
		t.Fatal("implement signal empty after a request")
	}
	select {
	case <-service.ImplementSignal():
		t.Fatal("implement signal fired twice; two execute runs would start")
	default:
	}
}

func TestPlanStatusServiceImplementRequestIgnoredUntilOfferedAndSucceeded(t *testing.T) {
	unoffered := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	unoffered.Start()
	unoffered.Finish(true)
	unoffered.RequestImplement()
	if phase := unoffered.Snapshot().ExecPhase; phase != "" {
		t.Errorf("execPhase without an offer = %q, want empty", phase)
	}

	failed := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	failed.OfferImplement()
	failed.Start()
	failed.Finish(false)
	failed.RequestImplement()
	if phase := failed.Snapshot().ExecPhase; phase != "" {
		t.Errorf("execPhase after failed planning = %q, want empty", phase)
	}
	select {
	case <-failed.ImplementSignal():
		t.Error("implement signal fired for an ignored request")
	default:
	}
}

func TestPlanStatusServiceExecutionLifecycleRecordsPhaseAndTiming(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := implementReadyService(clock)
	service.RequestImplement()

	clock.advance(time.Minute)
	service.StartExecution()
	clock.advance(10 * time.Minute)
	service.FinishExecution(true)

	snapshot := service.Snapshot()
	if snapshot.ExecPhase != models.ExecPhaseSucceeded {
		t.Errorf("execPhase = %q, want succeeded", snapshot.ExecPhase)
	}
	if !snapshot.ExecStartedAt.Equal(planStart().Add(time.Minute)) {
		t.Errorf("execStartedAt = %v, want start+1m", snapshot.ExecStartedAt)
	}
	if !snapshot.ExecEndedAt.Equal(planStart().Add(11 * time.Minute)) {
		t.Errorf("execEndedAt = %v, want start+11m", snapshot.ExecEndedAt)
	}
}

func TestPlanStatusServiceExecutionFailureMarksFailedPhase(t *testing.T) {
	service := implementReadyService(newSteppingClock(planStart()))
	service.StartExecution()
	service.FinishExecution(false)

	if phase := service.Snapshot().ExecPhase; phase != models.ExecPhaseFailed {
		t.Errorf("execPhase = %q, want failed", phase)
	}
}

func TestPlanStatusServicePublishesExplanationLifecycle(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots

	service.StartExplanation()
	service.SetExplanation("The change keeps execution visible.\n\n```diff\n+new behavior\n```")
	service.FinishExplanation(true)

	running := <-snapshots
	published := <-snapshots
	finished := <-snapshots
	if running.ExplainPhase != models.ExplainPhaseRunning {
		t.Errorf("running phase = %q, want running", running.ExplainPhase)
	}
	if published.Explanation != "The change keeps execution visible.\n\n```diff\n+new behavior\n```" {
		t.Errorf("published explanation = %q", published.Explanation)
	}
	if finished.ExplainPhase != models.ExplainPhaseSucceeded {
		t.Errorf("finished phase = %q, want succeeded", finished.ExplainPhase)
	}
}

func TestPlanStatusServiceReportsExplanationFailure(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.StartExplanation()
	service.FinishExplanation(false)

	snapshot := service.Snapshot()
	if snapshot.ExplainPhase != models.ExplainPhaseFailed {
		t.Errorf("explainPhase = %q, want failed", snapshot.ExplainPhase)
	}
	if snapshot.Explanation != "" {
		t.Errorf("explanation = %q, want empty", snapshot.Explanation)
	}
}

func TestPlanStatusServicePublishesQuizLifecycle(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots
	questions := []models.QuizQuestion{{
		Question: "What changed?", Choices: []string{"A", "B", "C", "D"},
		CorrectIndex: 2, Rationale: "C describes the diff.",
	}}

	service.StartQuiz()
	service.SetQuiz(questions)
	service.FinishQuiz(true)

	if running := <-snapshots; running.QuizPhase != models.QuizPhaseRunning {
		t.Errorf("running phase = %q, want running", running.QuizPhase)
	}
	if published := <-snapshots; len(published.Quiz) != 1 || published.Quiz[0].Question != "What changed?" {
		t.Errorf("published quiz = %+v", published.Quiz)
	}
	if finished := <-snapshots; finished.QuizPhase != models.QuizPhaseSucceeded {
		t.Errorf("finished phase = %q, want succeeded", finished.QuizPhase)
	}
}

func TestPlanStatusServiceReportsQuizFailure(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.StartQuiz()
	service.FinishQuiz(false)

	snapshot := service.Snapshot()
	if snapshot.QuizPhase != models.QuizPhaseFailed {
		t.Errorf("quizPhase = %q, want failed", snapshot.QuizPhase)
	}
	if snapshot.Quiz != nil {
		t.Errorf("quiz = %+v, want empty", snapshot.Quiz)
	}
}

func TestPlanStatusServiceQuizJSONUsesPublicFieldNames(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.SetQuiz([]models.QuizQuestion{{
		Question: "What changed?", Choices: []string{"A", "B", "C", "D"},
		CorrectIndex: 2, Rationale: "C describes the diff.",
	}})
	service.FinishQuiz(true)

	encoded, err := json.Marshal(service.Snapshot())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	statusJSON := string(encoded)
	for _, field := range []string{`"quiz":[`, `"quizPhase":"succeeded"`, `"correctIndex":2`} {
		if !strings.Contains(statusJSON, field) {
			t.Errorf("status JSON %s missing %s", statusJSON, field)
		}
	}
}

func TestPlanStatusServiceExecLogAccumulatesSeparatelyFromPlanLog(t *testing.T) {
	clock := newSteppingClock(planStart())
	service := services.NewPlanStatusService(clock, models.GitContext{})
	service.BeginLogEntry("planning project")
	service.AppendLogOutput("plan output\n")

	service.BeginExecLogEntry("executing step 1: add the widget")
	service.AppendExecLogOutput("widget built\n")
	service.AppendExecLogOutput("tests pass\n")

	snapshot := service.Snapshot()
	want := models.LogEntry{At: planStart(), Message: "executing step 1: add the widget", Body: "widget built\ntests pass\n"}
	if len(snapshot.ExecLog) != 1 || snapshot.ExecLog[0] != want {
		t.Errorf("execLog = %+v, want exactly %+v", snapshot.ExecLog, want)
	}
	if len(snapshot.Log) != 1 || snapshot.Log[0].Body != "plan output\n" {
		t.Errorf("plan log = %+v, want it untouched by exec output", snapshot.Log)
	}
}

func TestPlanStatusServiceExecOutputWithoutEntryIsDropped(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	service.AppendExecLogOutput("stray output\n")
	if log := service.Snapshot().ExecLog; len(log) != 0 {
		t.Errorf("execLog = %+v, want empty when no entry is open", log)
	}
}

func TestPlanStatusServiceBroadcastsAnnotationQueueChanges(t *testing.T) {
	service := services.NewPlanStatusService(newSteppingClock(planStart()), models.GitContext{})
	updates, cancel := service.Subscribe()
	defer cancel()
	<-updates // initial snapshot

	service.SubmitAnnotation(pageAnnotation("first"))
	snapshot := <-updates
	if len(snapshot.PendingAnnotations) != 1 {
		t.Fatalf("broadcast pending = %+v, want 1 entry", snapshot.PendingAnnotations)
	}

	service.TakeAnnotation()
	snapshot = <-updates
	if len(snapshot.PendingAnnotations) != 0 {
		t.Errorf("broadcast pending after take = %+v, want empty", snapshot.PendingAnnotations)
	}
}
