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

func TestEmptyDocumentHasNoJourneyTests(t *testing.T) {
	if missing := services.NewTestsDocument("").JourneyTestsMissingDiagrams(); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
}
