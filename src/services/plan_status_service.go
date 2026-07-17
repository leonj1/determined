package services

import (
	"sync"

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
}

// NewPlanStatusService wires a PlanStatusService with the session's git
// context already resolved (see clients.GitContextReader).
func NewPlanStatusService(clock Clock, git models.GitContext) *PlanStatusService {
	return &PlanStatusService{
		clock: clock,
		status: models.PlanSessionStatus{
			Git:   git,
			Phase: models.PlanPhaseRunning,
			Steps: []models.PlanStep{},
		},
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
