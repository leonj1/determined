package services

import "fmt"

func recheckPrompt(f ReviewFinding, step Step) string {
	return fmt.Sprintf("Read the implementation and verify only finding %s from the %s review. Finding: %s. Evidence: %s. Required fix: %s. Acceptance criterion: %s. Write nothing. End with VERDICT: RESOLVED or VERDICT: UNRESOLVED and, when unresolved, RATIONALE: <what remains>. Do not modify any file.", f.ID, f.Specialist, f.Title, f.Evidence, f.Fix, step.DoneWhen)
}
