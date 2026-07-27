package tests

import (
	"strings"
	"testing"
	"unicode/utf8"

	"determined/src/services"
)

func TestPlainToolOutputPassesThroughUnchanged(t *testing.T) {
	line := "==> [10:00:00] executing step 1"
	if got := services.TraceToolOutput(line); got != line {
		t.Errorf("trace = %q, want the plain line unchanged", got)
	}
}

func TestLongToolOutputLinesAreTruncatedWithACount(t *testing.T) {
	line := strings.Repeat("x", 1000)
	got := services.TraceToolOutput(line)
	if !strings.HasSuffix(got, " … (+700 chars)") {
		t.Errorf("trace = %q, want a truncation marker naming the elided length", got)
	}
	if utf8.RuneCountInString(got) > 350 {
		t.Errorf("trace is %d runes, want a bounded line", utf8.RuneCountInString(got))
	}
}

func TestTruncationCountsRunesNotBytes(t *testing.T) {
	line := strings.Repeat("é", 400)
	got := services.TraceToolOutput(line)
	if !strings.HasSuffix(got, " … (+100 chars)") {
		t.Errorf("trace = %q, want rune-based truncation", got)
	}
}

func TestAssistantEventsBecomeReadableTraceLines(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[` +
		`{"type":"text","text":"Working on it.\nNext I will edit."},` +
		`{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`
	got := services.TraceToolOutput(line)
	want := "💬 Working on it. Next I will edit.\n→ Bash {\"command\":\"go test ./...\"}"
	if got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestToolResultEventsSummarizeInsteadOfEmbeddingPayloads(t *testing.T) {
	payload := strings.Repeat("file contents ", 50_000)
	line := `{"type":"user","message":{"content":[` +
		`{"type":"tool_result","content":"` + payload + `"}]}}`
	got := services.TraceToolOutput(line)
	if !strings.HasPrefix(got, "← tool result (700000 chars):") {
		t.Errorf("trace = %q, want a tool result summary with its size", got)
	}
	if utf8.RuneCountInString(got) > 350 {
		t.Errorf("trace is %d runes, want a bounded line", utf8.RuneCountInString(got))
	}
}

func TestToolResultBlocksAndErrorsAreSummarized(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,` +
		`"content":[{"type":"text","text":"no such file"}]}]}}`
	got := services.TraceToolOutput(line)
	if got != "✗ tool result (12 chars): no such file" {
		t.Errorf("trace = %q, want an error-marked block summary", got)
	}
}

func TestSystemAndResultEventsBecomeOneLineSummaries(t *testing.T) {
	system := `{"type":"system","subtype":"init","model":"claude-opus-5"}`
	if got := services.TraceToolOutput(system); got != "⚙ session init (model claude-opus-5)" {
		t.Errorf("system trace = %q", got)
	}
	result := `{"type":"result","subtype":"success","result":"All steps complete.","num_turns":4,"duration_ms":81234}`
	if got := services.TraceToolOutput(result); got != "✔ finished after 4 turn(s) in 81.2s: All steps complete." {
		t.Errorf("result trace = %q", got)
	}
	failed := `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":9,"duration_ms":500}`
	if got := services.TraceToolOutput(failed); !strings.HasPrefix(got, "✖ failed after 9 turn(s) in 500ms") {
		t.Errorf("failed result trace = %q", got)
	}
}

func TestNonStreamJSONObjectsAreOnlyTruncated(t *testing.T) {
	line := `{"level":"info","msg":"` + strings.Repeat("z", 600) + `"}`
	got := services.TraceToolOutput(line)
	if !strings.HasPrefix(got, `{"level":"info"`) || !strings.Contains(got, "… (+") {
		t.Errorf("trace = %q, want the raw JSON truncated, not dropped", got)
	}
}
