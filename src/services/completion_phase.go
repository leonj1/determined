package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"determined/src/models"
)

type ReviewFinding struct{ ID, Specialist, Severity, Title, Evidence, Fix string }

var findingStart = regexp.MustCompile(`(?i)^FINDING:\s*(must-fix|advisory)\s*$`)

func (o *Orchestrator) initializeReviewPolicy() {
	plan, err := o.files.Read(o.cfg.PlanFile)
	if err != nil {
		return
	}
	o.profile = ReviewProfileOf(plan)
	o.remediationsLeft = o.cfg.RemediationBudget
	if o.remediationsLeft < 0 {
		o.remediationsLeft = BudgetFor(o.profile.Size)
	}
	if o.files.Exists(o.cfg.StopFile) {
		_ = o.files.Remove(o.cfg.StopFile)
	}
}

func (o *Orchestrator) runCompletionPhase(ctx context.Context) (models.Outcome, bool) {
	if !o.auditPassed {
		return o.runGoalAudit(ctx)
	}
	if o.cfg.SpecializedReviews {
		if outcome, stop := o.runRequiredSpecialists(ctx); stop {
			return outcome, true
		}
		if !AllStepsComplete(o.parsedSteps()) {
			return models.OutcomeStopped, false
		}
	}
	result := o.updateDocs(ctx)
	if result.stop {
		return result.outcome, true
	}
	if !result.succeeded && !result.skipped {
		return models.OutcomeStopped, false
	}
	if err := o.files.Write(o.cfg.StopFile, "determined: goal, audit, and required reviews satisfied\n"); err != nil {
		fmt.Fprintf(o.terminal, "determined: could not create %s: %v\n", o.cfg.StopFile, err)
		return models.OutcomeDroidFailed, true
	}
	return models.OutcomeStopped, true
}

func (o *Orchestrator) runGoalAudit(ctx context.Context) (models.Outcome, bool) {
	result := o.invokeReadOnly(ctx, auditPrompt, "auditing the whole plan")
	if result.stop || !result.succeeded {
		return result.outcome, result.stop
	}
	verdict, ok := ParseGateVerdict(result.output, "SATISFIED", "GAP")
	if !ok && o.files.Exists(o.cfg.StopFile) {
		_ = o.files.Remove(o.cfg.StopFile)
		o.auditPassed = true
		return models.OutcomeStopped, false
	}
	if !ok {
		outcome, stop := o.recordFailure(ctx, errors.New("whole-plan audit returned no parseable verdict"))
		return outcome, stop
	}
	if verdict.Token == "SATISFIED" {
		o.auditPassed = true
		return models.OutcomeStopped, false
	}
	if !o.spendRemediation("whole-plan audit", verdict.Rationale) {
		return models.OutcomeRemediationBudget, true
	}
	o.applyAuditGap(result.output, verdict.Rationale)
	return models.OutcomeStopped, false
}

func (o *Orchestrator) applyAuditGap(output, rationale string) {
	if n, err := strconv.Atoi(fieldValue(output, "STEP:")); err == nil && n > 0 {
		o.uncheckStep(n - 1)
	} else {
		title := fieldValue(output, "TEST:")
		if title == "" {
			title = "Resolve whole-plan audit gap"
		}
		o.appendStep("Audit remediation: "+title, "The implementation aligns with the goal.", rationale)
	}
	_ = o.files.Append("FIXES.md", fmt.Sprintf("- Audit gap: %s\n", rationale))
}

func (o *Orchestrator) runRequiredSpecialists(ctx context.Context) (models.Outcome, bool) {
	for _, name := range o.profile.RequiredSpecialists {
		if o.specialistsRun[name] {
			continue
		}
		review := reviewNamed(name)
		result := o.invokeReadOnly(ctx, specializedReviewPrompt(review), progressMessage("running "+name+" review"))
		if result.stop || !result.succeeded {
			return result.outcome, result.stop
		}
		o.specialistsRun[name] = true
		for _, finding := range ParseReviewFindings(result.output, name) {
			if finding.Severity == "advisory" || o.acceptedTradeOff(finding.Title) {
				_ = o.files.Append("NOTES.md", fmt.Sprintf("Advisory (%s): %s — %s\n", name, finding.Title, finding.Evidence))
				continue
			}
			if !o.queueFinding(finding) {
				return models.OutcomeRemediationBudget, true
			}
		}
	}
	return models.OutcomeStopped, false
}

func (o *Orchestrator) queueFinding(f ReviewFinding) bool {
	if !o.spendRemediation(f.Specialist, f.Title) {
		return false
	}
	o.nextFindingID++
	f.ID = fmt.Sprintf("F%d", o.nextFindingID)
	text := fmt.Sprintf("Remediation (%s) %s: %s", f.Specialist, f.ID, f.Title)
	o.pendingFindings[text] = f
	o.appendStep(text, fmt.Sprintf("The %s finding %s no longer applies.", f.Specialist, f.ID), "the targeted re-check answers RESOLVED.")
	_ = o.files.Append("FIXES.md", fmt.Sprintf("- %s %s: %s; evidence: %s; fix: %s\n", f.Specialist, f.ID, f.Title, f.Evidence, f.Fix))
	return true
}

