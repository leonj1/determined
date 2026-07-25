package models

// LogStream identifies which status-page log receives an output line.
type LogStream string

const (
	// LogStreamPlan is output from interactive planning invocations.
	LogStreamPlan LogStream = "plan"
	// LogStreamExec is output from execute-loop invocations.
	LogStreamExec LogStream = "exec"
)

// LogSequence is a monotonic position in the combined status-page log stream.
type LogSequence uint64

// LogEntryIndex identifies the invocation header that owns an output line.
type LogEntryIndex int

// LogCapacity is the number of output lines retained in memory.
type LogCapacity int

// LogBatchSize bounds one browser fetch response.
type LogBatchSize int

// LogLine is one sequenced output line stored outside status snapshots.
type LogLine struct {
	Sequence LogSequence   `json:"seq"`
	Stream   LogStream     `json:"stream"`
	Entry    LogEntryIndex `json:"entry"`
	Text     string        `json:"text"`
}

// LogGap describes output overwritten before a client could fetch it.
type LogGap struct {
	Dropped LogSequence   `json:"dropped"`
	Stream  LogStream     `json:"stream"`
	Entry   LogEntryIndex `json:"entry"`
}

// LogBatch is one bounded response to a request for lines after Since.
type LogBatch struct {
	Lines  []LogLine   `json:"lines"`
	Next   LogSequence `json:"nextSeq"`
	Latest LogSequence `json:"latestSeq"`
	Gap    *LogGap     `json:"gap,omitempty"`
}
