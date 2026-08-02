package services

import (
	"regexp"
	"strings"
)

var testHeadingPattern = regexp.MustCompile(`(?m)^#{2,4}\s+Test\s+\d+`)
var typeLinePattern = regexp.MustCompile(`(?i)\*\*Type:\*\*\s*([^\n]+)`)
var bddTypePattern = regexp.MustCompile(`(?i)bdd|gherkin`)
var sequenceDiagramPattern = regexp.MustCompile("(?s)```mermaid\\s*\\n\\s*sequenceDiagram")
var gherkinFencePattern = regexp.MustCompile("```gherkin")
var alignmentLinePattern = regexp.MustCompile(`(?i)\*\*Alignment:\*\*\s*(aligned|partial|misaligned)`)
var alignmentNotePattern = regexp.MustCompile(`(?i)\*\*Alignment note:\*\*\s*([^\n]*)`)

// TestsDocument reads the recommended-tests markdown and answers questions
// about its journey tests.
type TestsDocument struct {
	content string
}

// NewTestsDocument wraps one TESTS.md content string.
func NewTestsDocument(content string) TestsDocument {
	return TestsDocument{content: content}
}

// JourneyTestsMissingDiagrams returns the heading line of every journey test
// that lacks a mermaid sequence diagram. A section counts as a journey test
// when its Type line names journey/end-to-end/e2e, or when it is untyped and
// carries no Gherkin scenario.
func (d TestsDocument) JourneyTestsMissingDiagrams() []string {
	missing := []string{}
	for _, section := range d.sections() {
		if !d.isJourney(section) {
			continue
		}
		if sequenceDiagramPattern.MatchString(section) {
			continue
		}
		missing = append(missing, strings.SplitN(strings.TrimSpace(section), "\n", 2)[0])
	}
	return missing
}

// TestsMissingAlignment returns the heading line of every test that carries no
// `**Alignment:**` verdict judging it against the plan's functional goal.
func (d TestsDocument) TestsMissingAlignment() []string {
	missing := []string{}
	for _, section := range d.sections() {
		if alignmentLinePattern.MatchString(section) {
			continue
		}
		missing = append(missing, strings.SplitN(strings.TrimSpace(section), "\n", 2)[0])
	}
	return missing
}

// MisalignedTest pairs one misaligned test's heading with the assessor's
// explanation of why it fails to prove the goal.
type MisalignedTest struct {
	Heading string
	Note    string
}

// MisalignedTests returns every test whose `**Alignment:**` verdict is
// misaligned, with its `**Alignment note:**` text. A missing note yields an
// empty Note; verdict-free, aligned, and partial sections are skipped.
func (d TestsDocument) MisalignedTests() []MisalignedTest {
	misaligned := []MisalignedTest{}
	for _, section := range d.sections() {
		match := alignmentLinePattern.FindStringSubmatch(section)
		if match == nil || !strings.EqualFold(match[1], "misaligned") {
			continue
		}
		misaligned = append(misaligned, MisalignedTest{
			Heading: strings.SplitN(strings.TrimSpace(section), "\n", 2)[0],
			Note:    d.alignmentNote(section),
		})
	}
	return misaligned
}

// alignmentNote extracts the trimmed note text, or "" when the line is absent.
func (d TestsDocument) alignmentNote(section string) string {
	match := alignmentNotePattern.FindStringSubmatch(section)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// sections splits the document at each `### Test N` heading, dropping any
// preamble before the first test.
func (d TestsDocument) sections() []string {
	indexes := testHeadingPattern.FindAllStringIndex(d.content, -1)
	sections := []string{}
	for i, loc := range indexes {
		end := len(d.content)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		sections = append(sections, d.content[loc[0]:end])
	}
	return sections
}

// isJourney classifies one test section: an explicit BDD/Gherkin Type line or
// a gherkin fence marks a BDD test; everything else is a journey test.
func (d TestsDocument) isJourney(section string) bool {
	match := typeLinePattern.FindStringSubmatch(section)
	if match != nil {
		return !bddTypePattern.MatchString(match[1])
	}
	return !gherkinFencePattern.MatchString(section)
}
