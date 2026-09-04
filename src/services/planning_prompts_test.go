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

func TestAssessmentAsksOnlyGoalClarifyingQuestions(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		prompt := services.PlanningPrompts(mode).Assess
		for _, expected := range []string{
			"objective finding as a markdown list item in REFINEMENTS.md",
			"only when GOAL.md is ambiguous about something the plan must implement",
			"needed to implement GOAL.md as written",
			"Never ask whether to add behaviour",
			"QUESTIONS.md as a markdown numbered list",
			"names the GOAL.md phrase it clarifies",
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

func TestReviewInterviewsUserOnlyToClarifyGoal(t *testing.T) {
	prompts := services.ReviewPrompts()
	for _, expected := range []string{"assumptions", "edge cases", "GOAL.md is ambiguous", "REVIEW_QUESTIONS.md", "Never ask whether to add behaviour"} {
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

func TestStandardPlanTriagesGoalSizeBeforeApplyingTemplates(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Plan
	for _, expected := range []string{
		"## Size", "**Size:**", "trivial", "small", "medium", "large",
		"at most 3 steps", "at most 6 steps", "at most 12 steps",
		"For trivial and small goals apply the lean gate",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected standard planning prompt to contain %q", expected)
		}
	}
	if strings.Index(prompt, "**Size:**") > strings.Index(prompt, "Classify the work and apply the matching template") {
		t.Fatal("size triage must precede the task-template instruction")
	}
}

func TestLeanGateIsSharedBetweenMVPAndSmallStandardPlans(t *testing.T) {
	lean := "Require only the intended outcome, target user/use case, must-have scope, key constraint, and observable core success behavior."
	if !strings.Contains(services.PlanningPrompts(models.PlanModeMVP).Plan, lean) ||
		!strings.Contains(services.PlanningPrompts(models.PlanModeStandard).Plan, lean) {
		t.Fatal("expected the lean gate text in MVP and standard prompts")
	}
}

func TestAssessmentAndRefinementCanShrinkPlans(t *testing.T) {
	for _, assess := range []string{
		services.PlanningPrompts(models.PlanModeStandard).Assess,
		services.PlanningPrompts(models.PlanModeMVP).Assess,
		services.ReviewPrompts().Assess,
	} {
		for _, expected := range []string{
			"merged into a neighbour", "exactly one caller", "GOAL.md does not require",
			"satisfy a convention rather than the goal", "Prefer removing a step over splitting one",
			"never the presence of an interface, Fake, wrapper type, or file",
			"exceeds its cap", "trivial 3", "small 6", "medium 12",
		} {
			if !strings.Contains(assess, expected) {
				t.Fatalf("expected assessment prompt to contain %q", expected)
			}
		}
	}
	refine := services.PlanningPrompts(models.PlanModeStandard).Refine
	for _, expected := range []string{"merge or delete steps", "Never add a step the findings do not require"} {
		if !strings.Contains(refine, expected) {
			t.Fatalf("expected refinement prompt to contain %q", expected)
		}
	}
}

func TestSimplifyPromptRemovesWhatTheGoalDoesNotRequire(t *testing.T) {
	prompt := services.PlanningPrompts(models.PlanModeStandard).Simplify
	for _, expected := range []string{
		"Read GOAL.md", "STEPS.md", "TESTS.md", "the goal does not require",
		"merge steps", "## Simplifications", "Do not implement anything or create STOP.md",
		"never the presence of an interface, Fake, wrapper type, or file",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected simplify prompt to contain %q", expected)
		}
	}
}

func TestAnswersNeverWidenTheGoal(t *testing.T) {
	prompts := services.PlanningPrompts(models.PlanModeStandard)
	for name, prompt := range map[string]string{
		"plan": prompts.Plan, "refine": prompts.Refine, "review": services.ReviewPrompts().Refine,
	} {
		for _, expected := range []string{
			"for the questions they answer, and for nothing else", "An answer never widens GOAL.md",
			"plan no step, design decision, or test for it",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s prompt to contain %q", name, expected)
			}
		}
	}
}

func TestPlanRecordsAcceptedTradeOffsAndProportionalTests(t *testing.T) {
	for _, mode := range []models.PlanMode{models.PlanModeStandard, models.PlanModeMVP, models.PlanModePrototype} {
		if !strings.Contains(services.PlanningPrompts(mode).Plan, "`## Accepted trade-offs` heading") {
			t.Fatalf("expected %s prompt to require accepted trade-offs", mode)
		}
	}
	for _, prompt := range []string{services.PlanningPrompts(models.PlanModeStandard).Plan, services.PlanningPrompts(models.PlanModeStandard).Tests} {
		for _, expected := range []string{
			"trivial or small goal, TESTS.md lists exactly one test", "no journey test and no mermaid diagram",
			"medium and large goals, list up to 3 tests",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected tests requirement to contain %q", expected)
			}
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
