package services_test

import (
	"strings"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

func TestStandardPlanRequiresQualityGateAndTaskTemplate(t *testing.T) {
	prompts := services.PlanningPrompts(models.PlanModeStandard)
	for _, expected := range []string{"out-of-scope", "observable success", "material risks", "bugfix", "migration"} {
		if !strings.Contains(prompts.Plan, expected) {
			t.Fatalf("expected standard planning prompt to contain %q", expected)
		}
	}
}

func TestStandardAssessmentFindsVagueAcceptanceCriteria(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Assess
	for _, expected := range []string{"vague `Done when:`", "works correctly", "unqualified `tests pass`", "REFINEMENTS.md"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected assessment prompt to contain %q", expected)
		}
	}
}

func TestMVPPlanUsesReducedQualityGate(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeMVP).Plan
	for _, expected := range []string{"MVP mode", "must-have", "smallest usable version"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected MVP prompt to contain %q", expected)
		}
	}
}

func TestPrototypePlanPrioritizesExperimentation(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModePrototype).Plan
	for _, expected := range []string{"prototype mode", "Ask questions only", "shortest path", "manual observation"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prototype prompt to contain %q", expected)
		}
	}
}

func TestReviewInterviewsUserAboutConsequentialFindings(t *testing.T) {
	prompts := services.ReviewPrompts()
	for _, expected := range []string{"assumptions", "edge cases", "risk tolerance", "REVIEW_QUESTIONS.md", "options and tradeoffs"} {
		if !strings.Contains(prompts.Assess, expected) {
			t.Fatalf("expected review assessment prompt to contain %q", expected)
		}
	}
	for _, expected := range []string{"REVIEW_ANSWERS.md", "authoritative", "Do not implement"} {
		if !strings.Contains(prompts.Refine, expected) {
			t.Fatalf("expected review refinement prompt to contain %q", expected)
		}
	}
}
