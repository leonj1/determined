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

func TestPlanRequiresRecommendedTestsFile(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Plan
		for _, expected := range []string{"TESTS.md", "up to 3 recommended tests", "fewer when", "journey test", "Given/When/Then", "```gherkin"} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s planning prompt to contain %q", mode, expected)
			}
		}
	}
}

func TestPlanRequiresAssumptionsSection(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Plan
		for _, expected := range []string{"`## Assumptions` heading", "every assumption and chosen default", "one markdown list item per assumption"} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s planning prompt to contain %q", mode, expected)
			}
		}
	}
}

func TestDemoPromptOnlyAllowsTrivialSelfContainedUIChanges(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Demo
	for _, expected := range []string{
		"after planning is complete", "trivial UI change", "self-contained HTML", "If and only if",
		"DEMO.html", "no UI change", "do not create DEMO.html", "Do not modify GOAL.md",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected demo prompt to contain %q, got:\n%s", expected, prompt)
		}
	}
}

func TestTestsPromptBackfillsOnlyTheTestsFile(t *testing.T) {
	for _, prompt := range []string{
		services.PlanningPrompts(models.PlanModeStandard).Tests,
		services.ReviewPrompts().Tests,
	} {
		for _, expected := range []string{
			"Write only TESTS.md",
			"up to 3 recommended tests",
			"fewer when",
			"journey test",
			"Given/When/Then",
			"```gherkin",
			"Do not modify PLAN.md or STEPS.md",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected tests prompt to contain %q, got:\n%s", expected, prompt)
			}
		}
	}
}

func TestPlanRequiresPurposeLinePerStep(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Plan
		for _, expected := range []string{"`Purpose:`", "functional intent", "not the technical mechanics", "throttled to prevent DDOS"} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s planning prompt to contain %q", mode, expected)
			}
		}
	}
}

func TestAssessmentFlagsTechnicalPurposeLines(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Assess
	for _, expected := range []string{"`Purpose:` line", "restates the technical action"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected assessment prompt to contain %q", expected)
		}
	}
}

func TestRefinementKeepsPurposeLineRequirement(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Refine
	if !strings.Contains(prompt, "`Purpose:` line") {
		t.Fatalf("expected refinement prompt to contain %q", "`Purpose:` line")
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

func TestStandardAssessmentRejectsStepsThatRequireImplementerAssumptions(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Assess
	expectedInstructions := []string{
		"no unstated context",
		"Do not fill in missing details yourself",
		"one bounded change",
		"without inventing requirements",
		"explicit prerequisites",
		"consequential design choices",
		"step-specific `Done when:`",
		"reviewed independently",
	}
	for _, expected := range expectedInstructions {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected assessment prompt to contain %q", expected)
		}
	}
}

func TestAssessmentSplitsFindingsFromPreferenceQuestions(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Assess
		for _, expected := range []string{
			"objective finding as a markdown list item in REFINEMENTS.md",
			"depends on user preference, product intent, or risk tolerance",
			"QUESTIONS.md as a markdown numbered list",
			"do not resolve preference-dependent findings yourself",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s assessment prompt to contain %q", mode, expected)
			}
		}
	}
}

func TestRefinementReadsUserAnswers(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Refine
		for _, expected := range []string{"ANSWERS.md if it exists", "answers in ANSWERS.md as authoritative"} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s refinement prompt to contain %q", mode, expected)
			}
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

func TestRealignPromptReplacesOnlyMisalignedTests(t *testing.T) {
	for _, prompt := range []string{
		services.PlanningPrompts(models.PlanModeStandard).Realign,
		services.ReviewPrompts().Realign,
	} {
		for _, expected := range []string{
			"`**Alignment:** misaligned` verdict",
			"replace it in place",
			"functional goal",
			"Keep every aligned and partial test and everything else in TESTS.md verbatim",
			"Do not modify PLAN.md or STEPS.md",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected realign prompt to contain %q, got:\n%s", expected, prompt)
			}
		}
	}
}
