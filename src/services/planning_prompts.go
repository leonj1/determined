package services

import "determined/src/models"

const testsRequirement = "TESTS.md must list up to 3 recommended tests that validate the goal is implemented — fewer when " +
	"the goal is small enough that fewer tests cover it: at least one " +
	"end-to-end journey test and at least one BDD test written as a Gherkin `Scenario` with Given/When/Then " +
	"steps. Format TESTS.md with one `### Test N: <name>` heading per test followed by its journey " +
	"narrative or its Gherkin scenario in a fenced ```gherkin block. " +
	"Every journey test must also include a mermaid sequence diagram of its flow in a fenced ```mermaid " +
	"block starting with `sequenceDiagram`, using only `participant` declarations and `->>` / `-->>` " +
	"message arrows between participants. "

// alignmentRequirement defines the per-test verdict every finished TESTS.md
// carries: does the test prove the plan's functional goal, or only its
// technical mechanics? The status page colours each test by this verdict.
const alignmentRequirement = "Every `### Test N` section must end with an alignment verdict that judges the test against the " +
	"functional goal of GOAL.md and PLAN.md — the user-facing outcome the plan exists to deliver — not its " +
	"technical mechanics. Write the verdict as a `**Alignment:** aligned`, `**Alignment:** partial`, or " +
	"`**Alignment:** misaligned` line. Use `aligned` only when passing the test proves the functional goal is " +
	"achieved. Use `partial` when it proves part of the goal or mixes functional and technical assertions. " +
	"Use `misaligned` when it asserts implementation details, internal structure, or anything that could pass " +
	"while the functional goal remains unmet. For `partial` and `misaligned`, add a following " +
	"`**Alignment note:** <reason>` line naming exactly which part of the functional goal the test fails to " +
	"cover. Omit the note line for `aligned`. "

const testsProtocol = "Read GOAL.md if it exists, PLAN.md, and STEPS.md. Write only TESTS.md. " +
	testsRequirement +
	alignmentRequirement +
	"Do not modify PLAN.md or STEPS.md, implement anything, or create STOP.md."

const demoProtocol = "Read GOAL.md, PLAN.md, and STEPS.md after planning is complete. " +
	"Decide whether the task includes a trivial UI change that can be usefully demonstrated as one small, " +
	"self-contained HTML document. A trivial demo requires no backend, build step, package installation, " +
	"external assets, or external network access. If and only if those conditions hold, write DEMO.html with " +
	"the proposed UI demonstration, using only inline HTML, CSS, and optional JavaScript. Make the demo focused " +
	"on the planned UI behavior and clearly label it as a demo. If the task has no UI change, the change is not " +
	"trivial, or a useful self-contained demo is not possible, do not create DEMO.html. Do not modify GOAL.md, " +
	"PLAN.md, STEPS.md, TESTS.md, or any implementation file, and do not create STOP.md."

// alignProtocol re-judges an existing TESTS.md whose tests lack alignment
// verdicts, adding them without rewriting the tests themselves.
const alignProtocol = "Read GOAL.md if it exists, PLAN.md, STEPS.md, and TESTS.md. Assess each recommended test " +
	"against the plan's functional goal and record the verdict in TESTS.md. " +
	alignmentRequirement +
	"Change nothing else in TESTS.md: keep every heading, narrative, mermaid diagram, and Gherkin scenario " +
	"exactly as written. Do not modify PLAN.md or STEPS.md, implement anything, or create STOP.md."

const annotateProtocol = "Read ANNOTATION.md. It contains the user's feedback on one section of the plan " +
	"documents, naming the section (goal/plan/steps/tests), the specific target within it when there is one, " +
	"and the requested adjustment. Apply the adjustment to the referenced file only — GOAL.md, PLAN.md, " +
	"STEPS.md, or TESTS.md — keeping all other content and files unchanged. Preserve each file's required " +
	"format: STEPS.md stays an ordered `- [ ]` checkbox list where every step has a `Purpose:` line and ends " +
	"with `Done when:` and a concrete acceptance condition; TESTS.md keeps one `### Test N: <name>` heading " +
	"per test with journey narratives, their mermaid sequence diagrams, and Gherkin scenarios in fenced " +
	"blocks, each test keeping its `**Alignment:**` verdict line — re-judged against the plan's functional " +
	"goal when the adjustment changes the test, with an `**Alignment note:**` line for partial or misaligned " +
	"verdicts. Do not modify ANNOTATION.md, implement anything, or create STOP.md."

