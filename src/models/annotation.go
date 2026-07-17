package models

import (
	"strings"
	"time"
)

// AnnotationSection names the status-page section an annotation refers to.
type AnnotationSection string

const (
	// AnnotationSectionGoal targets GOAL.md.
	AnnotationSectionGoal AnnotationSection = "goal"
	// AnnotationSectionPlan targets PLAN.md.
	AnnotationSectionPlan AnnotationSection = "plan"
	// AnnotationSectionSteps targets STEPS.md.
	AnnotationSectionSteps AnnotationSection = "steps"
	// AnnotationSectionTests targets TESTS.md.
	AnnotationSectionTests AnnotationSection = "tests"
)

// File returns the plan document the section refers to, resolved against the
// configured file names.
func (s AnnotationSection) File(cfg PlanConfig) string {
	switch s {
	case AnnotationSectionGoal:
		return cfg.GoalFile
	case AnnotationSectionPlan:
		return cfg.PlanFile
	case AnnotationSectionSteps:
		return cfg.StepsFile
	case AnnotationSectionTests:
		return cfg.TestsFile
	}
	return ""
}

// Annotation is one piece of user feedback submitted from the status page,
// anchored to the section (and optionally a finer target within it) the user
// annotated so the AI tool knows exactly what to adjust.
type Annotation struct {
	At      time.Time         `json:"at"`
	Section AnnotationSection `json:"section"`
	Target  string            `json:"target"` // e.g. "Step 3: …", "Journey tests"; empty = whole section
	Comment string            `json:"comment"`
}

// Valid reports whether the annotation names a known section and carries a
// non-blank comment.
func (a Annotation) Valid() bool {
	switch a.Section {
	case AnnotationSectionGoal, AnnotationSectionPlan, AnnotationSectionSteps, AnnotationSectionTests:
		return strings.TrimSpace(a.Comment) != ""
	}
	return false
}
