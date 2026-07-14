package services

// criteriaHeader opens a fresh criteria file so readers — including the
// planning prompt and the final execution audit — know the listed tests are
// binding acceptance criteria, not suggestions.
const criteriaHeader = "# Acceptance Criteria\n\n" +
	"Each BDD journey test below must exist as an automated test and pass.\n\n"

// CriteriaPrompt returns the instruction for drafting one BDD journey test
// from the user's description and any revision requests.
func CriteriaPrompt() string {
	return "You are turning a user's described journey into one BDD acceptance test before any implementation exists. " +
		"Read CRITERIA_REQUEST.md: it contains the journey description under `# Journey`, " +
		"followed by any `## Revision` requests, newest last. " +
		"Read CRITERIA_DRAFT.md if it exists for the current proposal, and GOAL.md and CRITERIA.md " +
		"if they exist for context and consistency with already-accepted tests. " +
		"Write exactly one Gherkin feature with Given/When/Then scenarios covering the described journey " +
		"to CRITERIA_DRAFT.md, overwriting any previous draft and honoring every revision request. " +
		"Write only Gherkin source, with no markdown fences or commentary. " +
		"Do not implement anything, do not ask questions, and do not create or modify any other file."
}
