package models

import "fmt"

// SessionRecord is what a running interactive session leaves behind so a later
// invocation can find its status page: the serving port and the process that
// owns it. It is a claim, not a guarantee — the port may have been recycled and
// the process may be long gone, so a reader must verify both before trusting it.
type SessionRecord struct {
	PID  int `json:"pid"`
	Port int `json:"port"`
}

// SessionLink is a status page URL that has been confirmed live: its process is
// running, its port is listening, and it answered with a status page.
type SessionLink struct {
	URL  string
	PID  int
	Port int
}

// URLFor returns the address browsers should open for this record. The server
// listens on all interfaces, so the host is localhost; remote users substitute
// the machine's external IP with the same port.
func (r SessionRecord) URLFor() string {
	return fmt.Sprintf("http://localhost:%d/", r.Port)
}

// Valid reports whether the record carries a usable process and port. A record
// that fails this was truncated or corrupted and must not be probed.
func (r SessionRecord) Valid() bool {
	return r.PID > 0 && r.Port > 0 && r.Port <= 65535
}
