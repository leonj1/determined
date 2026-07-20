package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

func TestQuizQuestionsLinkToExplanationHeadings(t *testing.T) {
	tests := []struct {
		name        string
		explanation string
		source      string
		wantError   string
	}{
		{name: "matching heading", explanation: "Intro.\n\n## Widget behavior\n\nDetails.", source: "Widget behavior"},
		{name: "missing source", explanation: "## Widget behavior", wantError: "quiz question 1: sourceSection must be non-empty"},
		{name: "unknown source", explanation: "## Widget behavior", source: "Raw diff", wantError: `quiz question 1: sourceSection "Raw diff" is not an explanation heading`},
		{name: "heading-less explanation", explanation: "Only an introduction.", source: "Widget behavior", wantError: "explanation has no ## headings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertQuizParse(t, test.explanation, test.source, test.wantError)
		})
	}
}

func TestExplanationHeadingsMustHaveUniqueLinks(t *testing.T) {
	tests := []struct {
		name        string
		explanation string
		wantError   string
	}{
		{name: "duplicate", explanation: "## Widget behavior\n\nOne.\n\n## Widget behavior\n\nTwo.", wantError: `explanation heading "Widget behavior" is duplicated`},
		{name: "slug collision", explanation: "## Widget: behavior\n\nOne.\n\n## widget behavior!\n\nTwo.", wantError: `explanation headings "Widget: behavior" and "widget behavior!" share link anchor "explain--widget-behavior"`},
		{name: "empty slug", explanation: "## 🎉\n\nDetails.", wantError: `explanation heading "🎉" has no linkable characters`},
		{name: "empty heading", explanation: "## \n\nDetails.", wantError: `explanation contains an empty ## heading`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertQuizParse(t, test.explanation, "Widget behavior", test.wantError)
		})
	}
}

func TestQuizIgnoresHeadingsInsideCodeAndBelowLevelTwo(t *testing.T) {
	explanation := "## Real change\n\n```markdown\n## Code sample\n```\n\n### Detail"
	_, err := services.ParseQuiz(buildQuizJSON(t, "Real change"), explanation)
	if err != nil {
		t.Fatalf("parse quiz grounded in the real heading: %v", err)
	}
}

func assertQuizParse(t *testing.T, explanation string, source string, wantError string) {
	t.Helper()
	_, err := services.ParseQuiz(buildQuizJSON(t, source), explanation)
	if wantError == "" && err != nil {
		t.Fatalf("parse grounded quiz: %v", err)
	}
	if wantError != "" && (err == nil || err.Error() != wantError) {
		t.Fatalf("error = %v, want %q", err, wantError)
	}
}

func buildQuizJSON(t *testing.T, source string) string {
	t.Helper()
	questions := make([]models.QuizQuestion, 5)
	for i := range questions {
		questions[i] = models.QuizQuestion{
			Question: "What changed?", Choices: []string{"A", "B", "C", "D"},
			CorrectIndex: 0, Rationale: "The explanation says A.", SourceSection: source,
		}
	}
	content, err := json.Marshal(struct {
		Questions []models.QuizQuestion `json:"questions"`
	}{Questions: questions})
	if err != nil {
		t.Fatalf("marshal quiz: %v", err)
	}
	return strings.TrimSpace(string(content))
}
