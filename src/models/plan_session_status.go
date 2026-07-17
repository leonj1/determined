package models

import "time"

// PlanPhase describes where an interactive planning session currently stands.
type PlanPhase string

const (
	// PlanPhaseRunning means the planning loop is still working.
	PlanPhaseRunning PlanPhase = "running"
	// PlanPhaseSucceeded means planning finished with an accepted plan.
	PlanPhaseSucceeded PlanPhase = "succeeded"
	// PlanPhaseFailed means planning ended without an accepted plan.
	PlanPhaseFailed PlanPhase = "failed"
)

// GitContext identifies the repository a planning session runs inside. Missing
// values carry explicit placeholders rather than empty strings so the status
// page never renders blanks.
type GitContext struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

// PlanStep is one timestamped workflow event emitted during planning.
type PlanStep struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// TaskStep is one checkbox item from the produced STEPS.md, rendered as a card
// on the status page's Steps tab.
type TaskStep struct {
	Text      string `json:"text"`
	Purpose   string `json:"purpose"`
	DoneWhen  string `json:"doneWhen"`
	Completed bool   `json:"completed"`
}

// LogEntry is one tool invocation's terminal output: the progress header the
// terminal shows as "==> [time] message" plus the tool's streamed output body.
type LogEntry struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	Body    string    `json:"body"`
}

// WithBody returns a copy of the entry with more output appended.
func (e LogEntry) WithBody(text string) LogEntry {
	e.Body += text
	return e
}

// PlanSessionStatus is the full snapshot the interactive status page renders.
// Each broadcast carries the whole snapshot; browsers re-render on receipt.
type PlanSessionStatus struct {
	Git             GitContext `json:"git"`
	Goal            string     `json:"goal"`
	Plan            string     `json:"plan"`
	Tests           string     `json:"tests"`
	Phase           PlanPhase  `json:"phase"`
	WaitingForInput bool       `json:"waitingForInput"`
	Steps           []PlanStep `json:"steps"`
	TaskSteps       []TaskStep `json:"taskSteps"`
	Log             []LogEntry `json:"log"`
	StartedAt       time.Time  `json:"startedAt"`
	EndedAt         time.Time  `json:"endedAt"`
}

// Duration returns the elapsed planning time: zero until the session starts,
// running time while active, and the final span once ended.
func (s PlanSessionStatus) Duration(now time.Time) time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	if s.EndedAt.IsZero() {
		return now.Sub(s.StartedAt)
	}
	return s.EndedAt.Sub(s.StartedAt)
}

// WithLogEntry returns a copy of the status with a new log entry appended.
func (s PlanSessionStatus) WithLogEntry(entry LogEntry) PlanSessionStatus {
	log := make([]LogEntry, len(s.Log), len(s.Log)+1)
	copy(log, s.Log)
	s.Log = append(log, entry)
	return s
}

// WithLogOutput returns a copy of the status with text appended to the last
// log entry's body. With no entries yet the text is dropped: output only makes
// sense under an invocation header.
func (s PlanSessionStatus) WithLogOutput(text string) PlanSessionStatus {
	if len(s.Log) == 0 {
		return s
	}
	log := make([]LogEntry, len(s.Log))
	copy(log, s.Log)
	log[len(log)-1] = log[len(log)-1].WithBody(text)
	s.Log = log
	return s
}

// WithStep returns a copy of the status with one more step appended.
func (s PlanSessionStatus) WithStep(step PlanStep) PlanSessionStatus {
	steps := make([]PlanStep, len(s.Steps), len(s.Steps)+1)
	copy(steps, s.Steps)
	s.Steps = append(steps, step)
	return s
}
