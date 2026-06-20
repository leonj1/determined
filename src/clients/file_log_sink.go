package clients

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// clock is the slice of time-reading behaviour FileLogSink needs. Declaring it
// locally keeps clients free of any dependency on the services package.
type clock interface {
	Now() time.Time
}

// FileLogSink writes each iteration to its own timestamped file in a directory.
type FileLogSink struct {
	dir   string
	clock clock
}

// NewFileLogSink constructs a FileLogSink writing into dir.
func NewFileLogSink(dir string, clock clock) FileLogSink {
	return FileLogSink{dir: dir, clock: clock}
}

// OpenIteration creates <dir>/iter-NNNN-YYYYMMDD-HHMMSS.log for the iteration.
func (s FileLogSink) OpenIteration(iteration int) (io.WriteCloser, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	stamp := s.clock.Now().Format("20060102-150405")
	name := fmt.Sprintf("iter-%04d-%s.log", iteration, stamp)
	return os.Create(filepath.Join(s.dir, name))
}
