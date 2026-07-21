package services

import (
	"sync"
	"time"

	"determined/src/models"
)

// PlanStatusService is the state hub for an interactive planning session. The
// orchestrator pushes updates in; the status server subscribes for full
// snapshots to broadcast to browsers. Every mutation notifies subscribers, and
// a new subscriber immediately receives the current snapshot so late-joining
// browsers render the full session so far.
type PlanStatusService struct {
	clock Clock

	mu          sync.Mutex
	status      models.PlanSessionStatus
	subscribers []chan models.PlanSessionStatus
	annotations chan struct{}
	implement   chan struct{}
}

// NewPlanStatusService wires a PlanStatusService with the session's git
// context already resolved (see clients.GitContextReader) and the identity of
// the AI tool driving the session.
func NewPlanStatusService(clock Clock, git models.GitContext, tool models.ToolIdentity) *PlanStatusService {
	return &PlanStatusService{
		clock: clock,
		status: models.PlanSessionStatus{
			Git:   git,
			Tool:  tool,
			Phase: models.PlanPhaseRunning,
			Steps: []models.PlanStep{},
		},
		annotations: make(chan struct{}, 1),
		implement:   make(chan struct{}, 1),
	}
}

// Snapshot returns the current session state.
func (s *PlanStatusService) Snapshot() models.PlanSessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Subscribe registers a snapshot channel primed with the current state. The
// returned cancel function must be called when the subscriber is done.
func (s *PlanStatusService) Subscribe() (<-chan models.PlanSessionStatus, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan models.PlanSessionStatus, 16)
	ch <- s.status
	s.subscribers = append(s.subscribers, ch)
	return ch, func() { s.unsubscribe(ch) }
}

func (s *PlanStatusService) unsubscribe(ch chan models.PlanSessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.subscribers {
		if existing == ch {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			return
		}
	}
}

// Start records the planning phase start time.
func (s *PlanStatusService) Start() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.StartedAt = s.clock.Now()
		return st
	})
}

// SetGoal publishes the goal text.
func (s *PlanStatusService) SetGoal(goal string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Goal = goal
		return st
	})
}

// SetPlan publishes the plan text.
func (s *PlanStatusService) SetPlan(plan string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Plan = plan
		return st
	})
}

// SetDemo publishes the optional self-contained UI demonstration.
func (s *PlanStatusService) SetDemo(demo string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Demo = demo
		return st
	})
}

// SetTests publishes the recommended tests text.
func (s *PlanStatusService) SetTests(tests string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Tests = tests
		return st
	})
}

// SetTaskSteps publishes the parsed STEPS.md checkbox items.
func (s *PlanStatusService) SetTaskSteps(steps []models.TaskStep) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.TaskSteps = steps
		return st
	})
}

// AddStep appends a timestamped workflow step, clearing any waiting flag.
func (s *PlanStatusService) AddStep(message string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.WaitingForInput = false
		return st.WithStep(models.PlanStep{At: s.clock.Now(), Message: message})
	})
}

// BeginLogEntry opens a new log entry for a tool invocation, mirroring the
// terminal's "==> [time] message" header.
func (s *PlanStatusService) BeginLogEntry(message string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		return st.WithLogEntry(models.LogEntry{At: s.clock.Now(), Message: message})
	})
}

// AppendLogOutput streams tool output into the current log entry.
func (s *PlanStatusService) AppendLogOutput(text string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		return st.WithLogOutput(text)
	})
}

// WaitForInput marks the session as blocked on terminal input, adding a
// visible step so the browser user knows to return to the terminal.
func (s *PlanStatusService) WaitForInput() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st = st.WithStep(models.PlanStep{At: s.clock.Now(), Message: "waiting for input on the terminal"})
		st.WaitingForInput = true
		return st
	})
}

// Finish records the end of the planning phase as succeeded or failed.
func (s *PlanStatusService) Finish(succeeded bool) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.EndedAt = s.clock.Now()
		st.WaitingForInput = false
		if succeeded {
			st.Phase = models.PlanPhaseSucceeded
		} else {
			st.Phase = models.PlanPhaseFailed
		}
		return st
	})
}

// SubmitAnnotation queues user feedback from the status page and signals the
// annotation channel so a waiting orchestrator can apply it.
func (s *PlanStatusService) SubmitAnnotation(a models.Annotation) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.PendingAnnotations = append(append([]models.Annotation{}, st.PendingAnnotations...), a)
		return st
	})
	select {
	case s.annotations <- struct{}{}:
	default: // already signalled; the drain loop empties the whole queue
	}
}

// TakeAnnotation pops the oldest pending annotation, reporting whether one
// existed. The updated queue is broadcast so the page reflects the drain.
func (s *PlanStatusService) TakeAnnotation() (models.Annotation, bool) {
	var taken models.Annotation
	var ok bool
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if len(st.PendingAnnotations) == 0 {
			return st
		}
		taken, ok = st.PendingAnnotations[0], true
		st.PendingAnnotations = append([]models.Annotation{}, st.PendingAnnotations[1:]...)
		return st
	})
	return taken, ok
}

// AnnotationSignal reports annotation arrivals: the channel receives one value
// per burst of submissions.
func (s *PlanStatusService) AnnotationSignal() <-chan struct{} {
	return s.annotations
}

