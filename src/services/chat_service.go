package services

import (
	"fmt"
	"strings"
	"time"

	"determined/src/models"
)

const (
	chatLogLimit  = 10
	chatBodyLimit = 2000
)

// ChatStatusSource supplies the live state used for answers and events.
type ChatStatusSource interface {
	Snapshot() models.PlanSessionStatus
	Subscribe() (<-chan models.PlanSessionStatus, func())
}

// ChatService answers chat requests deterministically from session state.
type ChatService struct {
	source ChatStatusSource
	clock  Clock
}

// NewChatService constructs a snapshot-derived responder.
func NewChatService(source ChatStatusSource, clock Clock) *ChatService {
	return &ChatService{source: source, clock: clock}
}

// Answer produces exactly one correlated response for a request.
func (s *ChatService) Answer(request models.ChatRequest) models.ChatResponse {
	request = request.Normalized()
	if err := request.Validate(); err != nil || request.Type != models.ChatRequestMessage {
		message := "unknown request type"
		if err != nil && request.Type == models.ChatRequestMessage {
			message = err.Error()
		}
		return chatError(request.ID, message)
	}
	snapshot := s.source.Snapshot()
	intent, fallback := intentFor(request.Text)
	return s.reply(request.ID, intent, snapshot, fallback)
}

// Events describes externally meaningful changes between two snapshots.
func (s *ChatService) Events(before, after models.PlanSessionStatus) []models.ChatResponse {
	events := phaseEvents(before, after)
	events = append(events, stepEvents(before.Steps, after.Steps)...)
	events = append(events, logEvents(before.Log, after.Log)...)
	events = append(events, logEvents(before.ExecLog, after.ExecLog)...)
	return events
}

func (s *ChatService) reply(id string, intent models.ChatIntent, snapshot models.PlanSessionStatus, fallback bool) models.ChatResponse {
	data := baseChatData(intent, snapshot, s.clock.Now())
	data, text := populateIntentData(data, snapshot)
	if fallback {
		copy := snapshot
		data.Snapshot = &copy
	}
	return models.ChatResponse{ID: id, Type: models.ChatResponseReply, Text: text, Data: &data}
}

func populateIntentData(data models.ChatData, snapshot models.PlanSessionStatus) (models.ChatData, string) {
	switch data.Intent {
	case models.ChatIntentPlan:
		data.Goal, data.Plan = snapshot.Goal, snapshot.Plan
		return data, fmt.Sprintf("Goal: %s\n\n%s", textOr(snapshot.Goal, "not published"), textOr(snapshot.Plan, "Plan not published yet."))
	case models.ChatIntentSteps:
		data.TaskSteps = copiedTaskSteps(snapshot.TaskSteps)
		return data, stepsText(snapshot.TaskSteps, *data.Progress)
	case models.ChatIntentActivity:
		data.LastStep, data.LastLog = lastStep(snapshot), lastLog(snapshot)
		return data, activityText(data)
	case models.ChatIntentLog:
		data.Logs = recentLogs(snapshot)
		return data, logsText(data.Logs)
	case models.ChatIntentHelp:
		data.Intents = models.ChatIntents()
		return data, "Ask about status, plan or goal, steps or progress, current activity, recent log output, or help."
	}
	return data, statusText(snapshot, data)
}

