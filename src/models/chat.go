package models

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ChatConnection is the transport boundary consumed by the chat client.
type ChatConnection interface {
	ReadText() ([]byte, error)
	WriteText([]byte) error
	SetDeadline(time.Time) error
	CleanClose(error) bool
	Close() error
}

// ChatURL is the verified session's WebSocket endpoint.
type ChatURL string

// ChatConnector opens a chat transport connection.
type ChatConnector interface {
	Connect(context.Context, ChatURL) (ChatConnection, error)
}

// ChatRequestType identifies an operation accepted by the chat protocol.
type ChatRequestType string

const (
	ChatRequestMessage   ChatRequestType = "message"
	ChatRequestSubscribe ChatRequestType = "subscribe"
)

// ChatResponseType identifies a message emitted by the chat protocol.
type ChatResponseType string

const (
	ChatResponseReply ChatResponseType = "reply"
	ChatResponseEvent ChatResponseType = "event"
	ChatResponseError ChatResponseType = "error"
)

// ChatEventType identifies a pushed live-status change.
type ChatEventType string

const (
	ChatEventPhase ChatEventType = "phase"
	ChatEventStep  ChatEventType = "step"
	ChatEventLog   ChatEventType = "log"
)

// ChatIntent is one deterministic question category understood by chat.
type ChatIntent string

const (
	ChatIntentStatus   ChatIntent = "status"
	ChatIntentPlan     ChatIntent = "plan"
	ChatIntentSteps    ChatIntent = "steps"
	ChatIntentActivity ChatIntent = "activity"
	ChatIntentLog      ChatIntent = "log"
	ChatIntentHelp     ChatIntent = "help"
)

// ChatRequest is one client-to-server JSON message.
type ChatRequest struct {
	ID   string          `json:"id,omitempty"`
	Type ChatRequestType `json:"type,omitempty"`
	Text string          `json:"text,omitempty"`
}

// Normalized returns a copy with surrounding text whitespace removed.
func (r ChatRequest) Normalized() ChatRequest {
	r.Text = strings.TrimSpace(r.Text)
	return r
}

// AsHTTPMessage interprets the curl-friendly omitted type as a message.
func (r ChatRequest) AsHTTPMessage() ChatRequest {
	if r.Type == "" {
		r.Type = ChatRequestMessage
	}
	return r.Normalized()
}

// Validate reports whether the request has the fields required by its type.
func (r ChatRequest) Validate() error {
	r = r.Normalized()
	switch r.Type {
	case ChatRequestMessage:
		if r.Text == "" {
			return fmt.Errorf("message text is required")
		}
		return nil
	case ChatRequestSubscribe:
		return nil
	default:
		return fmt.Errorf("unknown request type")
	}
}

// ChatProgress is the machine-readable completion summary for task steps.
type ChatProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// ChatData is the typed structured payload paired with human-readable text.
// Fields not relevant to the selected intent are omitted.
type ChatData struct {
	Intent          ChatIntent         `json:"intent"`
	Phase           string             `json:"phase,omitempty"`
	ElapsedSeconds  int64              `json:"elapsedSeconds,omitempty"`
	CurrentActivity string             `json:"currentActivity,omitempty"`
	Progress        *ChatProgress      `json:"progress,omitempty"`
	StopReason      string             `json:"stopReason,omitempty"`
	Advice          string             `json:"advice,omitempty"`
	Goal            string             `json:"goal,omitempty"`
	Plan            string             `json:"plan,omitempty"`
	TaskSteps       []TaskStep         `json:"taskSteps,omitempty"`
	LastStep        *PlanStep          `json:"lastStep,omitempty"`
	LastLog         *LogEntry          `json:"lastLog,omitempty"`
	Logs            []LogEntry         `json:"logs,omitempty"`
	Intents         []ChatIntent       `json:"intents,omitempty"`
	Snapshot        *PlanSessionStatus `json:"snapshot,omitempty"`
}

// ChatResponse is one server-to-client JSON message.
type ChatResponse struct {
	ID    string           `json:"id,omitempty"`
	Type  ChatResponseType `json:"type"`
	Text  string           `json:"text,omitempty"`
	Event ChatEventType    `json:"event,omitempty"`
	Error string           `json:"error,omitempty"`
	Data  *ChatData        `json:"data,omitempty"`
}

// ChatIntents returns the stable intent list exposed by the help response.
func ChatIntents() []ChatIntent {
	return []ChatIntent{
		ChatIntentStatus, ChatIntentPlan, ChatIntentSteps,
		ChatIntentActivity, ChatIntentLog, ChatIntentHelp,
	}
}
