package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
)

type recordingPromptSink struct {
	responses []models.PromptResponse
	result    models.PromptSubmissionResult
}

func (s *recordingPromptSink) SubmitPromptResponse(response models.PromptResponse) models.PromptSubmissionResult {
	s.responses = append(s.responses, response)
	return s.result
}

func TestPromptResponseEndpointDeliversAuthorizedAnswer(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	sink := &recordingPromptSink{result: models.PromptSubmissionAccepted}
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithPromptResponses(sink)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	response := postWithToken(t, server, "prompt/respond", `{"id":12,"answer":"rewrite"}`)

	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}
	want := models.PromptResponse{ID: 12, Answer: "rewrite"}
	if len(sink.responses) != 1 || sink.responses[0] != want {
		t.Fatalf("responses = %+v, want exactly %+v", sink.responses, want)
	}
}

func TestPromptResponseEndpointMapsInvalidAndStaleAnswers(t *testing.T) {
	for _, test := range []struct {
		name   string
		result models.PromptSubmissionResult
		status int
	}{
		{"invalid", models.PromptSubmissionInvalid, http.StatusBadRequest},
		{"stale", models.PromptSubmissionStale, http.StatusConflict},
		{"missing", models.PromptSubmissionMissing, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := newFakePlanStatusSource(models.PlanSessionStatus{})
			sink := &recordingPromptSink{result: test.result}
			server := clients.NewPlanStatusServer(source, nil, nil, serverClock{}).WithPromptResponses(sink)
			if err := server.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			defer shutdown(t, server)
			response := postWithToken(t, server, "prompt/respond", `{"id":9,"answer":"value"}`)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
}

func TestBrowserPromptCancellationClearsPublishedState(t *testing.T) {
	service := newTestPlanStatusService(newSteppingClock(planStart()), models.GitContext{}, models.ToolIdentity{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Ask(ctx, models.TextPrompt("Question", "What changed?", false))
		done <- err
	}()
	deadline := time.After(time.Second)
	for service.Snapshot().PendingPrompt == nil {
		select {
		case <-deadline:
			t.Fatal("prompt was not published")
		default:
		}
	}
	id := service.Snapshot().PendingPrompt.ID
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Ask error = %v, want context canceled", err)
	}
	if service.Snapshot().PendingPrompt != nil {
		t.Fatal("cancelled prompt remains published")
	}
	if got := service.SubmitPromptResponse(models.PromptResponse{ID: id, Answer: "late"}); got != models.PromptSubmissionMissing {
		t.Fatalf("late submission = %q, want missing", got)
	}
}
