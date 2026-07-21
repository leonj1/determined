package tests

import (
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func chatSnapshot() models.PlanSessionStatus {
	started := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	return models.PlanSessionStatus{
		Goal: "ship agent chat", Plan: "# Plan\nImplement the protocol.",
		Phase: models.PlanPhaseSucceeded, ExecPhase: models.ExecPhaseRunning,
		ExecStartedAt: started,
		Steps:         []models.PlanStep{{At: started, Message: "implementing websocket transport"}},
		TaskSteps: []models.TaskStep{
			{Text: "add models", Completed: true},
			{Text: "add server", Completed: true},
			{Text: "add client", Completed: false},
		},
		ExecLog: []models.LogEntry{{At: started, Message: "running tests", Body: "ok", State: models.EntryStateRunning}},
	}
}

func TestChatServiceAnswersEveryDocumentedIntent(t *testing.T) {
	source := newFakePlanStatusSource(chatSnapshot())
	service := services.NewChatService(source, serverClock{t: time.Date(2026, 7, 21, 10, 2, 0, 0, time.UTC)})
	cases := map[string]models.ChatIntent{
		"what is the status?": models.ChatIntentStatus,
		"show the goal":       models.ChatIntentPlan,
		"show progress":       models.ChatIntentSteps,
		"what are you doing?": models.ChatIntentActivity,
		"show recent output":  models.ChatIntentLog,
		"help":                models.ChatIntentHelp,
	}
	for question, intent := range cases {
		response := service.Answer(models.ChatRequest{ID: "request", Type: models.ChatRequestMessage, Text: question})
		if response.ID != "request" || response.Type != models.ChatResponseReply || response.Text == "" {
			t.Errorf("%q response = %+v, want correlated prose reply", question, response)
			continue
		}
		if response.Data == nil || response.Data.Intent != intent {
			t.Errorf("%q intent = %+v, want %q", question, response.Data, intent)
		}
	}
}

func TestChatStatusCarriesMachineReadableProgressAndTiming(t *testing.T) {
	service := services.NewChatService(newFakePlanStatusSource(chatSnapshot()), serverClock{t: time.Date(2026, 7, 21, 10, 2, 0, 0, time.UTC)})
	response := service.Answer(models.ChatRequest{Type: models.ChatRequestMessage, Text: "status"})

	if response.Data.Phase != "execution running" || response.Data.ElapsedSeconds != 120 {
		t.Fatalf("status data = %+v, want execution phase and 120 seconds", response.Data)
	}
	if response.Data.Progress.Done != 2 || response.Data.Progress.Total != 3 {
		t.Fatalf("progress = %+v, want 2 of 3", response.Data.Progress)
	}
	if !strings.Contains(response.Text, "2 of 3") || !strings.Contains(response.Text, "2m0s") {
		t.Errorf("status text = %q, want progress and elapsed time", response.Text)
	}
}

func TestChatStatusDistinguishesPlanningAndFailedExecution(t *testing.T) {
	planning := chatSnapshot()
	planning.ExecPhase = ""
	planning.ExecStartedAt = time.Time{}
	planning.Phase = models.PlanPhaseRunning
	service := services.NewChatService(newFakePlanStatusSource(planning), serverClock{t: planning.StartedAt})
	if response := service.Answer(models.ChatRequest{Type: models.ChatRequestMessage, Text: "status"}); response.Data.Phase != "planning running" {
		t.Fatalf("planning phase = %q", response.Data.Phase)
	}

	failed := chatSnapshot()
	failed.ExecPhase = models.ExecPhaseFailed
	failed.ExecStopReason = "the tool failed repeatedly"
	failed.ExecAdvice = "inspect the latest log entry"
	service = services.NewChatService(newFakePlanStatusSource(failed), serverClock{t: failed.ExecStartedAt})
	response := service.Answer(models.ChatRequest{Type: models.ChatRequestMessage, Text: "status"})
	if response.Data.Phase != "execution failed" || response.Data.StopReason != failed.ExecStopReason || response.Data.Advice != failed.ExecAdvice {
		t.Fatalf("failed status data = %+v", response.Data)
	}
	if !strings.Contains(response.Text, failed.ExecStopReason) || !strings.Contains(response.Text, failed.ExecAdvice) {
		t.Errorf("failed status text = %q", response.Text)
	}
}

func TestUnmatchedChatQuestionFallsBackWithFullSnapshot(t *testing.T) {
	service := services.NewChatService(newFakePlanStatusSource(chatSnapshot()), serverClock{})
	response := service.Answer(models.ChatRequest{Type: models.ChatRequestMessage, Text: "tell me something surprising"})

	if response.Data.Intent != models.ChatIntentStatus || response.Data.Snapshot == nil {
		t.Fatalf("fallback data = %+v, want status with full snapshot", response.Data)
	}
	if response.Data.Snapshot.Goal != "ship agent chat" {
		t.Errorf("fallback snapshot goal = %q", response.Data.Snapshot.Goal)
	}
}

func TestUnknownChatRequestReturnsCorrelatedError(t *testing.T) {
	service := services.NewChatService(newFakePlanStatusSource(chatSnapshot()), serverClock{})
	response := service.Answer(models.ChatRequest{ID: "9", Type: "dance"})

	if response.ID != "9" || response.Type != models.ChatResponseError || response.Error != "unknown request type" {
		t.Fatalf("response = %+v, want correlated unknown-type error", response)
	}
}

func TestChatEventsExposePhaseStepAndLogChanges(t *testing.T) {
	before := chatSnapshot()
	after := before
	after.ExecPhase = models.ExecPhaseSucceeded
	after.Steps = append(after.Steps, models.PlanStep{Message: "finished"})
	after.ExecLog = append(after.ExecLog, models.LogEntry{Message: "final audit", Body: "approved"})
	service := services.NewChatService(newFakePlanStatusSource(before), serverClock{})

	events := service.Events(before, after)
	if len(events) != 3 {
		t.Fatalf("events = %+v, want phase, step, and log", events)
	}
	for i, kind := range []models.ChatEventType{models.ChatEventPhase, models.ChatEventStep, models.ChatEventLog} {
		if events[i].Type != models.ChatResponseEvent || events[i].Event != kind || events[i].ID != "" {
			t.Errorf("event %d = %+v, want uncorrelated %s event", i, events[i], kind)
		}
	}
}
