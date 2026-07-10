package services

import "strings"

// ParseQuestions extracts the individual clarifying questions a tool wrote to
// QUESTIONS.md. The protocol asks the tool for a markdown list — either an
// ordered list ("1. ...", "2) ...") or a bulleted one ("- ...", "* ..."). Any
// line that is not a list item (headings, blank lines, prose) is ignored, so a
// chatty tool that wraps its list in explanation still parses cleanly.
func ParseQuestions(content string) []string {
	var questions []string
	for _, line := range strings.Split(content, "\n") {
		if q, ok := listItem(line); ok {
			questions = append(questions, q)
		}
	}
	return questions
}

// RefinementIssues interprets the plan assessor's findings. An empty list, or
// a lone "NONE" sentinel, means the plan passed its quality gate.
func RefinementIssues(content string) []string {
	items := ParseQuestions(content)
	if len(items) == 1 && strings.EqualFold(items[0], "NONE") {
		return nil
	}
	return items
}

// listItem returns the text of a markdown list item, stripped of its marker,
// and whether the line was a list item at all.
func listItem(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(s, "- "); ok {
		return strings.TrimSpace(rest), true
	}
	if rest, ok := strings.CutPrefix(s, "* "); ok {
		return strings.TrimSpace(rest), true
	}
	if rest, ok := orderedItem(s); ok {
		return rest, true
	}
	return "", false
}

// orderedItem matches "<digits>." or "<digits>)" followed by a space.
func orderedItem(s string) (string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return "", false
	}
	if s[i] != '.' && s[i] != ')' {
		return "", false
	}
	rest := s[i+1:]
	if !strings.HasPrefix(rest, " ") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