// OfferImplement marks the session as accepting an Implement request from the
// page, which shows the button once planning succeeds.
func (s *PlanStatusService) OfferImplement() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ImplementOffered = true
		return st
	})
}

// RequestImplement queues one execution request from the page's Implement
// button. Requests are ignored unless implementation was offered, planning
// succeeded, and execution is either unstarted or failed, so repeated clicks
// start at most one run while successful execution remains final.
func (s *PlanStatusService) RequestImplement() {
	requested := false
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if !st.ImplementOffered || st.Phase != models.PlanPhaseSucceeded || !execRetryable(st.ExecPhase) {
			return st
		}
		st.ExecPhase = models.ExecPhaseRequested
		requested = true
		return st
	})
	if !requested {
		return
	}
	select {
	case s.implement <- struct{}{}:
	default: // already signalled
	}
}

// execRetryable reports whether the page may request a new execute run.
func execRetryable(phase models.ExecPhase) bool {
	return phase == "" || phase == models.ExecPhaseFailed
}

// ImplementSignal reports Implement requests: the channel receives one value
// per accepted request.
func (s *PlanStatusService) ImplementSignal() <-chan struct{} {
	return s.implement
}

// StartExecution records a fresh timing window for the execute loop while
// retaining the cumulative execution log from prior attempts.
func (s *PlanStatusService) StartExecution() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ExecPhase = models.ExecPhaseRunning
		st.ExecStartedAt = s.clock.Now()
		st.ExecEndedAt = time.Time{}
		return st
	})
}

// FinishExecution records the end of the execute loop as succeeded or failed.
// A still-running entry at this point belongs to an invocation the loop never
// settled — an abort or crash — so it takes the run's own outcome rather than
// glowing forever.
func (s *PlanStatusService) FinishExecution(succeeded bool) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ExecEndedAt = s.clock.Now()
		if succeeded {
			st.ExecPhase = models.ExecPhaseSucceeded
		} else {
			st.ExecPhase = models.ExecPhaseFailed
		}
		return st.WithoutRunningExecEntries(stateFor(succeeded))
	})
}

// stateFor maps a run outcome onto the entry state an unsettled entry inherits.
func stateFor(succeeded bool) models.EntryState {
	if succeeded {
		return models.EntryStateOK
	}
	return models.EntryStateError
}

// StartExplanation marks the post-execution explanation as being generated.
func (s *PlanStatusService) StartExplanation() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ExplainPhase = models.ExplainPhaseRunning
		return st
	})
}

// SetExplanation publishes the generated markdown walkthrough.
func (s *PlanStatusService) SetExplanation(text string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Explanation = text
		return st
	})
}

// FinishExplanation records whether the explanation was produced and read.
func (s *PlanStatusService) FinishExplanation(succeeded bool) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if succeeded {
			st.ExplainPhase = models.ExplainPhaseSucceeded
		} else {
			st.ExplainPhase = models.ExplainPhaseFailed
		}
		return st
	})
}

// StartQuiz marks the post-explanation quiz as being generated.
func (s *PlanStatusService) StartQuiz() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.QuizPhase = models.QuizPhaseRunning
		return st
	})
}

// SetQuiz publishes the validated multiple-choice questions.
func (s *PlanStatusService) SetQuiz(questions []models.QuizQuestion) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Quiz = questions
		return st
	})
}

// FinishQuiz records whether the quiz was produced and validated.
func (s *PlanStatusService) FinishQuiz(succeeded bool) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if succeeded {
			st.QuizPhase = models.QuizPhaseSucceeded
		} else {
			st.QuizPhase = models.QuizPhaseFailed
		}
		return st
	})
}

// BeginExecLogEntry opens a new execution log entry for a tool invocation,
// mirroring the terminal's "==> [time] message" header. The entry starts in the
// running state, which the page renders as a glow, and its index is returned so
// the caller can settle that entry once the invocation's outcome is known.
func (s *PlanStatusService) BeginExecLogEntry(message string) int {
	i := -1
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st = st.WithExecLogEntry(models.LogEntry{
			At:      s.clock.Now(),
			Message: message,
			State:   models.EntryStateRunning,
		})
		i = len(st.ExecLog) - 1
		return st
	})
	return i
}

// SettleExecLogEntryAt records the outcome of an execution log entry, which the
// status page renders as the entry's background. Taking an index rather than
// always settling the last entry lets a signal that arrives late — a verifier
// unchecking the step it just reviewed — revise the right entry.
func (s *PlanStatusService) SettleExecLogEntryAt(i int, state models.EntryState) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		return st.WithExecLogStateAt(i, state)
	})
}

// AppendExecLogOutput streams tool output into the current execution log entry.
func (s *PlanStatusService) AppendExecLogOutput(text string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		return st.WithExecLogOutput(text)
	})
}

// Progress implements ProgressSink so orchestrator progress messages appear as
// steps on the status page.
func (s *PlanStatusService) Progress(message string) {
	s.AddStep(message)
}

func (s *PlanStatusService) update(apply func(models.PlanSessionStatus) models.PlanSessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = apply(s.status)
	for _, ch := range s.subscribers {
		select {
		case ch <- s.status:
		default: // drop for slow subscribers; a later snapshot supersedes this one
		}
	}
}
