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

// ExecPhase describes where the follow-on execution run stands. The empty
// value means execution has not been requested.
type ExecPhase string

const (
	// ExecPhaseRequested means the page's Implement button fired and the
	// execute loop is about to start.
	ExecPhaseRequested ExecPhase = "requested"
	// ExecPhaseRunning means the execute loop is working through the steps.
	ExecPhaseRunning ExecPhase = "running"
	// ExecPhaseSucceeded means the execute loop finished with an approved run.
	ExecPhaseSucceeded ExecPhase = "succeeded"
	// ExecPhaseFailed means the execute loop ended without completing the plan.
	ExecPhaseFailed ExecPhase = "failed"
)

// ExplainPhase describes where the post-execution explanation stands. The
// empty value means no explanation was requested because execution has not
// completed successfully.
type ExplainPhase string

const (
	// ExplainPhaseRunning means the explanation invocation is working.
	ExplainPhaseRunning ExplainPhase = "running"
	// ExplainPhaseSucceeded means the explanation was produced and published.
	ExplainPhaseSucceeded ExplainPhase = "succeeded"
	// ExplainPhaseFailed means the explanation could not be produced or read.
	ExplainPhaseFailed ExplainPhase = "failed"
)

// QuizPhase describes where post-explanation quiz generation stands. The
// empty value means no quiz was requested because no explanation succeeded.
type QuizPhase string

const (
	// QuizPhaseRunning means the quiz invocation is working.
	QuizPhaseRunning QuizPhase = "running"
	// QuizPhaseSucceeded means the quiz was validated and published.
	QuizPhaseSucceeded QuizPhase = "succeeded"
	// QuizPhaseFailed means the quiz could not be produced, read, or validated.
	QuizPhaseFailed QuizPhase = "failed"
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

// QuizQuestion is one validated multiple-choice question grounded in a
// section of the generated explanation.
type QuizQuestion struct {
	Question      string   `json:"question"`
	Choices       []string `json:"choices"`
	CorrectIndex  int      `json:"correctIndex"`
	Rationale     string   `json:"rationale"`
	SourceSection string   `json:"sourceSection"`
}

// EntryState is one execution log entry's outcome, which the status page turns
// into the entry's background: running entries glow, and finished ones are
// tinted by whether the invocation was clean, produced a warning signal, or
// failed.
type EntryState string

const (
	// EntryStateRunning is an invocation that has not returned yet.
	EntryStateRunning EntryState = "running"
	// EntryStateOK is an invocation that finished with no warning signal.
	EntryStateOK EntryState = "ok"
	// EntryStateWarn is an invocation that finished but tampered with a
	// protected file, or whose step a verifier later unchecked.
	EntryStateWarn EntryState = "warn"
	// EntryStateError is an invocation that failed to run to completion.
	EntryStateError EntryState = "error"
)

// LogEntry is one tool invocation's terminal output: the progress header the
// terminal shows as "==> [time] message" plus the tool's streamed output body.
type LogEntry struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	Body    string    `json:"body"`
	// State is the invocation's outcome. Planning log entries leave it empty;
	// only the execution log tracks per-entry state.
	State EntryState `json:"state,omitempty"`
}

// WithState returns a copy of the entry carrying the given outcome.
func (e LogEntry) WithState(state EntryState) LogEntry {
	e.State = state
	return e
}

// WithBody returns a copy of the entry with more output appended.
func (e LogEntry) WithBody(text string) LogEntry {
	e.Body += text
	return e
}

