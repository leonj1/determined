package services

import (
	"errors"

	"determined/src/models"
)

// ErrNoSession means no live status page could be confirmed: either no session
// record exists, or the recorded session failed one of the liveness checks.
var ErrNoSession = errors.New("no running interactive session found")

// SessionRecordStore persists and retrieves the record a running interactive
// session leaves behind. The real implementation is clients.FileSessionRecordStore.
type SessionRecordStore interface {
	Load() (models.SessionRecord, error)
	Save(models.SessionRecord) error
	Clear() error
}

// ProcessProbe reports whether a process ID belongs to a process that is still
// running. The real implementation is clients.SignalProcessProbe.
type ProcessProbe interface {
	Running(pid int) bool
}

// StatusPageProbe reports whether a URL is currently answering with the
// interactive status page. The real implementation is clients.HttpStatusPageProbe.
type StatusPageProbe interface {
	Serving(url string) bool
}

// SessionLocator answers "where is the running status page?" without trusting
// the session record on its own. A stale record naming a dead process, or a
// recycled port now owned by some unrelated server, must not be reported as a
// live link — so the record is only a starting point for three checks.
type SessionLocator struct {
	records   SessionRecordStore
	processes ProcessProbe
	pages     StatusPageProbe
}

// NewSessionLocator constructs a SessionLocator over a record store, a process
// probe, and a status page probe.
func NewSessionLocator(records SessionRecordStore, processes ProcessProbe, pages StatusPageProbe) SessionLocator {
	return SessionLocator{records: records, processes: processes, pages: pages}
}

// Locate returns the confirmed status page link, or ErrNoSession when no live
// session can be proven. It clears a record that fails verification so the
// stale claim cannot mislead a later call.
func (l SessionLocator) Locate() (models.SessionLink, error) {
	record, err := l.records.Load()
	if err != nil {
		return models.SessionLink{}, ErrNoSession
	}
	if !record.Valid() {
		return l.discard()
	}
	if !l.processes.Running(record.PID) {
		return l.discard()
	}
	if !l.pages.Serving(record.URLFor()) {
		return l.discard()
	}
	return models.SessionLink{URL: record.URLFor(), PID: record.PID, Port: record.Port}, nil
}

// Remember records a newly started session so a later -link call can find it.
func (l SessionLocator) Remember(record models.SessionRecord) error {
	return l.records.Save(record)
}

// Forget drops the record for a session that is shutting down.
func (l SessionLocator) Forget() error {
	return l.records.Clear()
}

// discard removes a record that failed verification and reports no session.
func (l SessionLocator) discard() (models.SessionLink, error) {
	l.records.Clear() //nolint:errcheck // the record is already known unusable
	return models.SessionLink{}, ErrNoSession
}
