package services

import (
	"strings"
	"testing"
)

func TestSpecialistsDoNotReopenAcceptedTradeOffs(t *testing.T) {
	for _, review := range specializedReviewSequence() {
		prompt := specializedReviewPrompt(review)
		for _, expected := range []string{
			"`## Accepted trade-offs`", "Do not report a finding that restates one of them",
			"do not reopen or append a step for it", "Advisory (" + review.name + "):",
		} {
			if !strings.Contains(prompt, expected) {
				t.Fatalf("expected %s prompt to contain %q", review.name, expected)
			}
		}
	}
}
