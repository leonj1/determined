package services_test

import (
	"testing"

	"determined/src/models"
	"determined/src/services"
)

type fakeDocumentSink struct {
	goals []string
	plans []string
	tests []string
	steps [][]models.TaskStep
}

func (s *fakeDocumentSink) SetGoal(goal string)                  { s.goals = append(s.goals, goal) }
func (s *fakeDocumentSink) SetPlan(plan string)                  { s.plans = append(s.plans, plan) }
func (s *fakeDocumentSink) SetTests(tests string)                { s.tests = append(s.tests, tests) }
func (s *fakeDocumentSink) SetTaskSteps(steps []models.TaskStep) { s.steps = append(s.steps, steps) }

func documentConfig() models.PlanConfig {
	return models.PlanConfig{
		GoalFile:  "GOAL.md",
		PlanFile:  "PLAN.md",
		TestsFile: "TESTS.md",
		StepsFile: "STEPS.md",
	}
}

func TestUserCanResumeWithAllPlanningDocuments(t *testing.T) {
	files := newFakeFileStore()
	files.data["GOAL.md"] = "ship the retry loop"
	files.data["PLAN.md"] = "# Plan\n\nBuild it."
	files.data["TESTS.md"] = "# Tests\n\nRetry after failure."
	files.data["STEPS.md"] = "- [x] Build status seeding\n- [ ] Add retries\n"
	sink := &fakeDocumentSink{}

	services.NewPlanDocumentPublisher(files, documentConfig()).Publish(sink)

	if len(sink.goals) != 1 || sink.goals[0] != files.data["GOAL.md"] {
		t.Fatalf("goals = %#v, want exact GOAL.md", sink.goals)
	}
	if len(sink.plans) != 1 || sink.plans[0] != files.data["PLAN.md"] {
		t.Fatalf("plans = %#v, want exact PLAN.md", sink.plans)
	}
	if len(sink.tests) != 1 || sink.tests[0] != files.data["TESTS.md"] {
		t.Fatalf("tests = %#v, want exact TESTS.md", sink.tests)
	}
	want := []models.TaskStep{{Text: "Build status seeding", Completed: true}, {Text: "Add retries"}}
	if len(sink.steps) != 1 || len(sink.steps[0]) != len(want) {
		t.Fatalf("steps = %+v, want %+v", sink.steps, want)
	}
	for i := range want {
		if sink.steps[0][i] != want[i] {
			t.Errorf("steps[%d] = %+v, want %+v", i, sink.steps[0][i], want[i])
		}
	}
}

func TestMissingPlanningDocumentsLeavePagePlaceholders(t *testing.T) {
	sink := &fakeDocumentSink{}

	services.NewPlanDocumentPublisher(newFakeFileStore(), documentConfig()).Publish(sink)

	if len(sink.goals) != 0 || len(sink.plans) != 0 || len(sink.tests) != 0 || len(sink.steps) != 0 {
		t.Fatalf("published missing documents: %+v", sink)
	}
}

func TestGoalOnlyResumePublishesNothingElse(t *testing.T) {
	files := newFakeFileStore()
	files.data["GOAL.md"] = "goal only"
	sink := &fakeDocumentSink{}

	services.NewPlanDocumentPublisher(files, documentConfig()).Publish(sink)

	if len(sink.goals) != 1 || sink.goals[0] != "goal only" {
		t.Fatalf("goals = %#v, want goal only", sink.goals)
	}
	if len(sink.plans) != 0 || len(sink.tests) != 0 || len(sink.steps) != 0 {
		t.Fatalf("published non-goal documents: %+v", sink)
	}
}
