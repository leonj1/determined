package services

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
)

type reviewFileStore struct {
	data      map[string]string
	readError map[string]error
	writeErr  error
}

func (f *reviewFileStore) Exists(path string) bool { _, ok := f.data[path]; return ok }
func (f *reviewFileStore) Read(path string) (string, error) {
	if err := f.readError[path]; err != nil {
		return "", err
	}
	value, ok := f.data[path]
	if !ok {
		return "", errors.New("missing file")
	}
	return value, nil
}
func (f *reviewFileStore) Write(path, content string) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.data[path] = content
	return nil
}
func (f *reviewFileStore) Append(path, content string) error { f.data[path] += content; return nil }
func (f *reviewFileStore) Remove(path string) error          { delete(f.data, path); return nil }

type reviewRunner struct{}

func (reviewRunner) Run(context.Context, models.Invocation, io.Writer) error { return nil }

type reviewClock struct{}

func (reviewClock) Now() time.Time { return time.Unix(0, 0) }

type reviewLogs struct{}

func (reviewLogs) OpenIteration(int) (io.WriteCloser, error) {
	return reviewWriteCloser{Writer: io.Discard}, nil
}

type reviewWriteCloser struct{ io.Writer }

func (reviewWriteCloser) Close() error { return nil }

func TestMilestoneParserIgnoresTrailerHeadingsInsideFences(t *testing.T) {
	content := "# Milestones\n\n## Milestone 1: first\nGoal: ship first\nWorking state: works\nRisks retired: none\nDepends on: none\n```markdown\n## Not a trailer\n```\n\n## Milestone 2: second\nGoal: ship second\nWorking state: works too\nRisks retired: none\nDepends on: 1\n"
	doc, err := ParseMilestones(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Milestones) != 2 {
		t.Fatalf("expected both milestones, got %d", len(doc.Milestones))
	}
}

func TestMilestoneTransitionsReturnIndependentState(t *testing.T) {
	doc, err := ParseMilestones("## Milestone 1: first\nGoal: ship\nWorking state: works\nRisks retired: none\nDepends on: none\n")
	if err != nil {
		t.Fatal(err)
	}
	original := NewMilestoneState(doc)
	approved := ApproveIntent(original, 1)
	if original.Milestones["1"].IntentChecked {
		t.Fatal("transition mutated the input state")
	}
	if !approved.Milestones["1"].IntentChecked {
		t.Fatal("returned state was not approved")
	}
}

func TestMilestoneDefinitionChangeInvalidatesVerifiedState(t *testing.T) {
	first, _ := ParseMilestones("## Milestone 1: first\nGoal: old goal\nWorking state: works\nRisks retired: none\nDepends on: none\n")
	changed, _ := ParseMilestones("## Milestone 1: first\nGoal: new goal\nWorking state: works\nRisks retired: none\nDepends on: none\n")
	state := MarkVerified(NewMilestoneState(first), 1)
	if sameMilestones(state, changed) {
		t.Fatal("changed goal must invalidate the persisted definition")
	}
	merged := mergeMilestoneState(state, changed)
	if merged.Milestones["1"].Verified {
		t.Fatal("changed milestone must not stay verified")
	}
}

func TestMilestoneRunStopsWhenInitialStateCannotPersist(t *testing.T) {
	files := &reviewFileStore{data: map[string]string{"MILESTONES.md": "## Milestone 1: first\nGoal: ship\nWorking state: works\nRisks retired: none\nDepends on: none\n"}, readError: map[string]error{}, writeErr: errors.New("disk full")}
	cfg := models.Config{MilestonesFile: "MILESTONES.md", MilestoneStateFile: ".determined/milestones.json"}
	o := NewMilestoneOrchestrator(reviewRunner{}, files, reviewClock{}, reviewLogs{}, io.Discard, cfg, nil, nil)
	if got := o.Run(context.Background()); got != models.OutcomeDroidFailed {
		t.Fatalf("expected persistence failure, got %v", got)
	}
}

func TestDivergenceReadFailureStopsInnerLoop(t *testing.T) {
	files := &reviewFileStore{data: map[string]string{"PLAN.md": "# Plan\n", "STEPS.md": "- [ ] work\n", "DIVERGENCE.md": "signal"}, readError: map[string]error{"DIVERGENCE.md": errors.New("permission denied")}}
	cfg := models.Config{PlanFile: "PLAN.md", StepsFile: "STEPS.md", DivergenceFile: "DIVERGENCE.md", Milestones: true, Tool: models.DroidTool{}, MaxConsecutiveFailures: 1}
	o := NewOrchestrator(reviewRunner{}, files, reviewClock{}, reviewLogs{}, io.Discard, cfg)
	if got := o.Run(context.Background()); got != models.OutcomeDroidFailed {
		t.Fatalf("expected read failure to abort, got %v", got)
	}
}

func TestMilestonePlanningPreservesSelectedMode(t *testing.T) {
	if prompt := BuildMilestonePlanningPrompts(models.PlanModeMVP).Plan; !strings.Contains(prompt, "This is MVP mode") {
		t.Fatalf("MVP quality missing from prompt: %s", prompt)
	}
	if prompt := BuildMilestonePlanningPrompts(models.PlanModePrototype).Plan; !strings.Contains(prompt, "prototype mode") {
		t.Fatalf("prototype quality missing from prompt: %s", prompt)
	}
}

func TestInvalidMilestonesDoNotDeleteSteps(t *testing.T) {
	files := &reviewFileStore{data: map[string]string{"PLAN.md": "# Plan\n", "MILESTONES.md": "invalid\n", "STEPS.md": "keep me\n"}, readError: map[string]error{}}
	o := &PlanOrchestrator{files: files, cfg: models.PlanConfig{Milestones: true, PlanFile: "PLAN.md", MilestonesFile: "MILESTONES.md", StepsFile: "STEPS.md"}}
	if o.planDrafted() {
		t.Fatal("invalid milestones must not be a complete draft")
	}
	if files.data["STEPS.md"] != "keep me\n" {
		t.Fatal("invalid milestone validation deleted STEPS.md")
	}
}
