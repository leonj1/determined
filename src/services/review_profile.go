package services

import (
	"regexp"
	"sort"
	"strings"

	"determined/src/models"
)

var profileLine = regexp.MustCompile(`(?im)^\*\*(Task type|Risk tags|Reviews required):\*\*\s*(.+)$`)

var sizeBudgets = map[models.PlanSize]int{
	models.PlanSizeTrivial: 1, models.PlanSizeSmall: 2,
	models.PlanSizeMedium: 4, models.PlanSizeLarge: 6,
}

var riskReviews = map[string]string{
	"secrets": "security", "auth": "security", "untrusted-input": "security",
	"rendered-html": "security", "network": "security", "filesystem": "security",
	"data-loop": "performance", "hot-io": "performance", "concurrency": "performance",
	"io-boundary": "reliability", "lifecycle": "reliability",
}

// ReviewProfileOf parses PLAN.md and applies conservative size defaults.
func ReviewProfileOf(plan string) models.ReviewProfile {
	size, ok := PlanSizeOf(plan)
	if !ok {
		size = models.PlanSizeLarge
	}
	p := models.ReviewProfile{Size: size}
	for _, match := range profileLine.FindAllStringSubmatch(plan, -1) {
		values := csvValues(match[2])
		switch strings.ToLower(match[1]) {
		case "task type":
			p.TaskTypes = values
		case "risk tags":
			p.RiskTags = values
		case "reviews required":
			p.RequiredSpecialists = values
		}
	}
	p.RequiredSpecialists = SpecialistsFor(p)
	return p
}

// SpecialistsFor unions explicit and tag-derived reviews with the size baseline.
func SpecialistsFor(p models.ReviewProfile) []string {
	set := map[string]bool{}
	for _, name := range p.RequiredSpecialists {
		set[normalizeSpecialist(name)] = true
	}
	for _, tag := range p.RiskTags {
		set[riskReviews[tag]] = true
	}
	if p.Size == models.PlanSizeMedium {
		set["reliability"] = true
	}
	if p.Size == models.PlanSizeLarge {
		set["security"], set["performance"], set["reliability"] = true, true, true
	}
	delete(set, "")
	ordered := []string{"security", "performance", "reliability"}
	result := make([]string, 0, len(set))
	for _, name := range ordered {
		if set[name] {
			result = append(result, name)
		}
	}
	return result
}

func normalizeSpecialist(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(v, "reliability") {
		return "reliability"
	}
	if v == "security" || v == "performance" {
		return v
	}
	return ""
}

func csvValues(value string) []string {
	if strings.EqualFold(strings.TrimSpace(value), "none") {
		return nil
	}
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}

// BudgetFor returns the default shared remediation budget for a size.
func BudgetFor(size models.PlanSize) int { return sizeBudgets[size] }

// RefinePassesFor applies the smaller of the configured and size allowances.
func RefinePassesFor(size models.PlanSize, cap int) int {
	allowance := map[models.PlanSize]int{
		models.PlanSizeTrivial: 0, models.PlanSizeSmall: 1,
		models.PlanSizeMedium: 2, models.PlanSizeLarge: cap,
	}[size]
	if cap <= 0 || allowance < cap {
		return allowance
	}
	return cap
}

func demoEligible(profile models.ReviewProfile) bool {
	if profile.Size != models.PlanSizeTrivial && profile.Size != models.PlanSizeSmall {
		return false
	}
	for _, taskType := range profile.TaskTypes {
		if taskType == "ui" || strings.Contains(taskType, "ui") {
			return true
		}
	}
	return false
}
