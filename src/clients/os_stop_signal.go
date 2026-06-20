package clients

import "os"

// OsStopSignal checks the real filesystem for the completion sentinel file.
type OsStopSignal struct{}

// NewOsStopSignal constructs an OsStopSignal.
func NewOsStopSignal() OsStopSignal { return OsStopSignal{} }

// Exists reports whether the sentinel file is present.
func (OsStopSignal) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