const planProtocol = "Read GOAL.md and ANSWERS.md if it exists. Treat GOAL.md as authoritative. " +
	"If essential information is missing, write only high-impact clarifying questions to QUESTIONS.md " +
	"as a markdown numbered list, one question per line. Accept `use sensible defaults` as an answer. " +
	"Otherwise write PLAN.md, STEPS.md, and TESTS.md, and do not write QUESTIONS.md. " +
	testsRequirement +
	alignmentRequirement +
	"Classify the work and apply the matching template: greenfield (foundations and delivery), feature " +
	"(user behavior, integration, regression), bugfix (reproduction, cause, fix, regression), refactor " +
	"(preserved behavior and incremental checks), migration (compatibility, rollout, rollback), API " +
	"(contract, errors, tests), UI (states, accessibility, responsiveness), CLI (syntax, output, exit codes), " +
	"or integration (boundaries and failure handling). Use multiple templates when appropriate. " +
	"STEPS.md must be an ordered markdown checkbox list with one `- [ ]` item per focused step. " +
	"Every step must include a `Purpose:` line stating its functional intent — the user-facing or " +
	"system-level outcome the step exists to achieve, not the technical mechanics (for example " +
	"`Purpose: Email messages are throttled to prevent DDOS`, not `Add message payloads to a queue`). " +
	"Every step must end with `Done when:` and a concrete acceptance condition. " +
	"If CRITERIA.md exists, treat its BDD journey tests as required acceptance tests: include steps that " +
	"implement each one as an automated test, and require those tests to pass in the relevant `Done when:` conditions. " +
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
		Plan:     "You are planning software before implementation. " + planProtocol + quality,
		Demo:     demoProtocol,
		Assess:   assessmentPrompt(mode),
		Refine:   refinementPrompt(mode),
		Tests:    testsProtocol,
		Align:    alignProtocol,
		Annotate: annotateProtocol,
	}
}

// ReviewPrompts returns instructions for interviewing the user about an
// existing plan before applying the resulting revisions.
func ReviewPrompts() models.PlanningPrompts {
	return models.PlanningPrompts{
		Demo: demoProtocol,
		Assess: "Read GOAL.md if it exists, PLAN.md, STEPS.md, and REVIEW_ANSWERS.md if it exists. " +
			"Critique scope boundaries, assumptions, edge cases, risks, sequencing, dependencies, validation, and acceptance criteria. " +
			"Write every specific actionable finding as a markdown list item in REFINEMENTS.md, or exactly NONE when the plan is sound. " +
			"For each consequential finding that depends on user preference, product intent, or risk tolerance, also write one concrete " +
			"interview question to REVIEW_QUESTIONS.md as a markdown numbered list; include options and tradeoffs when useful. " +
			"Do not ask about choices that can be safely inferred. Do not modify the plan or implement anything.",
		Refine: "Read GOAL.md if it exists, PLAN.md, STEPS.md, REFINEMENTS.md, and REVIEW_ANSWERS.md if it exists. " +
			"Treat the user's review answers as authoritative and resolve every finding by rewriting PLAN.md and/or STEPS.md. " +
			"Preserve confirmed scope, make assumptions and edge-case decisions explicit, order dependencies, and give each incomplete " +
			"`- [ ]` step a `Purpose:` line stating its functional intent and a concrete `Done when:` acceptance condition. " +
			"Do not implement anything or create STOP.md.",
		Tests: testsProtocol,
		Align: alignProtocol,
	}
}

func assessmentPrompt(mode models.PlanMode) string {
	criteria := "Review PLAN.md and STEPS.md against the full quality gate and applicable task template. "
	if mode == models.PlanModeMVP {
		criteria = "Review PLAN.md and STEPS.md against the reduced MVP quality gate and only essential task-template concerns. "
	}
	return criteria + "Evaluate each step as a capable implementer with repository access but no unstated context, prior " +
		"conversation, or permission to make consequential design decisions. Do not fill in missing details yourself. Flag any " +
		"step that requires guessing about scope, location, behavior, dependencies, interfaces, or validation. A step passes only " +
		"when it identifies one bounded change, can begin without inventing requirements, has completed or explicit prerequisites, " +
		"settles or deliberately delegates consequential design choices, has a step-specific `Done when:`, states its functional " +
		"intent in a `Purpose:` line, and can be completed and reviewed independently. Also flag steps that are out of order, " +
		"have vague `Done when:` criteria such as `works correctly`, `is implemented`, `looks good`, or unqualified `tests pass`, " +
		"or whose `Purpose:` merely restates the technical action instead of the functional outcome it serves. " +
		"Write each specific, actionable finding as a markdown list item in REFINEMENTS.md. " +
		"If there are no findings, write exactly NONE. Do not modify the plan or implement anything."
}

func refinementPrompt(mode models.PlanMode) string {
	return "Read GOAL.md, PLAN.md, STEPS.md, and REFINEMENTS.md. Resolve every listed planning issue by rewriting PLAN.md " +
		"and/or STEPS.md while preserving the user's scope. Split oversized steps, make dependencies explicit and ordered, " +
		"and replace vague acceptance criteria with commands or observable behavior specific to the step. Apply the " +
		string(mode) + " quality gate and task template. Keep each step as one incomplete `- [ ]` item with a `Purpose:` line " +
		"stating its functional intent, ending in `Done when:`. " +
		"Do not implement anything or create STOP.md."
}
