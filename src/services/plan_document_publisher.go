package services

import (
	"strings"

	"determined/src/models"
)

// PlanDocumentSink receives plan documents prepared for a status snapshot.
type PlanDocumentSink interface {
	SetGoal(goal string)
	SetPlan(plan string)
	SetDemo(demo string)
	SetTests(tests string)
	SetTaskSteps(steps []models.TaskStep)
}

// PlanDocumentPublisher publishes planning protocol files to a status sink.
type PlanDocumentPublisher struct {
	files FileStore
	cfg   models.PlanConfig
}

// NewPlanDocumentPublisher wires protocol file access and names.
func NewPlanDocumentPublisher(files FileStore, cfg models.PlanConfig) *PlanDocumentPublisher {
	return &PlanDocumentPublisher{files: files, cfg: cfg}
}

// Publish sends every available planning document to the sink.
func (p *PlanDocumentPublisher) Publish(sink PlanDocumentSink) {
	p.PublishGoal(sink)
	p.PublishPlan(sink)
}

// PublishGoal sends GOAL.md when it can be read.
func (p *PlanDocumentPublisher) PublishGoal(sink PlanDocumentSink) {
	if goal, err := p.files.Read(p.cfg.GoalFile); err == nil {
		sink.SetGoal(goal)
	}
}

// PublishPlan sends the plan, optional demo, tests, and parsed task steps.
func (p *PlanDocumentPublisher) PublishPlan(sink PlanDocumentSink) {
	if p.files.Exists(p.cfg.PlanFile) {
		if plan, err := p.files.Read(p.cfg.PlanFile); err == nil {
			sink.SetPlan(plan)
		}
	}
	p.publishDemo(sink)
	p.publishTests(sink)
	p.publishTaskSteps(sink)
}

// Assumptions returns the `## Assumptions` section body from PLAN.md, or ""
// when the plan or the heading is absent. Old-format plans without the
// section are valid, so this never reports an error.
func (p *PlanDocumentPublisher) Assumptions() string {
	plan, err := p.files.Read(p.cfg.PlanFile)
	if err != nil {
		return ""
	}
	return assumptionsSection(plan)
}

// assumptionsSection extracts the body between the `## Assumptions` heading
// and the next level-one or level-two heading (or end of document).
func assumptionsSection(plan string) string {
	lines := strings.Split(plan, "\n")
	start := assumptionsStart(lines)
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if sectionHeading(lines[i]) {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func assumptionsStart(lines []string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) == "## Assumptions" {
			return i + 1
		}
	}
	return -1
}

func sectionHeading(line string) bool {
	return strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")
}

func (p *PlanDocumentPublisher) publishDemo(sink PlanDocumentSink) {
	if p.cfg.DemoFile == "" || !p.files.Exists(p.cfg.DemoFile) {
		return
	}
	if demo, err := p.files.Read(p.cfg.DemoFile); err == nil {
		sink.SetDemo(demo)
	}
}

func (p *PlanDocumentPublisher) publishTests(sink PlanDocumentSink) {
	if !p.files.Exists(p.cfg.TestsFile) {
		return
	}
	if tests, err := p.files.Read(p.cfg.TestsFile); err == nil {
		sink.SetTests(tests)
	}
}

func (p *PlanDocumentPublisher) publishTaskSteps(sink PlanDocumentSink) {
	if !p.files.Exists(p.cfg.StepsFile) {
		return
	}
	content, err := p.files.Read(p.cfg.StepsFile)
	if err != nil {
		return
	}
	sink.SetTaskSteps(taskSteps(ParseSteps(content)))
}

// taskSteps converts parsed steps into the status page's task-step model.
func taskSteps(steps []Step) []models.TaskStep {
	out := make([]models.TaskStep, len(steps))
	for i, step := range steps {
		out[i] = models.TaskStep{Text: step.Text, Purpose: step.Purpose, DoneWhen: step.DoneWhen, Completed: step.Completed}
	}
	return out
}
