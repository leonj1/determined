package services

import (
	"strings"
	"testing"
)

func TestParseTieBreakerVerdictAccept(t *testing.T) {
	output := "VERDICT: ACCEPT\nRATIONALE: The acceptance criterion is clearly met.\n"
	verdict, rationale, guidance := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictAccept {
		t.Fatalf("expected ACCEPT, got %v", verdict)
	}
	if rationale != "The acceptance criterion is clearly met." {
		t.Fatalf("expected rationale, got %q", rationale)
	}
	if guidance != "" {
		t.Fatalf("expected no guidance for ACCEPT, got %q", guidance)
	}
}

func TestParseTieBreakerVerdictReject(t *testing.T) {
	output := "VERDICT: REJECT\nRATIONALE: The implementation fails the criterion.\nGUIDANCE: Add error handling around the file read.\n"
	verdict, rationale, guidance := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictReject {
		t.Fatalf("expected REJECT, got %v", verdict)
	}
	if rationale != "The implementation fails the criterion." {
		t.Fatalf("expected rationale, got %q", rationale)
	}
	if guidance != "Add error handling around the file read." {
		t.Fatalf("expected guidance, got %q", guidance)
	}
}

func TestParseTieBreakerVerdictCaseInsensitive(t *testing.T) {
	output := "verdict: accept\nRATIONALE: ok.\n"
	verdict, rationale, _ := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictAccept {
		t.Fatalf("expected ACCEPT from lowercase verdict, got %v", verdict)
	}
	if rationale != "ok." {
		t.Fatalf("expected rationale, got %q", rationale)
	}
}

func TestParseTieBreakerVerdictExtraWhitespace(t *testing.T) {
	output := "  VERDICT:   REJECT  \n  RATIONALE:   wrong.  \n  GUIDANCE:   fix it.  \n"
	verdict, rationale, guidance := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictReject {
		t.Fatalf("expected REJECT, got %v", verdict)
	}
	if rationale != "wrong." {
		t.Fatalf("expected trimmed rationale, got %q", rationale)
	}
	if guidance != "fix it." {
		t.Fatalf("expected trimmed guidance, got %q", guidance)
	}
}

func TestParseTieBreakerVerdictWithPreamble(t *testing.T) {
	output := "Here is my analysis of the deadlock.\n\nThe worker did X.\n\nVERDICT: ACCEPT\nRATIONALE: Criterion met.\n"
	verdict, rationale, _ := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictAccept {
		t.Fatalf("expected ACCEPT even with preamble, got %v", verdict)
	}
	if rationale != "Criterion met." {
		t.Fatalf("expected rationale from after preamble, got %q", rationale)
	}
}

func TestParseTieBreakerVerdictOnlyFirstVerdictUsed(t *testing.T) {
	output := "VERDICT: ACCEPT\nRATIONALE: first.\nVERDICT: REJECT\nRATIONALE: second.\n"
	verdict, rationale, _ := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictAccept {
		t.Fatalf("expected first ACCEPT used, got %v", verdict)
	}
	if rationale != "first." {
		t.Fatalf("expected first rationale, got %q", rationale)
	}
}

func TestParseTieBreakerVerdictUnrecognizedIsInvalid(t *testing.T) {
	output := "VERDICT: MAYBE\nRATIONALE: unsure.\n"
	verdict, _, _ := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictInvalid {
		t.Fatalf("expected invalid for unrecognized verdict, got %v", verdict)
	}
}

func TestParseTieBreakerVerdictEmptyOutput(t *testing.T) {
	verdict, rationale, guidance := parseTieBreakerVerdict("")
	if verdict != tieBreakerVerdictInvalid {
		t.Fatalf("expected invalid for empty output, got %v", verdict)
	}
	if rationale != "" || guidance != "" {
		t.Fatalf("expected empty rationale/guidance, got %q / %q", rationale, guidance)
	}
}

func TestParseTieBreakerVerdictRejectWithoutGuidance(t *testing.T) {
	output := "VERDICT: REJECT\nRATIONALE: wrong.\n"
	verdict, rationale, guidance := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictReject {
		t.Fatalf("expected REJECT, got %v", verdict)
	}
	if rationale != "wrong." {
		t.Fatalf("expected rationale, got %q", rationale)
	}
	if guidance != "" {
		t.Fatalf("expected no guidance when omitted, got %q", guidance)
	}
}

func TestParseTieBreakerVerdictMultilineRationaleUsesFirst(t *testing.T) {
	output := "VERDICT: REJECT\nRATIONALE: first line.\nRATIONALE: second line.\n"
	_, rationale, _ := parseTieBreakerVerdict(output)
	if rationale != "first line." {
		t.Fatalf("expected only first rationale line, got %q", rationale)
	}
}

func TestParseTieBreakerVerdictGuidanceBeforeVerdict(t *testing.T) {
	// The parser scans in order; GUIDANCE before VERDICT should still be captured.
	output := "GUIDANCE: fix it.\nVERDICT: REJECT\nRATIONALE: wrong.\n"
	verdict, _, guidance := parseTieBreakerVerdict(output)
	if verdict != tieBreakerVerdictReject {
		t.Fatalf("expected REJECT, got %v", verdict)
	}
	if guidance != "fix it." {
		t.Fatalf("expected guidance captured regardless of position, got %q", guidance)
	}
}

func TestTieBreakerPromptContent(t *testing.T) {
	// The prompt must instruct the tool to read the right files, evaluate both
	// sides, and return a structured verdict — never modify files.
	for _, want := range []string{
		"PLAN.md",
		"STEPS.md",
		"FIXES.md",
		"NOTES.md",
		"VERDICT: ACCEPT",
		"VERDICT: REJECT",
		"RATIONALE:",
		"GUIDANCE:",
		"objectively correct",
		"more reasonable",
		"Do not implement anything",
		"Do not modify files",
	} {
		if !strings.Contains(tieBreakerPrompt, want) {
			t.Fatalf("tieBreakerPrompt must contain %q", want)
		}
	}
}
