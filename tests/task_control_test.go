package tests

import (
	"net/http"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

func TestTaskControlAdvertisesOnlyWhileATaskRuns(t *testing.T) {
	service := services.NewPlanStatusService(serverClock{}, models.GitContext{}, models.ToolIdentity{})

	if service.Snapshot().TaskControlAvailable {
		t.Fatal("no task registered yet, control must not be advertised")
	}
	if service.RequestSkipActiveTask() || service.RequestStopRun() {
		t.Fatal("requests without an active task must be rejected")
	}

	service.BeginTask(func() {})
	if !service.Snapshot().TaskControlAvailable {
		t.Fatal("a registered task must advertise control")
	}

	service.EndTask()
	if service.Snapshot().TaskControlAvailable {
		t.Fatal("a settled task must withdraw control")
	}
	if service.RequestSkipActiveTask() {
		t.Fatal("requests after the task settled must be rejected")
	}
}

func TestSkipRequestCancelsTheActiveInvocation(t *testing.T) {
	service := services.NewPlanStatusService(serverClock{}, models.GitContext{}, models.ToolIdentity{})
	cancelled := false
	service.BeginTask(func() { cancelled = true })

	if !service.RequestSkipActiveTask() {
		t.Fatal("expected the skip to be accepted")
	}
	if !cancelled {
		t.Fatal("expected the skip to cancel the running invocation")
	}
	service.EndTask()
	if got := service.TakeTaskAction(); got != models.TaskActionSkip {
		t.Fatalf("action = %v, want skip", got)
	}
	if got := service.TakeTaskAction(); got != models.TaskActionNone {
		t.Fatalf("action must be consumed once, got %v", got)
	}
}

func TestStopRequestOutranksARacingSkip(t *testing.T) {
	service := services.NewPlanStatusService(serverClock{}, models.GitContext{}, models.ToolIdentity{})
	service.BeginTask(func() {})

	service.RequestStopRun()
	service.RequestSkipActiveTask()

	if got := service.TakeTaskAction(); got != models.TaskActionStop {
		t.Fatalf("action = %v, want stop to survive the racing skip", got)
	}
}

// fakeTaskControlSink records task commands and answers with a scripted verdict.
type fakeTaskControlSink struct {
	skips, stops int
	accept       bool
}

func (s *fakeTaskControlSink) RequestSkipActiveTask() bool { s.skips++; return s.accept }
func (s *fakeTaskControlSink) RequestStopRun() bool        { s.stops++; return s.accept }

func startTaskControlServer(t *testing.T, sink clients.TaskControlSink) *clients.PlanStatusServer {
	t.Helper()
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{},
		serverClock{t: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)})
	if sink != nil {
		server.WithTaskControl(sink)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return server
}

func postStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestTaskEndpointsRelaySkipAndStop(t *testing.T) {
	sink := &fakeTaskControlSink{accept: true}
	server := startTaskControlServer(t, sink)
	defer shutdown(t, server)

	if code := postStatus(t, server.URL()+"task/skip"); code != http.StatusAccepted {
		t.Fatalf("skip status = %d, want 202", code)
	}
	if code := postStatus(t, server.URL()+"task/stop"); code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", code)
	}
	if sink.skips != 1 || sink.stops != 1 {
		t.Fatalf("sink saw %d skips and %d stops, want 1 and 1", sink.skips, sink.stops)
	}
}

func TestTaskEndpointsRejectWhenNothingRuns(t *testing.T) {
	server := startTaskControlServer(t, &fakeTaskControlSink{accept: false})
	defer shutdown(t, server)

	if code := postStatus(t, server.URL()+"task/skip"); code != http.StatusConflict {
		t.Fatalf("skip status = %d, want 409 when no task is active", code)
	}
}

func TestTaskEndpointsRequirePostAndASink(t *testing.T) {
	server := startTaskControlServer(t, nil)
	defer shutdown(t, server)

	if code := postStatus(t, server.URL()+"task/skip"); code != http.StatusServiceUnavailable {
		t.Fatalf("skip status = %d, want 503 without a sink", code)
	}
	resp, err := http.Get(server.URL() + "task/stop")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}
}
