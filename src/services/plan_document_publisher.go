package services

import "determined/src/models"

// PlanDocumentSink receives plan documents prepared for a status snapshot.
type PlanDocumentSink interface {
	SetGoal(goal string)
	SetPlan(plan string)
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

// PublishPlan sends the plan, tests, and parsed task steps when available.
func (p *PlanDocumentPublisher) PublishPlan(sink PlanDocumentSink) {
	if p.files.Exists(p.cfg.PlanFile) {
		if plan, err := p.files.Read(p.cfg.PlanFile); err == nil {
			sink.SetPlan(plan)
		}
	}
	p.publishTests(sink)
	p.publishTaskSteps(sink)
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
