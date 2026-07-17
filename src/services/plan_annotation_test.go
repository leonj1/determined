package services_test

import (
	"context"
	"io"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func annotation(section models.AnnotationSection, target, comment string) models.Annotation {
	return models.Annotation{
		At:      time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		Section: section,
		Target:  target,
		Comment: comment,
	}
}

// closedChannel returns an already-dismissed channel so ServeAnnotations
// drains the queue once and exits.
func closedChannel() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestServeAnnotationsAppliesQueuedFeedbackInOrder(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] first step\n")
	fs.Write("TESTS.md", validTestsDoc)
	fs.Write("GOAL.md", "the goal")
	var staged []string
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		content, _ := fs.Read("ANNOTATION.md")
		staged = append(staged, content)
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionPlan, "", "tighten the scope"),
		annotation(models.AnnotationSectionSteps, "Step 1: first step", "split into two steps"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if runner.calls != 2 {
		t.Fatalf("annotate invocations = %d, want 2", runner.calls)
	}
	wantFirst := "# Annotation\n\n**Section:** plan (PLAN.md)\n\n**Requested adjustment:**\n\ntighten the scope\n"
	if staged[0] != wantFirst {
		t.Errorf("first ANNOTATION.md = %q, want %q", staged[0], wantFirst)
	}
	wantSecond := "# Annotation\n\n**Section:** steps (STEPS.md)\n\n**Target:** Step 1: first step\n\n" +
		"**Requested adjustment:**\n\nsplit into two steps\n"
	if staged[1] != wantSecond {
		t.Errorf("second ANNOTATION.md = %q, want %q", staged[1], wantSecond)
	}
	if fs.Exists("ANNOTATION.md") {
		t.Error("ANNOTATION.md should be removed after applying")
	}
	if reporter.plan != "the plan" || reporter.goal != "the goal" {
		t.Errorf("plan/goal not republished: plan=%q goal=%q", reporter.plan, reporter.goal)
	}
}

func TestServeAnnotationsRepublishesAfterEachApplication(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "original plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		fs.Write("PLAN.md", "revised plan")
		return nil
	}}
	reporter := &fakeStatusReporter{annotations: []models.Annotation{
		annotation(models.AnnotationSectionPlan, "", "reword the summary"),
	}}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	o.ServeAnnotations(context.Background(), closedChannel())

	if reporter.plan != "revised plan" {
		t.Errorf("republished plan = %q, want the tool's revision", reporter.plan)
	}
}

func TestServeAnnotationsAppliesLateArrivalsUntilDismissed(t *testing.T) {
	fs := newFakeFileStore()
	fs.Write("PLAN.md", "the plan")
	fs.Write("STEPS.md", "- [ ] a step\n")
	invoked := make(chan struct{}, 1)
	runner := &fakeRunner{script: func(int, io.Writer) error {
		invoked <- struct{}{}
		return nil
	}}
	dismissed := make(chan struct{})
	reporter := &fakeStatusReporter{signal: make(chan struct{}, 1)}
	o := services.NewPlanOrchestrator(runner, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	done := make(chan struct{})
	go func() {
		o.ServeAnnotations(context.Background(), dismissed)
		close(done)
	}()

	// Submit an annotation after the loop is already waiting, the way the
	// status page does during the hold window.
	reporter.mu.Lock()
	reporter.annotations = append(reporter.annotations,
		annotation(models.AnnotationSectionGoal, "", "clarify the audience"))
	reporter.mu.Unlock()
	reporter.signal <- struct{}{}

	select {
	case <-invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("annotation did not trigger an annotate invocation")
	}
	close(dismissed)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeAnnotations did not exit on dismissal")
	}
	if runner.calls != 1 {
		t.Errorf("annotate invocations = %d, want 1", runner.calls)
	}
	if fs.Exists("ANNOTATION.md") {
		t.Error("ANNOTATION.md should be removed after applying")
	}
}

func TestServeAnnotationsExitsOnContextCancel(t *testing.T) {
	fs := newFakeFileStore()
	reporter := &fakeStatusReporter{signal: make(chan struct{}, 1)}
	o := services.NewPlanOrchestrator(&fakeRunner{}, fs, &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0)).
		WithStatusReporter(reporter)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		o.ServeAnnotations(ctx, make(chan struct{}))
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeAnnotations did not exit on context cancel")
	}
}

func TestServeAnnotationsWithoutReporterReturnsImmediately(t *testing.T) {
	o := services.NewPlanOrchestrator(&fakeRunner{}, newFakeFileStore(), &fakePrompter{}, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, planConfig(0))
	o.ServeAnnotations(context.Background(), make(chan struct{}))
}