func (o *Orchestrator) verifyRemediation(ctx context.Context, i int, step Step) (invocationResult, bool) {
	finding, ok := o.pendingFindings[strings.TrimSpace(step.Text)]
	if !ok {
		return invocationResult{}, false
	}
	result := o.invokeReadOnly(ctx, recheckPrompt(finding, step), progressMessage(fmt.Sprintf("re-checking finding %s", finding.ID)))
	if result.stop || !result.succeeded {
		return result, true
	}
	verdict, parsed := ParseGateVerdict(result.output, "RESOLVED", "UNRESOLVED")
	if !parsed {
		outcome, stop := o.recordFailure(ctx, errors.New("remediation re-check returned no parseable verdict"))
		return invocationResult{outcome: outcome, stop: stop}, true
	}
	if verdict.Token == "RESOLVED" {
		delete(o.pendingFindings, strings.TrimSpace(step.Text))
		return result, true
	}
	o.uncheckStep(i)
	_ = o.files.Append("FIXES.md", fmt.Sprintf("- %s remains unresolved: %s\n", finding.ID, verdict.Rationale))
	if !o.spendRemediation("re-check "+finding.ID, verdict.Rationale) {
		return invocationResult{outcome: models.OutcomeRemediationBudget, stop: true, succeeded: true}, true
	}
	return result, true
}

func (o *Orchestrator) spendRemediation(source, detail string) bool {
	if o.remediationsLeft <= 0 {
		fmt.Fprintf(o.terminal, "determined: remediation budget exhausted at %s: %s\n", source, detail)
		o.noteOpenRemediations()
		return false
	}
	o.remediationsLeft--
	return true
}

func (o *Orchestrator) noteOpenRemediations() {
	var open []string
	for _, step := range o.parsedSteps() {
		if !step.Completed {
			open = append(open, step.Text)
		}
	}
	_ = o.files.Append("NOTES.md", "\n## Remediation budget exhausted\n\n- "+strings.Join(open, "\n- ")+"\n")
}

func (o *Orchestrator) invokeReadOnly(ctx context.Context, prompt string, progress progressMessage) invocationResult {
	guard := o.guard
	paths := append(append([]string{}, o.cfg.ProtectedFiles...), o.cfg.StepsFile)
	o.guard = NewTamperGuard(o.files, paths)
	result := o.invoke(ctx, prompt, progress)
	o.guard = guard
	return result
}

func ParseReviewFindings(output, specialist string) []ReviewFinding {
	var result []ReviewFinding
	var current *ReviewFinding
	flush := func() {
		if current != nil && current.Title != "" {
			result = append(result, *current)
		}
		current = nil
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if match := findingStart.FindStringSubmatch(line); match != nil {
			flush()
			current = &ReviewFinding{Specialist: specialist, Severity: strings.ToLower(match[1])}
			continue
		}
		if line == "" {
			flush()
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case hasFoldPrefix(line, "TITLE:"):
			current.Title = fieldValue(line, "TITLE:")
		case hasFoldPrefix(line, "EVIDENCE:"):
			current.Evidence = fieldValue(line, "EVIDENCE:")
		case hasFoldPrefix(line, "FIX:"):
			current.Fix = fieldValue(line, "FIX:")
		}
	}
	flush()
	return result
}

func fieldValue(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if hasFoldPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func reviewNamed(name string) specializedReview {
	for _, review := range specializedReviewSequence() {
		if normalizeSpecialist(review.name) == name {
			return review
		}
	}
	return specializedReview{name: name, focus: name + " risks"}
}

func (o *Orchestrator) acceptedTradeOff(title string) bool {
	plan, err := o.files.Read(o.cfg.PlanFile)
	if err != nil {
		return false
	}
	section := strings.SplitN(plan, "## Accepted trade-offs", 2)
	if len(section) != 2 {
		return false
	}
	return strings.Contains(strings.ToLower(section[1]), strings.ToLower(title))
}

func (o *Orchestrator) appendStep(text, purpose, done string) {
	_ = o.files.Append(o.cfg.StepsFile, fmt.Sprintf("\n- [ ] %s\n  Purpose: %s\n  Done when: %s\n", text, purpose, done))
}

func (o *Orchestrator) uncheckStep(index int) {
	content, err := o.files.Read(o.cfg.StepsFile)
	if err != nil {
		return
	}
	lines, count := strings.Split(content, "\n"), -1
	for i, line := range lines {
		if _, ok := checkboxItem(line); ok {
			count++
		}
		if count == index && strings.Contains(line, "[x]") {
			lines[i] = strings.Replace(line, "[x]", "[ ]", 1)
			break
		}
	}
	_ = o.files.Write(o.cfg.StepsFile, strings.Join(lines, "\n"))
}
