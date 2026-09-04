package services

import (
	"regexp"
	"strings"

	"determined/src/models"
)

var planSizePattern = regexp.MustCompile(`(?im)^\*\*Size:\*\*\s*([^\s]+)\s*$`)

var stepCaps = map[models.PlanSize]int{
	models.PlanSizeTrivial: 3,
	models.PlanSizeSmall:   6,
	models.PlanSizeMedium:  12,
	models.PlanSizeLarge:   0,
}

// PlanSizeOf returns the recognized Size line from PLAN.md.
func PlanSizeOf(plan string) (models.PlanSize, bool) {
	match := planSizePattern.FindStringSubmatch(plan)
	if match == nil {
		return "", false
	}
	size := models.PlanSize(strings.ToLower(match[1]))
	_, ok := stepCaps[size]
	if !ok {
		return "", false
	}
	return size, true
}

// StepCap returns the maximum planned steps for size; zero means unlimited.
func StepCap(size models.PlanSize) int {
	return stepCaps[size]
}
