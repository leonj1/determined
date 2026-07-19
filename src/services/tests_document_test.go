package services_test

import (
	"testing"

	"determined/src/services"
)

func TestJourneyTestMissingDiagramIsReported(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: signup journey\n**Type:** Journey\nUser signs up.\n\n" +
			"### Test 2: login scenario\n**Type:** BDD\n```gherkin\nScenario: login\n```\n")

	missing := doc.JourneyTestsMissingDiagrams()

	if len(missing) != 1 || missing[0] != "### Test 1: signup journey" {
		t.Fatalf("missing = %v, want the journey test heading only", missing)
	}
}

func TestJourneyTestWithDiagramPasses(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: signup journey\n**Type:** Journey\nUser signs up.\n" +
			"```mermaid\nsequenceDiagram\nUser->>App: signup\n```\n")

	if missing := doc.JourneyTestsMissingDiagrams(); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
}

func TestUntypedSectionWithGherkinFenceIsNotJourney(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: login\n```gherkin\nScenario: login\nGiven a user\n```\n")

	if missing := doc.JourneyTestsMissingDiagrams(); len(missing) != 0 {
		t.Fatalf("missing = %v, want none for a Gherkin-only test", missing)
	}
}

func TestUntypedSectionWithoutGherkinCountsAsJourney(t *testing.T) {
	doc := services.NewTestsDocument("### Test 1: browse catalog\nUser browses.\n")

	missing := doc.JourneyTestsMissingDiagrams()

	if len(missing) != 1 || missing[0] != "### Test 1: browse catalog" {
		t.Fatalf("missing = %v, want the untyped journey heading", missing)
	}
}

func TestDiagramFenceWithoutSequenceDiagramDoesNotCount(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: journey\n**Type:** Journey\n```mermaid\nflowchart TD\nA-->B\n```\n")

	missing := doc.JourneyTestsMissingDiagrams()

	if len(missing) != 1 {
		t.Fatalf("missing = %v, want one — a flowchart is not a sequence diagram", missing)
	}
}

func TestTestWithoutAlignmentVerdictIsReported(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: signup journey\n**Alignment:** aligned\nUser signs up.\n\n" +
			"### Test 2: login scenario\n```gherkin\nScenario: login\n```\n")

	missing := doc.TestsMissingAlignment()

	if len(missing) != 1 || missing[0] != "### Test 2: login scenario" {
		t.Fatalf("missing = %v, want the unjudged test heading only", missing)
	}
}

func TestEveryAlignmentVerdictSatisfiesTheCheck(t *testing.T) {
	doc := services.NewTestsDocument(
		"### Test 1: a\n**Alignment:** aligned\n\n" +
			"### Test 2: b\n**Alignment:** partial\n**Alignment note:** only covers signup.\n\n" +
			"### Test 3: c\n**Alignment:** misaligned\n**Alignment note:** asserts internals.\n")

	if missing := doc.TestsMissingAlignment(); len(missing) != 0 {
		t.Fatalf("missing = %v, want none — all three carry a verdict", missing)
	}
}

func TestUnknownAlignmentWordDoesNotCountAsAVerdict(t *testing.T) {
	doc := services.NewTestsDocument("### Test 1: a\n**Alignment:** maybe\n")

	if missing := doc.TestsMissingAlignment(); len(missing) != 1 {
		t.Fatalf("missing = %v, want one — `maybe` is not a valid verdict", missing)
	}
}

func TestEmptyDocumentHasNoJourneyTests(t *testing.T) {
	if missing := services.NewTestsDocument("").JourneyTestsMissingDiagrams(); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
}
