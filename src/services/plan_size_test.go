package services_test

import (
	"testing"

	"determined/src/models"
	"determined/src/services"
)

func TestPlanSizeOfReadsTheSizeLine(t *testing.T) {
	cases := map[string]struct {
		size models.PlanSize
		ok   bool
	}{
		"# PLAN\n\n## Size\n\n**Size:** trivial\n": {models.PlanSizeTrivial, true},
		"## Size\n**Size:** Large\n":               {models.PlanSizeLarge, true},
		"# PLAN\nno size here\n":                   {"", false},
		"**Size:** enormous\n":                     {"", false},
	}
	for plan, want := range cases {
		got, ok := services.PlanSizeOf(plan)
		if ok != want.ok || got != want.size {
			t.Fatalf("PlanSizeOf(%q) = (%q, %v), want (%q, %v)", plan, got, ok, want.size, want.ok)
		}
	}
}

func TestStepCapPerSize(t *testing.T) {
	caps := map[models.PlanSize]int{
		models.PlanSizeTrivial: 3,
		models.PlanSizeSmall:   6,
		models.PlanSizeMedium:  12,
		models.PlanSizeLarge:   0,
	}
	for size, want := range caps {
		if got := services.StepCap(size); got != want {
			t.Fatalf("StepCap(%s) = %d, want %d", size, got, want)
		}
	}
}
