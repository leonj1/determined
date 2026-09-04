package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"determined/src/models"
)

// PlanStatusService is the state hub for an interactive planning session. The
// orchestrator pushes updates in; the status server subscribes for small state
// snapshots while log bodies flow through the injected buffer. State mutations
// notify snapshot subscribers; output appends only signal buffer subscribers.
// New subscribers receive current headers/state and can pull retained output.
type PlanStatusService struct {
	clock   Clock
	logs    LogBuffer
	history LogHistory

	mu          sync.Mutex
	historyErr  error
	status      models.PlanSessionStatus
	subscribers []chan models.PlanSessionStatus
	annotations chan struct{}
	implement   chan struct{}

	// taskCancel kills the tool invocation currently running, and taskAction
	// records the page's verdict on it (skip or stop) for the orchestrator to
	// collect once the invocation settles. Both are nil/none between
	// invocations, when the page's Skip and Stop have nothing to act on.
	taskCancel context.CancelFunc
	taskAction models.TaskAction

	// explain carries the page's request to generate the explanation and quiz
	// after execution finishes (succeeded or failed). The status page submits
	// the request, the feedback loop collects it, and the channel buffers one
	// signal so a racing submit after collection is harmless.
	explain chan struct{}

	// stallChoice carries the page's tiebreak verdict to an execute run parked
	// in AwaitStallChoice. It is non-nil only while a run is blocked on the
	// modal, so SubmitStallChoice can tell whether a wait is pending.
	stallChoice chan models.StallGuidance

	nextPromptID   models.PromptID
	promptResponse chan models.PromptResponse
}

// NewPlanStatusService wires a PlanStatusService with the session's git
// context already resolved (see clients.GitContextReader) and the identity of
// the AI tool driving the session.
func NewPlanStatusService(clock Clock, git models.GitContext, tool models.ToolIdentity, logs LogBuffer) *PlanStatusService {
	return &PlanStatusService{
		clock: clock,
		logs:  logs,
		status: models.PlanSessionStatus{
			Git:   git,
			Tool:  tool,
			Phase: models.PlanPhaseRunning,
			Steps: []models.PlanStep{},
		},
		annotations: make(chan struct{}, 1),
		implement:   make(chan struct{}, 1),
		explain:     make(chan struct{}, 1),
	}
}

// WithLogHistory enables full-run persistence of streamed output, powering the
// page's backwards paging. Without it LogsBefore reports history unavailable.
func (s *PlanStatusService) WithLogHistory(history LogHistory) *PlanStatusService {
	s.history = history
	return s
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

// SetAssumptions publishes the plan's `## Assumptions` section body.
func (s *PlanStatusService) SetAssumptions(assumptions string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Assumptions = assumptions
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

// SetMilestone publishes the outer milestone gate currently in progress.
func (s *PlanStatusService) SetMilestone(progress models.MilestoneProgress) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.Milestone = &progress
		return st
	})
}

// AddStep appends a timestamped workflow step.
func (s *PlanStatusService) AddStep(message string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
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

// AppendLogOutput stores tool output without broadcasting a status snapshot.
func (s *PlanStatusService) AppendLogOutput(text string) {
	entry, ok := s.lastLogEntry(models.LogStreamPlan)
	if ok {
		s.appendOutput(models.LogStreamPlan, entry, text)
	}
}

// Ask implements Prompter for interactive sessions by publishing a typed
// prompt and waiting for the status page to submit its answer.
func (s *PlanStatusService) Ask(ctx context.Context, prompt models.UserPrompt) (string, error) {
	responses := make(chan models.PromptResponse, 1)
	prompt = s.openPrompt(prompt, responses)
	defer s.closePrompt(prompt.ID)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case response := <-responses:
		return strings.TrimSpace(response.Answer), nil
	}
}

func (s *PlanStatusService) openPrompt(prompt models.UserPrompt, responses chan models.PromptResponse) models.UserPrompt {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		s.nextPromptID++
		prompt.ID = s.nextPromptID
		s.promptResponse = responses
		st.PendingPrompt = &prompt
		return st
	})
	return prompt
}

func (s *PlanStatusService) closePrompt(id models.PromptID) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if st.PendingPrompt == nil || st.PendingPrompt.ID != id {
			return st
		}
		s.promptResponse = nil
		st.PendingPrompt = nil
		return st
	})
}