// PlanSessionStatus is the full snapshot the interactive status page renders.
// Each broadcast carries the whole snapshot; browsers re-render on receipt.
type PlanSessionStatus struct {
	Git GitContext `json:"git"`
	// Tool names the AI CLI and model driving the session; the page header
	// shows the model (or the CLI's default when none was selected).
	Tool            ToolIdentity `json:"tool"`
	Goal            string       `json:"goal"`
	Plan            string       `json:"plan"`
	Demo            string       `json:"demo"`
	Tests           string       `json:"tests"`
	Phase           PlanPhase    `json:"phase"`
	WaitingForInput bool         `json:"waitingForInput"`
	Steps           []PlanStep   `json:"steps"`
	TaskSteps       []TaskStep   `json:"taskSteps"`
	Log             []LogEntry   `json:"log"`
	StartedAt       time.Time    `json:"startedAt"`
	EndedAt         time.Time    `json:"endedAt"`

	// PendingAnnotations is the queue of user feedback submitted from the page
	// and not yet applied by the AI tool.
	PendingAnnotations []Annotation `json:"pendingAnnotations"`

	// TaskControlAvailable reports whether a cancellable tool invocation is
	// running right now, so the page can offer Skip and Stop on the active
	// activity entry only while those commands can actually take effect.
	TaskControlAvailable bool `json:"taskControlAvailable"`

	// ImplementOffered reports whether the session accepts an Implement request
	// from the page once planning succeeds.
	ImplementOffered bool `json:"implementOffered"`
	// ExecPhase, ExecLog, and the execution timestamps describe the follow-on
	// execute run the Implement button starts; the page's Execution tab
	// renders them.
	ExecPhase     ExecPhase  `json:"execPhase"`
	ExecLog       []LogEntry `json:"execLog"`
	ExecStartedAt time.Time  `json:"execStartedAt"`
	ExecEndedAt   time.Time  `json:"execEndedAt"`

	// ExecStopReason explains why a failed execute run ended (stall, tool
	// failure, budget, interruption) and ExecAdvice is the remediation the
	// status page recommends to the user. Both are empty while execution runs
	// and after a successful run.
	ExecStopReason string `json:"execStopReason"`
	ExecAdvice     string `json:"execAdvice"`

	// Explanation is the post-execution walkthrough shown after a successful
	// execute run; ExplainPhase describes its generation state.
	Explanation  string       `json:"explanation"`
	ExplainPhase ExplainPhase `json:"explainPhase"`

	// Quiz is the client-scored knowledge check generated from the explanation;
	// QuizPhase describes its generation and validation state.
	Quiz      []QuizQuestion `json:"quiz"`
	QuizPhase QuizPhase      `json:"quizPhase"`
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

// WithExecLogEntry returns a copy of the status with a new execution log
// entry appended.
func (s PlanSessionStatus) WithExecLogEntry(entry LogEntry) PlanSessionStatus {
	log := make([]LogEntry, len(s.ExecLog), len(s.ExecLog)+1)
	copy(log, s.ExecLog)
	s.ExecLog = append(log, entry)
	return s
}

// WithExecLogOutput returns a copy of the status with text appended to the
// last execution log entry's body. With no entries yet the text is dropped:
// output only makes sense under an invocation header.
func (s PlanSessionStatus) WithExecLogOutput(text string) PlanSessionStatus {
	if len(s.ExecLog) == 0 {
		return s
	}
	log := make([]LogEntry, len(s.ExecLog))
	copy(log, s.ExecLog)
	log[len(log)-1] = log[len(log)-1].WithBody(text)
	s.ExecLog = log
	return s
}

// WithExecLogStateAt returns a copy of the status with the execution log entry
// at index i given the outcome. An out-of-range index leaves the log unchanged,
// so a caller holding a stale index cannot mis-tint an unrelated entry.
func (s PlanSessionStatus) WithExecLogStateAt(i int, state EntryState) PlanSessionStatus {
	if i < 0 || i >= len(s.ExecLog) {
		return s
	}
	log := make([]LogEntry, len(s.ExecLog))
	copy(log, s.ExecLog)
	log[i] = log[i].WithState(state)
	s.ExecLog = log
	return s
}

// WithoutRunningExecEntries returns a copy of the status with every execution
// log entry still marked running given the fallback state, so an aborted run
// leaves no entry glowing after the loop has ended.
func (s PlanSessionStatus) WithoutRunningExecEntries(fallback EntryState) PlanSessionStatus {
	log := make([]LogEntry, len(s.ExecLog))
	copy(log, s.ExecLog)
	for i, entry := range log {
		if entry.State == EntryStateRunning {
			log[i] = entry.WithState(fallback)
		}
	}
	s.ExecLog = log
	return s
}

// WithStep returns a copy of the status with one more step appended.
func (s PlanSessionStatus) WithStep(step PlanStep) PlanSessionStatus {
	steps := make([]PlanStep, len(s.Steps), len(s.Steps)+1)
	copy(steps, s.Steps)
	s.Steps = append(steps, step)
	return s
}
