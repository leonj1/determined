package services

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxTraceLineRunes caps every status-page log line. Claude's stream-json
// events put an entire message — including whole file bodies inside tool
// results — on a single line, and laying out megabyte-scale unbroken strings
// is what froze the browser; a bounded line keeps rendering cheap.
const maxTraceLineRunes = 300

// streamEvent is the subset of a Claude CLI stream-json event the trace needs.
type streamEvent struct {
	Type       string         `json:"type"`
	Subtype    string         `json:"subtype"`
	Model      string         `json:"model"`
	Result     string         `json:"result"`
	IsError    bool           `json:"is_error"`
	NumTurns   int            `json:"num_turns"`
	DurationMS int            `json:"duration_ms"`
	Message    *streamMessage `json:"message"`
}

type streamMessage struct {
	Content []streamContent `json:"content"`
}

type streamContent struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// TraceToolOutput reduces one raw tool-output line (without its trailing
// newline) to short human-readable status-page trace lines. Claude stream-json
// events become one summary line per message part; anything else passes
// through. Every returned line respects maxTraceLineRunes.
func TraceToolOutput(line string) string {
	event, ok := parseStreamEvent(line)
	if !ok {
		return truncateTraceLine(line)
	}
	lines := traceEventLines(event)
	if len(lines) == 0 {
		return truncateTraceLine(line)
	}
	return strings.Join(lines, "\n")
}

func parseStreamEvent(line string) (streamEvent, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return streamEvent{}, false
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil || event.Type == "" {
		return streamEvent{}, false
	}
	return event, true
}

func traceEventLines(event streamEvent) []string {
	switch event.Type {
	case "system":
		return []string{truncateTraceLine(systemTrace(event))}
	case "assistant", "user":
		return messageTraceLines(event.Message)
	case "result":
		return []string{truncateTraceLine(resultTrace(event))}
	}
	return nil
}

func systemTrace(event streamEvent) string {
	trace := "⚙ session " + orUnknown(event.Subtype)
	if event.Model != "" {
		trace += " (model " + event.Model + ")"
	}
	return trace
}

func resultTrace(event streamEvent) string {
	mark := "✔ finished"
	if event.IsError || strings.HasPrefix(event.Subtype, "error") {
		mark = "✖ failed"
	}
	trace := fmt.Sprintf("%s after %d turn(s) in %s", mark, event.NumTurns, formatTraceDuration(event.DurationMS))
	if event.Result != "" {
		trace += ": " + collapseWhitespace(event.Result)
	}
	return trace
}

func messageTraceLines(message *streamMessage) []string {
	if message == nil {
		return nil
	}
	lines := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		if trace := contentTrace(part); trace != "" {
			lines = append(lines, truncateTraceLine(trace))
		}
	}
	return lines
}

func contentTrace(part streamContent) string {
	switch part.Type {
	case "text":
		return "💬 " + collapseWhitespace(part.Text)
	case "thinking":
		return "… thinking"
	case "tool_use":
		return "→ " + orUnknown(part.Name) + " " + collapseWhitespace(string(part.Input))
	case "tool_result":
		return toolResultTrace(part)
	}
	return ""
}

func toolResultTrace(part streamContent) string {
	text := toolResultText(part.Content)
	mark := "←"
	if part.IsError {
		mark = "✗"
	}
	return fmt.Sprintf("%s tool result (%d chars): %s", mark, utf8.RuneCountInString(text), collapseWhitespace(text))
}

// toolResultText extracts the text of a tool_result payload, which the CLI
// encodes either as a bare string or as a list of typed text blocks.
func toolResultText(payload json.RawMessage) string {
	var text string
	if json.Unmarshal(payload, &text) == nil {
		return text
	}
	var blocks []streamContent
	if json.Unmarshal(payload, &blocks) != nil {
		return string(payload)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, " ")
}

func formatTraceDuration(milliseconds int) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%dms", milliseconds)
	}
	return fmt.Sprintf("%.1fs", float64(milliseconds)/1000)
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// collapseWhitespace folds a possibly multi-line snippet onto one line so a
// trace never re-introduces the giant lines it exists to remove.
func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// truncateTraceLine bounds one line to maxTraceLineRunes, naming how much was
// elided so the full output is known to live in the terminal and run logs.
func truncateTraceLine(line string) string {
	runes := []rune(line)
	if len(runes) <= maxTraceLineRunes {
		return line
	}
	return string(runes[:maxTraceLineRunes]) + fmt.Sprintf(" … (+%d chars)", len(runes)-maxTraceLineRunes)
}
