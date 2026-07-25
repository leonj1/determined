package services

import (
	"strings"
	"sync"

	"determined/src/models"
)

// LogBuffer stores streamed status-page output independently of snapshots.
type LogBuffer interface {
	Append(models.LogStream, models.LogEntryIndex, string) []models.LogLine
	Since(models.LogSequence, models.LogBatchSize) models.LogBatch
	Subscribe() (<-chan struct{}, func())
}

// CircularLogBuffer retains the newest fixed number of output lines.
type CircularLogBuffer struct {
	mu          sync.Mutex
	lines       []models.LogLine
	capacity    models.LogCapacity
	latest      models.LogSequence
	subscribers []chan struct{}
}

// NewCircularLogBuffer constructs a fixed-capacity output buffer.
func NewCircularLogBuffer(capacity models.LogCapacity) *CircularLogBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &CircularLogBuffer{
		lines:    make([]models.LogLine, int(capacity)),
		capacity: capacity,
	}
}

// Append stores complete lines, notifies subscribers once when data advances,
// and returns the appended lines with their assigned sequences.
func (b *CircularLogBuffer) Append(stream models.LogStream, entry models.LogEntryIndex, text string) []models.LogLine {
	parts := splitLogOutput(text)
	if len(parts) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	appended := make([]models.LogLine, 0, len(parts))
	for _, part := range parts {
		b.latest++
		index := int((b.latest - 1) % models.LogSequence(b.capacity))
		b.lines[index] = models.LogLine{
			Sequence: b.latest, Stream: stream, Entry: entry, Text: part,
		}
		appended = append(appended, b.lines[index])
	}
	b.notify()
	return appended
}

// Latest returns the newest assigned sequence.
func (b *CircularLogBuffer) Latest() models.LogSequence {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.latest
}

// Since returns at most limit lines after the supplied sequence.
func (b *CircularLogBuffer) Since(since models.LogSequence, limit models.LogBatchSize) models.LogBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit < 1 {
		limit = 1
	}
	batch := models.LogBatch{Lines: []models.LogLine{}, Next: since, Latest: b.latest}
	if b.latest == 0 || since >= b.latest {
		batch.Next = b.latest
		return batch
	}
	first, gap := b.firstAvailable(since)
	batch.Gap = gap
	for sequence := first; sequence <= b.latest && len(batch.Lines) < int(limit); sequence++ {
		batch.Lines = append(batch.Lines, b.line(sequence))
		batch.Next = sequence
	}
	return batch
}

func (b *CircularLogBuffer) firstAvailable(since models.LogSequence) (models.LogSequence, *models.LogGap) {
	oldest := b.oldest()
	first := since + 1
	if first >= oldest {
		return first, nil
	}
	line := b.line(oldest)
	return oldest, &models.LogGap{
		Dropped: oldest - first, Stream: line.Stream, Entry: line.Entry,
	}
}

func (b *CircularLogBuffer) oldest() models.LogSequence {
	if b.latest < models.LogSequence(b.capacity) {
		return 1
	}
	return b.latest - models.LogSequence(b.capacity) + 1
}

func (b *CircularLogBuffer) line(sequence models.LogSequence) models.LogLine {
	index := int((sequence - 1) % models.LogSequence(b.capacity))
	return b.lines[index]
}

// Subscribe registers a coalescing notification channel for new output.
func (b *CircularLogBuffer) Subscribe() (<-chan struct{}, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan struct{}, 1)
	b.subscribers = append(b.subscribers, ch)
	return ch, func() { b.unsubscribe(ch) }
}

func (b *CircularLogBuffer) unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, existing := range b.subscribers {
		if existing == ch {
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}

func (b *CircularLogBuffer) notify() {
	for _, ch := range b.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func splitLogOutput(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	if parts[len(parts)-1] == "" {
		return parts[:len(parts)-1]
	}
	return parts
}