// SubmitPromptResponse delivers only the first valid response for the active
// prompt. Typed results let the HTTP adapter map failures without string tests.
func (s *PlanStatusService) SubmitPromptResponse(response models.PromptResponse) models.PromptSubmissionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	prompt := s.status.PendingPrompt
	if prompt == nil || s.promptResponse == nil {
		return models.PromptSubmissionMissing
	}
	if response.ID != prompt.ID {
		return models.PromptSubmissionStale
	}
	response.Answer = strings.TrimSpace(response.Answer)
	if !prompt.Accepts(response.Answer) {
		return models.PromptSubmissionInvalid
	}
	responses := s.promptResponse
	s.promptResponse = nil
	responses <- response
	return models.PromptSubmissionAccepted
}

// Finish records the end of the planning phase as succeeded or failed.
func (s *PlanStatusService) Finish(succeeded bool) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.EndedAt = s.clock.Now()
		st.PendingPrompt = nil
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

// BeginTask registers the cancel function that kills the tool invocation now
// running, clearing any stale verdict, and advertises the page's Skip and Stop
// controls on the active activity entry.
func (s *PlanStatusService) BeginTask(cancel context.CancelFunc) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		s.taskCancel = cancel
		s.taskAction = models.TaskActionNone
		st.TaskControlAvailable = true
		return st
	})
}

// EndTask deregisters the finished invocation, hiding the page's Skip and Stop
// controls; requests arriving after this are rejected rather than recorded.
func (s *PlanStatusService) EndTask() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		s.taskCancel = nil
		st.TaskControlAvailable = false
		return st
	})
}

// TakeTaskAction returns the page's verdict on the invocation that just
// settled and clears it, so one click never bleeds into a later invocation.
func (s *PlanStatusService) TakeTaskAction() models.TaskAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	action := s.taskAction
	s.taskAction = models.TaskActionNone
	return action
}

// RequestSkipActiveTask asks the run to abort the active task and move on. It
// reports whether an active task existed to act on.
func (s *PlanStatusService) RequestSkipActiveTask() bool {
	return s.requestTaskAction(models.TaskActionSkip)
}

// RequestStopRun asks the run to abort the active task and end the whole run.
// It reports whether an active task existed to act on.
func (s *PlanStatusService) RequestStopRun() bool {
	return s.requestTaskAction(models.TaskActionStop)
}

// requestTaskAction records the verdict and kills the active invocation. A
// stop already recorded is never downgraded by a racing skip click, since
// ending the run is the stronger promise made to the user.
func (s *PlanStatusService) requestTaskAction(action models.TaskAction) bool {
	s.mu.Lock()
	cancel := s.taskCancel
	if cancel == nil {
		s.mu.Unlock()
		return false
	}
	if s.taskAction != models.TaskActionStop {
		s.taskAction = action
	}
	s.mu.Unlock()
	cancel()
	return true
}

// AwaitStallChoice parks the execute run on a verification deadlock: it flags
// the page's modal open, publishes the stalled step's title, then blocks until
// the user submits a verdict or ctx cancels. A cancel returns
// StallDecisionCancel so the caller falls back to today's stop behavior. The
// modal flag is always cleared before returning.
func (s *PlanStatusService) AwaitStallChoice(ctx context.Context, prompt models.StallPrompt) models.StallGuidance {
	ch := make(chan models.StallGuidance, 1)
	s.openStallChoice(ch, prompt)
	defer s.closeStallChoice()
	select {
	case <-ctx.Done():
		return models.StallGuidance{Decision: models.StallDecisionCancel}
	case guidance := <-ch:
		return guidance
	}
}

func (s *PlanStatusService) openStallChoice(ch chan models.StallGuidance, prompt models.StallPrompt) {
	s.mu.Lock()
	s.stallChoice = ch
	s.mu.Unlock()
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.AwaitingStallChoice = true
		st.StallChoicePrompt = prompt.StepTitle
		st.StallChoiceOptions = prompt.Options
		return st
	})
}

func (s *PlanStatusService) closeStallChoice() {
	s.mu.Lock()
	s.stallChoice = nil
	s.mu.Unlock()
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.AwaitingStallChoice = false
		st.StallChoicePrompt = ""
		st.StallChoiceOptions = nil
		return st
	})
}

