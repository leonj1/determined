package services

import "determined/src/models"

const planProtocol = "Read GOAL.md and ANSWERS.md if it exists. Treat GOAL.md as authoritative. " +
	"If essential information is missing, write only high-impact clarifying questions to QUESTIONS.md " +
	"as a markdown numbered list, one question per line. Accept `use sensible defaults` as an answer. " +
	"Otherwise write PLAN.md and STEPS.md, and do not write QUESTIONS.md. " +
	"Classify the work and apply the matching template: greenfield (foundations and delivery), feature " +
	"(user behavior, integration, regression), bugfix (reproduction, cause, fix, regression), refactor " +
	"(preserved behavior and incremental checks), migration (compatibility, rollout, rollback), API " +
	"(contract, errors, tests), UI (states, accessibility, responsiveness), CLI (syntax, output, exit codes), " +
	"or integration (boundaries and failure handling). Use multiple templates when appropriate. " +
	"STEPS.md must be an ordered markdown checkbox list with one `- [ ]` item per focused step. " +
	"Every step must end with `Done when:` and a concrete acceptance condition. " +
	"Do not implement anything or create STOP.md. "

const standardQuality = "Before writing the files, enforce this quality gate: the intended outcome, target user/use case, " +
	"in-scope and out-of-scope boundaries, constraints, observable success criteria, material risks, and validation approach " +
	"must be known or explicitly documented as assumptions. Ask questions when a consequential answer cannot be safely assumed. " +
	"PLAN.md must record the task type and cover every gate item plus the applicable task template. "

const mvpQuality = "This is MVP mode. Keep planning lean. Require only the intended outcome, target user/use case, must-have " +
	"scope, key constraint, and observable core success behavior. Infer low-risk details and record consequential assumptions. " +
	"Apply only the parts of the task template needed to deliver the smallest usable version. "

const prototypeQuality = "This is prototype mode for an experiment. Ask questions only when work cannot begin without the " +
	"answer. Infer and state assumptions, prefer the shortest path to testing the idea, and omit production hardening, polish, " +
	"and exhaustive edge cases unless GOAL.md requires them. Keep PLAN.md and STEPS.md brief. `Done when:` may describe a " +
	"simple manual observation that proves the experiment. "

// PlanningPrompts returns mode-specific instructions for plan creation and review.
func PlanningPrompts(mode models.PlanMode) models.PlanningPrompts {
	quality := standardQuality
	if mode == models.PlanModeMVP {
		quality = mvpQuality
	}
	if mode == models.PlanModePrototype {
		quality = prototypeQuality
	}
	return models.PlanningPrompts{
		Plan:   "You are planning software before implementation. " + planProtocol + quality,
		Assess: assessmentPrompt(mode),
		Refine: refinementPrompt(mode),
	}
}

// ReviewPrompts returns instructions for interviewing the user about an
// existing plan before applying the resulting revisions.
func ReviewPrompts() models.PlanningPrompts {
	return models.PlanningPrompts{
		Assess: "Read GOAL.md if it exists, PLAN.md, STEPS.md, and REVIEW_ANSWERS.md if it exists. " +
			"Critique scope boundaries, assumptions, edge cases, risks, sequencing, dependencies, validation, and acceptance criteria. " +
			"Write every specific actionable finding as a markdown list item in REFINEMENTS.md, or exactly NONE when the plan is sound. " +
			"For each consequential finding that depends on user preference, product intent, or risk tolerance, also write one concrete " +
			"interview question to REVIEW_QUESTIONS.md as a markdown numbered list; include options and tradeoffs when useful. " +
			"Do not ask about choices that can be safely inferred. Do not modify the plan or implement anything.",
		Refine: "Read GOAL.md if it exists, PLAN.md, STEPS.md, REFINEMENTS.md, and REVIEW_ANSWERS.md if it exists. " +
			"Treat the user's review answers as authoritative and resolve every finding by rewriting PLAN.md and/or STEPS.md. " +
			"Preserve confirmed scope, make assumptions and edge-case decisions explicit, order dependencies, and give each incomplete " +
			"`- [ ]` step a concrete `Done when:` acceptance condition. Do not implement anything or create STOP.md.",
	}
}

func assessmentPrompt(mode models.PlanMode) string {
	criteria := "Review PLAN.md and STEPS.md against the full quality gate and applicable task template. "
	if mode == models.PlanModeMVP {
		criteria = "Review PLAN.md and STEPS.md against the reduced MVP quality gate and only essential task-template concerns. "
	}
	return criteria + "Also flag steps that are too large, depend on unstated work, are out of order, or have vague `Done when:` " +
		"criteria such as `works correctly`, `is implemented`, `looks good`, or unqualified `tests pass`. " +
		"Write each specific, actionable finding as a markdown list item in REFINEMENTS.md. " +
		"If there are no findings, write exactly NONE. Do not modify the plan or implement anything."
}

func refinementPrompt(mode models.PlanMode) string {
	return "Read GOAL.md, PLAN.md, STEPS.md, and REFINEMENTS.md. Resolve every listed planning issue by rewriting PLAN.md " +
		"and/or STEPS.md while preserving the user's scope. Split oversized steps, make dependencies explicit and ordered, " +
		"and replace vague acceptance criteria with commands or observable behavior specific to the step. Apply the " +
		string(mode) + " quality gate and task template. Keep each step as one incomplete `- [ ]` item ending in `Done when:`. " +
		"Do not implement anything or create STOP.md."
}
