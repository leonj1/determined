package services

import (
	"fmt"
	"io"
)

// progressMessage is a brief description of the active workflow stage.
type progressMessage string

// ProgressSink receives workflow progress messages in addition to the
// terminal. The interactive status page is the real implementation
// (PlanStatusService); a nil sink is silently skipped.
type ProgressSink interface {
	Progress(message string)
}

// writeProgress makes workflow transitions visible with a wall-clock timestamp.
func writeProgress(out io.Writer, clock Clock, message progressMessage) {
	stamp := clock.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(out, "\n==> [%s] %s\n", stamp, message)
}

// notifyProgress fans a progress message out to an optional sink.
func notifyProgress(sink ProgressSink, message progressMessage) {
	if sink == nil {
		return
	}
	sink.Progress(string(message))
}
