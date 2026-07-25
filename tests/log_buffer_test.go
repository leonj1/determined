package tests

import (
	"testing"

	"determined/src/models"
	"determined/src/services"
)

// fakeLogBuffer is the hand-written test implementation injected into status
// services that need deterministic output storage without a real ring.
type fakeLogBuffer struct {
	lines       []models.LogLine
	subscribers []chan struct{}
}

func (f *fakeLogBuffer) Append(stream models.LogStream, entry models.LogEntryIndex, text string) models.LogSequence {
	sequence := models.LogSequence(len(f.lines) + 1)
	f.lines = append(f.lines, models.LogLine{
		Sequence: sequence, Stream: stream, Entry: entry, Text: text,
	})
	for _, subscriber := range f.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	return sequence
}

func (f *fakeLogBuffer) Since(since models.LogSequence, limit models.LogBatchSize) models.LogBatch {
	batch := models.LogBatch{
		Lines: []models.LogLine{}, Next: since, Latest: models.LogSequence(len(f.lines)),
	}
	for _, line := range f.lines {
		if line.Sequence <= since || len(batch.Lines) >= int(limit) {
			continue
		}
		batch.Lines = append(batch.Lines, line)
		batch.Next = line.Sequence
	}
	return batch
}

func (f *fakeLogBuffer) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.subscribers = append(f.subscribers, ch)
	return ch, func() {}
}

func newTestPlanStatusService(clock services.Clock, git models.GitContext, tool models.ToolIdentity) *services.PlanStatusService {
	return services.NewPlanStatusService(
		clock, git, tool, services.NewCircularLogBuffer(2000))
}

func TestCircularLogBufferReportsWrappedLinesAndExactGap(t *testing.T) {
	buffer := services.NewCircularLogBuffer(3)
	buffer.Append(models.LogStreamPlan, 0, "one\ntwo\n")
	buffer.Append(models.LogStreamExec, 0, "three\n")
	buffer.Append(models.LogStreamExec, 1, "four\n")

	atBoundary := buffer.Since(1, 10)
	if atBoundary.Gap != nil {
		t.Fatalf("client at oldest boundary received gap %+v", atBoundary.Gap)
	}
	assertLogSequences(t, atBoundary, []models.LogSequence{2, 3, 4})

	overrun := buffer.Since(0, 10)
	if overrun.Gap == nil || overrun.Gap.Dropped != 1 {
		t.Fatalf("overrun gap = %+v, want one dropped line", overrun.Gap)
	}
	if overrun.Gap.Stream != models.LogStreamPlan || overrun.Gap.Entry != 0 {
		t.Fatalf("gap location = %+v, want oldest available planning entry", overrun.Gap)
	}
	assertLogSequences(t, overrun, []models.LogSequence{2, 3, 4})
}

func TestCircularLogBufferAdvancesThroughBoundedBatches(t *testing.T) {
	buffer := services.NewCircularLogBuffer(5)
	buffer.Append(models.LogStreamPlan, 0, "one\ntwo\nthree\nfour\nfive\n")

	first := buffer.Since(0, 2)
	if first.Next != 2 || first.Latest != 5 {
		t.Fatalf("first cursor = %d/%d, want 2/5", first.Next, first.Latest)
	}
	assertLogSequences(t, first, []models.LogSequence{1, 2})

	second := buffer.Since(first.Next, 2)
	if second.Next != 4 || second.Latest != 5 {
		t.Fatalf("second cursor = %d/%d, want 4/5", second.Next, second.Latest)
	}
	assertLogSequences(t, second, []models.LogSequence{3, 4})
}

func TestCircularLogBufferCoalescesAdvanceSignalsWhileClientIsIdle(t *testing.T) {
	buffer := services.NewCircularLogBuffer(5)
	updates, cancel := buffer.Subscribe()
	defer cancel()

	buffer.Append(models.LogStreamPlan, 0, "one\n")
	buffer.Append(models.LogStreamPlan, 0, "two\n")

	select {
	case <-updates:
	default:
		t.Fatal("client received no new-data signal")
	}
	select {
	case <-updates:
		t.Fatal("idle client received more than one queued signal")
	default:
	}
}

func TestAppendingPlanAndExecOutputAvoidsSnapshotBroadcastAndRemainsFetchable(t *testing.T) {
	buffer := &fakeLogBuffer{}
	service := services.NewPlanStatusService(
		newSteppingClock(planStart()), models.GitContext{}, models.ToolIdentity{}, buffer)
	service.BeginLogEntry("planning project")
	snapshots, cancel := service.Subscribe()
	defer cancel()
	<-snapshots

	service.AppendLogOutput("streamed output\n")

	select {
	case snapshot := <-snapshots:
		t.Fatalf("output caused a full snapshot broadcast: %+v", snapshot)
	default:
	}
	service.BeginExecLogEntry("executing step 1")
	<-snapshots
	service.AppendExecLogOutput("execution output\n")
	select {
	case snapshot := <-snapshots:
		t.Fatalf("exec output caused a full snapshot broadcast: %+v", snapshot)
	default:
	}
	batch := service.LogsSince(0, 10)
	if len(batch.Lines) != 2 || batch.Lines[0].Text != "streamed output\n" ||
		batch.Lines[1].Text != "execution output\n" {
		t.Fatalf("fetched output = %+v, want both appended lines", batch.Lines)
	}
	if body := service.Snapshot().Log[0].Body; body != "" {
		t.Fatalf("snapshot retained body %q, want header only", body)
	}
}

func assertLogSequences(t *testing.T, batch models.LogBatch, want []models.LogSequence) {
	t.Helper()
	if len(batch.Lines) != len(want) {
		t.Fatalf("lines = %+v, want sequences %v", batch.Lines, want)
	}
	for i, sequence := range want {
		if batch.Lines[i].Sequence != sequence {
			t.Fatalf("line %d sequence = %d, want %d", i, batch.Lines[i].Sequence, sequence)
		}
	}
}
