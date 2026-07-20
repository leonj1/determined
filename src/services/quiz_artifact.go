package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"determined/src/models"
)

type headingInventory struct {
	texts map[string]struct{}
	slugs map[string]string
}

// ParseQuiz parses a quiz artifact and verifies that every question is
// grounded in a uniquely linkable level-two explanation heading.
func ParseQuiz(content string, explanation string) ([]models.QuizQuestion, error) {
	headings, err := extractHeadings(explanation)
	if err != nil {
		return nil, err
	}
	var artifact struct {
		Questions []models.QuizQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(content), &artifact); err != nil {
		return nil, fmt.Errorf("parse quiz: %w", err)
	}
	if err := validateQuiz(artifact.Questions, headings); err != nil {
		return nil, err
	}
	return artifact.Questions, nil
}

func extractHeadings(markdown string) (headingInventory, error) {
	headings := headingInventory{texts: make(map[string]struct{}), slugs: make(map[string]string)}
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "## ") {
			continue
		}
		if err := headings.add(strings.TrimSpace(strings.TrimPrefix(line, "## "))); err != nil {
			return headingInventory{}, err
		}
	}
	return headings, nil
}

func (h headingInventory) add(heading string) error {
	if heading == "" {
		return fmt.Errorf("explanation contains an empty ## heading")
	}
	if _, exists := h.texts[heading]; exists {
		return fmt.Errorf("explanation heading %q is duplicated", heading)
	}
	slug := slugifyHeading(heading)
	if slug == "" {
		return fmt.Errorf("explanation heading %q has no linkable characters", heading)
	}
	if existing, exists := h.slugs[slug]; exists {
		return fmt.Errorf("explanation headings %q and %q share link anchor %q", existing, heading, "explain--"+slug)
	}
	h.texts[heading] = struct{}{}
	h.slugs[slug] = heading
	return nil
}

func slugifyHeading(heading string) string {
	var slug strings.Builder
	separator := false
	for _, character := range strings.TrimSpace(heading) {
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if !isASCIILetterOrNumber(character) {
			separator = slug.Len() > 0
			continue
		}
		if separator {
			slug.WriteByte('-')
			separator = false
		}
		slug.WriteRune(character)
	}
	return slug.String()
}

func isASCIILetterOrNumber(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func validateQuiz(questions []models.QuizQuestion, headings headingInventory) error {
	if len(headings.texts) == 0 {
		return fmt.Errorf("explanation has no ## headings")
	}
	if len(questions) != 5 {
		return fmt.Errorf("quiz has %d questions, want 5", len(questions))
	}
	for i, question := range questions {
		if err := validateQuizQuestion(question, headings); err != nil {
			return fmt.Errorf("quiz question %d: %w", i+1, err)
		}
	}
	return nil
}

func validateQuizQuestion(question models.QuizQuestion, headings headingInventory) error {
	if strings.TrimSpace(question.Question) == "" || strings.TrimSpace(question.Rationale) == "" {
		return fmt.Errorf("question and rationale must be non-empty")
	}
	if strings.TrimSpace(question.SourceSection) == "" {
		return fmt.Errorf("sourceSection must be non-empty")
	}
	if _, exists := headings.texts[question.SourceSection]; !exists {
		return fmt.Errorf("sourceSection %q is not an explanation heading", question.SourceSection)
	}
	if len(question.Choices) != 4 || question.CorrectIndex < 0 || question.CorrectIndex > 3 {
		return fmt.Errorf("must have 4 choices and a correctIndex from 0 through 3")
	}
	for _, choice := range question.Choices {
		if strings.TrimSpace(choice) == "" {
			return fmt.Errorf("choices must be non-empty")
		}
	}
	return nil
}