// SubmitStallChoice delivers the page's verdict to a run parked in
// AwaitStallChoice, reporting whether a wait was pending to receive it.
func (s *PlanStatusService) SubmitStallChoice(decision models.StallDecision, comment string) bool {
	s.mu.Lock()
	ch := s.stallChoice
	if ch == nil {
		s.mu.Unlock()
		return false
	}
	s.stallChoice = nil
	s.mu.Unlock()
	ch <- models.StallGuidance{Decision: decision, Comment: comment}
	return true
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

// RequestExplain queues one explanation-generation request from the page.
// Requests are ignored unless execution has finished (succeeded or failed)
// and the explanation has not already been generated, so repeated clicks
// start at most one generation while a running or completed explanation
// blocks further requests.
func (s *PlanStatusService) RequestExplain() {
	requested := false
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		if !explainRequestable(st) {
			return st
		}
		st.ExplainPhase = models.ExplainPhaseRunning
		requested = true
		return st
	})
	if !requested {
		return
	}
	select {
	case s.explain <- struct{}{}:
	default: // already signalled
	}
}

// explainRequestable reports whether the page may request explanation
// generation: execution must be done and the explanation must not have
// been started yet.
func explainRequestable(st models.PlanSessionStatus) bool {
	execDone := st.ExecPhase == models.ExecPhaseSucceeded || st.ExecPhase == models.ExecPhaseFailed
	explainIdle := st.ExplainPhase == "" || st.ExplainPhase == models.ExplainPhaseFailed
	return execDone && explainIdle
}

// ExplainSignal reports explanation requests: the channel receives one value
// per accepted request.
func (s *PlanStatusService) ExplainSignal() <-chan struct{} {
	return s.explain
}

// StartExecution records a fresh timing window for the execute loop while
// retaining the cumulative execution log from prior attempts.
func (s *PlanStatusService) StartExecution() {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ExecPhase = models.ExecPhaseRunning
		st.ExecStartedAt = s.clock.Now()
		st.ExecEndedAt = time.Time{}
		st.ExecStopReason = ""
		st.ExecAdvice = ""
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

// SetExecStopReason publishes why a failed execute run ended and what the user
// should do about it, which the status page renders as a prominent alert.
func (s *PlanStatusService) SetExecStopReason(reason, advice string) {
	s.update(func(st models.PlanSessionStatus) models.PlanSessionStatus {
		st.ExecStopReason = reason
		st.ExecAdvice = advice
		return st
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

// AppendExecLogOutput stores tool output without broadcasting a status snapshot.
func (s *PlanStatusService) AppendExecLogOutput(text string) {
	entry, ok := s.lastLogEntry(models.LogStreamExec)
	if ok {
		s.appendOutput(models.LogStreamExec, entry, text)
	}
}

// appendOutput stores lines in the live ring and, when history is configured,
// persists them for backwards paging. A persistence failure is retained and
// surfaced on the next LogsBefore call rather than silently dropping history.
func (s *PlanStatusService) appendOutput(stream models.LogStream, entry models.LogEntryIndex, text string) {
	lines := s.logs.Append(stream, entry, text)
	if s.history == nil || len(lines) == 0 {
		return
	}
	if err := s.history.Record(lines); err != nil {
		s.mu.Lock()
		if s.historyErr == nil {
			s.historyErr = err
		}
		s.mu.Unlock()
	}
}

func (s *PlanStatusService) lastLogEntry(stream models.LogStream) (models.LogEntryIndex, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	length := len(s.status.Log)
	if stream == models.LogStreamExec {
		length = len(s.status.ExecLog)
	}
	if length == 0 {
		return 0, false
	}
	return models.LogEntryIndex(length - 1), true
}

// LogsSince returns a bounded batch of output lines after a sequence.
func (s *PlanStatusService) LogsSince(since models.LogSequence, limit models.LogBatchSize) models.LogBatch {
	return s.logs.Since(since, limit)
}

// LogsBefore returns a bounded batch of persisted output lines ending just
// before a sequence, for the page's backwards paging. It fails when no history
// is configured or when persistence has previously failed.
func (s *PlanStatusService) LogsBefore(before models.LogSequence, limit models.LogBatchSize) (models.LogBatch, error) {
	if s.history == nil {
		return models.LogBatch{}, errors.New("log history unavailable")
	}
	s.mu.Lock()
	failed := s.historyErr
	s.mu.Unlock()
	if failed != nil {
		return models.LogBatch{}, fmt.Errorf("log history persistence failed: %w", failed)
	}
	return s.history.Before(before, limit)
}

// SubscribeLogOutput reports when output advances without carrying its payload.
func (s *PlanStatusService) SubscribeLogOutput() (<-chan struct{}, func()) {
	return s.logs.Subscribe()
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
