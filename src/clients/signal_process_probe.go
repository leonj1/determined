package clients

import (
	"os"
	"syscall"
)

// SignalProcessProbe reports whether a process is alive by sending it signal 0,
// which performs the kernel's existence and permission checks without
// delivering anything to the target.
type SignalProcessProbe struct{}

// NewSignalProcessProbe constructs a SignalProcessProbe.
func NewSignalProcessProbe() SignalProcessProbe { return SignalProcessProbe{} }

// Running reports whether pid names a live process.
func (SignalProcessProbe) Running(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