func intentFor(text string) (models.ChatIntent, bool) {
	lower := strings.ToLower(text)
	checks := []struct {
		intent models.ChatIntent
		words  []string
	}{
		{models.ChatIntentHelp, []string{"help", "commands"}},
		{models.ChatIntentPlan, []string{"plan", "goal"}},
		{models.ChatIntentSteps, []string{"steps", "step", "progress", "checklist"}},
		{models.ChatIntentActivity, []string{"activity", "doing", "working", "current"}},
		{models.ChatIntentLog, []string{"log", "output"}},
		{models.ChatIntentStatus, []string{"status", "how is it going", "how's it going"}},
	}
	for _, check := range checks {
		if containsAny(lower, check.words) {
			return check.intent, false
		}
	}
	return models.ChatIntentStatus, true
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func baseChatData(intent models.ChatIntent, snapshot models.PlanSessionStatus, now time.Time) models.ChatData {
	progress := progressOf(snapshot.TaskSteps)
	return models.ChatData{
		Intent: intent, Phase: activePhase(snapshot), ElapsedSeconds: elapsed(snapshot, now),
		CurrentActivity: currentActivity(snapshot), Progress: &progress,
		StopReason: snapshot.ExecStopReason, Advice: snapshot.ExecAdvice,
	}
}

func activePhase(snapshot models.PlanSessionStatus) string {
	if snapshot.ExecPhase != "" {
		return "execution " + string(snapshot.ExecPhase)
	}
	return "planning " + string(snapshot.Phase)
}

func elapsed(snapshot models.PlanSessionStatus, now time.Time) int64 {
	if snapshot.ExecPhase != "" && !snapshot.ExecStartedAt.IsZero() {
		end := snapshot.ExecEndedAt
		if end.IsZero() {
			end = now
		}
		return max(0, int64(end.Sub(snapshot.ExecStartedAt).Seconds()))
	}
	return max(0, int64(snapshot.Duration(now).Seconds()))
}

func progressOf(steps []models.TaskStep) models.ChatProgress {
	progress := models.ChatProgress{Total: len(steps)}
	for _, step := range steps {
		if step.Completed {
			progress.Done++
		}
	}
	return progress
}

func statusText(snapshot models.PlanSessionStatus, data models.ChatData) string {
	text := fmt.Sprintf("The session is %s after %s; %d of %d steps are complete.", data.Phase, formattedElapsed(data.ElapsedSeconds), data.Progress.Done, data.Progress.Total)
	if data.CurrentActivity != "" {
		text += " Current activity: " + data.CurrentActivity + "."
	}
	if snapshot.ExecStopReason != "" {
		text += " It stopped because " + snapshot.ExecStopReason + "."
		if snapshot.ExecAdvice != "" {
			text += " Advice: " + snapshot.ExecAdvice
		}
	}
	return text
}

func formattedElapsed(seconds int64) string {
	return (time.Duration(seconds) * time.Second).String()
}

func currentActivity(snapshot models.PlanSessionStatus) string {
	if entry := lastLog(snapshot); entry != nil {
		return entry.Message
	}
	if step := lastStep(snapshot); step != nil {
		return step.Message
	}
	return ""
}

func lastStep(snapshot models.PlanSessionStatus) *models.PlanStep {
	if len(snapshot.Steps) == 0 {
		return nil
	}
	step := snapshot.Steps[len(snapshot.Steps)-1]
	return &step
}

func lastLog(snapshot models.PlanSessionStatus) *models.LogEntry {
	logs := snapshot.Log
	if len(snapshot.ExecLog) > 0 {
		logs = snapshot.ExecLog
	}
	if len(logs) == 0 {
		return nil
	}
	entry := boundedLog(logs[len(logs)-1])
	return &entry
}

func recentLogs(snapshot models.PlanSessionStatus) []models.LogEntry {
	logs := append(append([]models.LogEntry{}, snapshot.Log...), snapshot.ExecLog...)
	if len(logs) > chatLogLimit {
		logs = logs[len(logs)-chatLogLimit:]
	}
	result := make([]models.LogEntry, len(logs))
	for i, entry := range logs {
		result[i] = boundedLog(entry)
	}
	return result
}

func boundedLog(entry models.LogEntry) models.LogEntry {
	if len(entry.Body) > chatBodyLimit {
		entry.Body = entry.Body[len(entry.Body)-chatBodyLimit:]
	}
	return entry
}

func copiedTaskSteps(steps []models.TaskStep) []models.TaskStep {
	return append([]models.TaskStep{}, steps...)
}

func stepsText(steps []models.TaskStep, progress models.ChatProgress) string {
	if len(steps) == 0 {
		return "No task steps have been published yet."
	}
	lines := []string{fmt.Sprintf("%d of %d steps are complete:", progress.Done, progress.Total)}
	for _, step := range steps {
		mark := "[ ]"
		if step.Completed {
			mark = "[x]"
		}
		lines = append(lines, mark+" "+step.Text)
	}
	return strings.Join(lines, "\n")
}

func activityText(data models.ChatData) string {
	if data.LastLog != nil {
		return fmt.Sprintf("Current activity: %s (%s).", data.LastLog.Message, textOr(string(data.LastLog.State), "in progress"))
	}
	if data.LastStep != nil {
		return "Current activity: " + data.LastStep.Message + "."
	}
	return "No activity has been reported yet."
}

func logsText(logs []models.LogEntry) string {
	if len(logs) == 0 {
		return "No log entries have been reported yet."
	}
	lines := make([]string, len(logs))
	for i, entry := range logs {
		lines[i] = entry.Message
		if strings.TrimSpace(entry.Body) != "" {
			lines[i] += ": " + strings.TrimSpace(entry.Body)
		}
	}
	return strings.Join(lines, "\n")
}

func textOr(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

func chatError(id, message string) models.ChatResponse {
	return models.ChatResponse{ID: id, Type: models.ChatResponseError, Error: message}
}

func phaseEvents(before, after models.PlanSessionStatus) []models.ChatResponse {
	if activePhase(before) == activePhase(after) {
		return nil
	}
	return []models.ChatResponse{{
		Type: models.ChatResponseEvent, Event: models.ChatEventPhase,
		Text: activePhase(after), Data: &models.ChatData{Intent: models.ChatIntentStatus, Phase: activePhase(after)},
	}}
}

func stepEvents(before, after []models.PlanStep) []models.ChatResponse {
	if len(after) <= len(before) {
		return nil
	}
	events := make([]models.ChatResponse, 0, len(after)-len(before))
	for _, step := range after[len(before):] {
		copy := step
		events = append(events, models.ChatResponse{
			Type: models.ChatResponseEvent, Event: models.ChatEventStep, Text: step.Message,
			Data: &models.ChatData{Intent: models.ChatIntentActivity, LastStep: &copy},
		})
	}
	return events
}

func logEvents(before, after []models.LogEntry) []models.ChatResponse {
	if len(after) <= len(before) {
		return nil
	}
	events := make([]models.ChatResponse, 0, len(after)-len(before))
	for _, entry := range after[len(before):] {
		copy := boundedLog(entry)
		events = append(events, models.ChatResponse{
			Type: models.ChatResponseEvent, Event: models.ChatEventLog, Text: entry.Message,
			Data: &models.ChatData{Intent: models.ChatIntentLog, LastLog: &copy},
		})
	}
	return events
}
