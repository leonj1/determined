package services

import "fmt"

// elaboratePrompt asks the planning tool for executable steps for one milestone.
func elaboratePrompt(m Milestone, revision int) string {
	return fmt.Sprintf("Read PLAN.md, MILESTONES.md, and NOTES.md if present. Elaborate milestone %d (%s), revision %d into STEPS.md. Working state: %s. Use one step per behavior change or refactor. Every step needs a Purpose: and Done when: and must include its own tests. Do not implement anything or change PLAN.md or MILESTONES.md.", m.Number, m.Name, revision, m.WorkingState)
}

// intentCheckPrompt defines the read-only pre-execution gate.
func intentCheckPrompt(m Milestone) string {
	return fmt.Sprintf("Read PLAN.md, MILESTONES.md and STEPS.md. Check that every step traces to the milestone's goal (%s) and includes its own tests. Do not modify any files. Return VERDICT: PASS or VERDICT: FAIL, followed by RATIONALE: and optional GUIDANCE:.", m.Goal)
}

// verifyMilestonePrompt defines the read-only post-execution gate.
func verifyMilestonePrompt(m Milestone, buildOutput string) string {
	return fmt.Sprintf("Read PLAN.md, MILESTONES.md and the implementation. Verify milestone %d working state: %s. Look for dead code, stubs, and unfinished flags. Automated build/test output:\n%s\nDo not modify any files. Return VERDICT: PASS or VERDICT: REPLAN followed by RATIONALE:.", m.Number, m.WorkingState, buildOutput)
}

// replanPrompt asks for a forward-only revision and changelog entry.
func replanPrompt(from int, reason string, revision int) string {
	return fmt.Sprintf("Read PLAN.md and MILESTONES.md. Revise MILESTONES.md from milestone %d forward for revision %d. Preserve verified earlier milestones and append the reason to ## Changelog. Do not modify PLAN.md or implementation files. Reason: %s", from, revision, reason)
}

// MilestonePlanProtocol describes the additional artifacts created by plan mode.
const MilestonePlanProtocol = "Create PLAN.md and MILESTONES.md, but do not write STEPS.md. Milestone 0 may be a feasibility spike. Milestone 1 must be a walking skeleton touching every architecture layer. Each later milestone adds one dimension, ordered by risk and dependency. Use `## Milestone N:` headings with Goal:, Working state:, Risks retired:, and Depends on:. End with Stubs and flags ledger, Open decisions, Out of scope, and an empty Changelog."
