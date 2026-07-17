package services

import "bytes"

// LogOutputSink receives streamed tool output for the status page's log tab.
// PlanStatusReporter satisfies it.
type LogOutputSink interface {
	AppendLogOutput(text string)
}

// logEntryWriter adapts an io.Writer stream onto a LogOutputSink, forwarding
// output one complete line at a time so each browser broadcast carries whole
// lines instead of arbitrary byte fragments.
type logEntryWriter struct {
	sink   LogOutputSink
	buffer bytes.Buffer
}

// newLogEntryWriter constructs a logEntryWriter over a sink.
func newLogEntryWriter(sink LogOutputSink) *logEntryWriter {
	return &logEntryWriter{sink: sink}
}

// Write buffers the bytes and forwards every complete line to the sink.
func (w *logEntryWriter) Write(p []byte) (int, error) {
	w.buffer.Write(p)
	w.forwardCompleteLines()
	return len(p), nil
}

// Flush forwards any trailing partial line. Call once the stream ends.
func (w *logEntryWriter) Flush() {
	if w.buffer.Len() == 0 {
		return
	}
	w.sink.AppendLogOutput(w.buffer.String())
	w.buffer.Reset()
}

func (w *logEntryWriter) forwardCompleteLines() {
	data := w.buffer.Bytes()
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return
	}
	w.sink.AppendLogOutput(string(data[:last+1]))
	w.buffer.Next(last + 1)
}
