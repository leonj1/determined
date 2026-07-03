package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"determined/src/models"
)

// ToolChangeVerifier asks the selected AI coding tool to review uncommitted
// changes before the orchestrator commits them.
type ToolChangeVerifier struct {
	runner CommandRunner
	tool   models.Tool
}

// NewToolChangeVerifier constructs a verifier backed by the selected tool.
func NewToolChangeVerifier(runner CommandRunner, tool models.Tool) ToolChangeVerifier {
	return ToolChangeVerifier{runner: runner, tool: tool}
}

// Verify runs the verifier prompt and parses a PASS/FAIL decision.
func (v ToolChangeVerifier) Verify(
	ctx context.Context,
	step models.Step,
	out io.Writer,
) (models.VerificationResult, error) {
	var captured bytes.Buffer
	inv := v.tool.Invocation(verificationPrompt(step))
	if err := v.runner.Run(ctx, inv, io.MultiWriter(out, &captured)); err != nil {
		return models.VerificationResult{}, err
	}
	return parseVerificationResult(captured.String()), nil
}

func verificationPrompt(step models.Step) string {
	return fmt.Sprintf("Review the uncommitted repository changes for this intended STEPS.md item:\n\nStep %d: %s\n\nValidate that the changes satisfy that exact step, follow AGENTS.md, and include or run reasonable tests when appropriate. Inspect PLAN.md, STEPS.md, AGENTS.md, and the git diff. Do not modify files. Respond with PASS if the changes should be committed. Respond with FAIL followed by specific insufficiencies and next fixes if anything is missing, wrong, untested, or outside the intended step.", step.Number, step.Text)
}

func parseVerificationResult(output string) models.VerificationResult {
	trimmed := strings.TrimSpace(output)
	for _, line := range strings.Split(trimmed, "\n") {
		decision := strings.ToUpper(strings.TrimSpace(line))
		if decision == "PASS" || strings.HasPrefix(decision, "PASS ") || strings.HasPrefix(decision, "PASS:") {
			return models.VerificationResult{Status: models.VerificationPassed, Feedback: trimmed}
		}
		if decision == "FAIL" || strings.HasPrefix(decision, "FAIL ") || strings.HasPrefix(decision, "FAIL:") {
			return models.VerificationResult{Status: models.VerificationFailed, Feedback: trimmed}
		}
	}
	return models.VerificationResult{
		Status:   models.VerificationFailed,
		Feedback: "Verifier did not return PASS. Output:\n" + trimmed,
	}
}
