package services

import (
	"reflect"
	"testing"

	"determined/src/models"
)

func TestReviewProfileSelectsSecurityForSmallSecretsPlan(t *testing.T) {
	plan := "## Size\n\n**Size:** small\n\n## Review profile\n\n**Task type:** feature\n**Risk tags:** secrets\n**Reviews required:** security\n"
	got := ReviewProfileOf(plan)
	if got.Size != models.PlanSizeSmall || BudgetFor(got.Size) != 2 {
		t.Fatalf("profile = %#v, budget = %d", got, BudgetFor(got.Size))
	}
	if !reflect.DeepEqual(got.RequiredSpecialists, []string{"security"}) {
		t.Fatalf("specialists = %#v, want security only", got.RequiredSpecialists)
	}
}

func TestReviewProfileFallsBackConservatively(t *testing.T) {
	got := ReviewProfileOf("legacy plan")
	want := []string{"security", "performance", "reliability"}
	if got.Size != models.PlanSizeLarge || !reflect.DeepEqual(got.RequiredSpecialists, want) {
		t.Fatalf("legacy profile = %#v, want large with all specialists", got)
	}
}

func TestRefinePassesScaleWithSize(t *testing.T) {
	cases := map[models.PlanSize]int{
		models.PlanSizeTrivial: 0, models.PlanSizeSmall: 1,
		models.PlanSizeMedium: 2, models.PlanSizeLarge: 5,
	}
	for size, want := range cases {
		if got := RefinePassesFor(size, 5); got != want {
			t.Errorf("RefinePassesFor(%s, 5) = %d, want %d", size, got, want)
		}
	}
}

func TestParseReviewFindingsReturnsTypedBlocks(t *testing.T) {
	output := "FINDING: must-fix\nTITLE: unsafe token\nEVIDENCE: rendered as HTML\nFIX: use textContent\n\nFINDING: advisory\nTITLE: naming\nEVIDENCE: vague name\nFIX: rename it\n"
	got := ParseReviewFindings(output, "security")
	if len(got) != 2 || got[0].Severity != "must-fix" || got[1].Severity != "advisory" {
		t.Fatalf("findings = %#v", got)
	}
	if empty := ParseReviewFindings("NO FINDINGS\n", "security"); len(empty) != 0 {
		t.Fatalf("NO FINDINGS parsed as %#v", empty)
	}
}

func TestRefinementIssuesIgnorePassedChecks(t *testing.T) {
	content := "- no finding: scope aligns\n- no finding: tests align\n- BLOCKING: step 2 has no acceptance criterion\n"
	got := RefinementIssues(content)
	if !reflect.DeepEqual(got, []string{"step 2 has no acceptance criterion"}) {
		t.Fatalf("issues = %#v", got)
	}
}
