package tests

import (
	"errors"
	"strings"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

// fakeLogHistory is the hand-written services.LogHistory used to prove what
// the status service persists and how persistence failures surface.
type fakeLogHistory struct {
	recorded  [][]models.LogLine
	recordErr error
	batch     models.LogBatch
	beforeSeq models.LogSequence
	limit     models.LogBatchSize
}

func (f *fakeLogHistory) Record(lines []models.LogLine) error {
	f.recorded = append(f.recorded, lines)
	return f.recordErr
}

func (f *fakeLogHistory) Before(before models.LogSequence, limit models.LogBatchSize) (models.LogBatch, error) {
	f.beforeSeq = before
	f.limit = limit
	return f.batch, nil
}

func newHistoryStatusService(history services.LogHistory) *services.PlanStatusService {
	return services.NewPlanStatusService(
		newSteppingClock(planStart()), models.GitContext{}, models.ToolIdentity{},
		services.NewCircularLogBuffer(2000)).WithLogHistory(history)
}

func TestAppendedOutputIsRecordedToHistoryWithSequences(t *testing.T) {
	history := &fakeLogHistory{}
	service := newHistoryStatusService(history)
	service.BeginLogEntry("planning")

	service.AppendLogOutput("one\ntwo\n")

	if len(history.recorded) != 1 {
		t.Fatalf("recorded %d batches, want 1", len(history.recorded))
	}
	lines := history.recorded[0]
	if len(lines) != 2 || lines[0].Sequence != 1 || lines[1].Sequence != 2 {
		t.Fatalf("lines = %+v, want sequences 1 and 2", lines)
	}
	if lines[0].Text != "one\n" || lines[0].Stream != models.LogStreamPlan || lines[0].Entry != 0 {
		t.Fatalf("line = %+v, want plan-stream text with entry 0", lines[0])
	}
}

func TestLogsBeforeDelegatesToHistory(t *testing.T) {
	history := &fakeLogHistory{batch: models.LogBatch{
		Lines: []models.LogLine{{Sequence: 4, Stream: models.LogStreamPlan, Text: "old\n"}},
		Next:  4, Latest: 30,
	}}
	service := newHistoryStatusService(history)

	batch, err := service.LogsBefore(5, 10)
	if err != nil {
		t.Fatalf("logs before: %v", err)
	}
	if history.beforeSeq != 5 || history.limit != 10 {
		t.Fatalf("delegated before/limit = %d/%d, want 5/10", history.beforeSeq, history.limit)
	}
	if len(batch.Lines) != 1 || batch.Latest != 30 {
		t.Fatalf("batch = %+v, want the history batch passed through", batch)
	}
}

func TestLogsBeforeFailsWithoutConfiguredHistory(t *testing.T) {
	service := services.NewPlanStatusService(
		newSteppingClock(planStart()), models.GitContext{}, models.ToolIdentity{},
		services.NewCircularLogBuffer(2000))

	if _, err := service.LogsBefore(5, 10); err == nil {
		t.Fatal("logs before without history succeeded, want unavailable error")
	}
}

func TestPersistenceFailureSurfacesOnBackwardsPaging(t *testing.T) {
	history := &fakeLogHistory{recordErr: errors.New("disk full")}
	service := newHistoryStatusService(history)
	service.BeginLogEntry("planning")

	service.AppendLogOutput("one\n")

	_, err := service.LogsBefore(5, 10)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the retained persistence failure", err)
	}
}
