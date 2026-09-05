package services_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

func TestCompletionAuditsThenRunsSelectedSpecialistOnce(t *testing.T) {
	fs := plannedFileStore("- [x] Store token.\n  Purpose: Persist the token.\n  Done when: token survives reload.\n")
	fs.Write("GOAL.md", "Store the token in localStorage.")
	fs.Write("PLAN.md", "## Size\n\n**Size:** small\n\n## Review profile\n\n**Task type:** feature\n**Risk tags:** secrets\n**Reviews required:** security\n")
	runner := &fakeRunner{script: func(call int, out io.Writer) error {
		switch call {
		case 1:
			io.WriteString(out, "VERDICT: SATISFIED\n")
		case 2:
			io.WriteString(out, "FINDING: must-fix\nTITLE: token is logged\nEVIDENCE: debug log contains token\nFIX: remove log\n")
		case 3:
			content, _ := fs.Read("STEPS.md")
			fs.Write("STEPS.md", strings.Replace(content, "- [ ] Remediation", "- [x] Remediation", 1))
		case 4:
			io.WriteString(out, "VERDICT: RESOLVED\n")
		}
		return nil
	}}
	cfg := config(0)
	cfg.SpecializedReviews = true
	cfg.RemediationBudget = -1
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)

	if outcome := o.Run(context.Background()); outcome != models.OutcomeStopped {
		t.Fatalf("outcome = %v", outcome)
	}
	if runner.calls != 5 {
		t.Fatalf("calls = %d, want audit, security, work, re-check, docs", runner.calls)
	}
	prompts := make([]string, runner.calls)
	for i := range prompts {
		prompts[i] = runner.prompt(i + 1)
	}
	joined := strings.Join(prompts, "\n")
	if strings.Count(joined, "VERDICT: SATISFIED") != 1 || strings.Count(joined, "independent security specialist") != 1 {
		t.Fatalf("audit or security reran: %s", joined)
	}
	if strings.Contains(joined, "performance specialist") || strings.Contains(joined, "reliability and maintainability specialist") {
		t.Fatalf("unselected specialist ran: %s", joined)
	}
}
