package tests

import (
	"errors"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

// fakeSessionRecordStore is a hand-rolled in-memory record store that records
// every Clear so tests can assert stale records are actually discarded.
type fakeSessionRecordStore struct {
	record  models.SessionRecord
	present bool
	loadErr error
	saved   []models.SessionRecord
	clears  int
}

func (f *fakeSessionRecordStore) Load() (models.SessionRecord, error) {
	if f.loadErr != nil {
		return models.SessionRecord{}, f.loadErr
	}
	if !f.present {
		return models.SessionRecord{}, errors.New("no record")
	}
	return f.record, nil
}

func (f *fakeSessionRecordStore) Save(r models.SessionRecord) error {
	f.saved = append(f.saved, r)
	f.record, f.present = r, true
	return nil
}

func (f *fakeSessionRecordStore) Clear() error {
	f.clears++
	f.present = false
	return nil
}

// fakeProcessProbe reports liveness for an explicit set of PIDs and records
// which PIDs were asked about.
type fakeProcessProbe struct {
	alive  map[int]bool
	asked  []int
	blowUp bool
}

func (f *fakeProcessProbe) Running(pid int) bool {
	if f.blowUp {
		panic("process probe must not be consulted")
	}
	f.asked = append(f.asked, pid)
	return f.alive[pid]
}

// fakeStatusPageProbe reports whether given URLs serve the status page and
// records every URL it was asked about.
type fakeStatusPageProbe struct {
	serving map[string]bool
	asked   []string
	blowUp  bool
}

func (f *fakeStatusPageProbe) Serving(url string) bool {
	if f.blowUp {
		panic("status page probe must not be consulted")
	}
	f.asked = append(f.asked, url)
	return f.serving[url]
}

// TestLocateConfirmsLiveSession proves a link is returned only after both the
// process and the page were actually probed, and that the record survives.
func TestLocateConfirmsLiveSession(t *testing.T) {
	records := &fakeSessionRecordStore{record: models.SessionRecord{PID: 4242, Port: 8931}, present: true}
	processes := &fakeProcessProbe{alive: map[int]bool{4242: true}}
	pages := &fakeStatusPageProbe{serving: map[string]bool{"http://localhost:8931/": true}}

	link, err := services.NewSessionLocator(records, processes, pages).Locate()

	if err != nil {
		t.Fatalf("Locate() error = %v, want nil", err)
	}
	if link.URL != "http://localhost:8931/" {
		t.Errorf("URL = %q, want %q", link.URL, "http://localhost:8931/")
	}
	if link.PID != 4242 || link.Port != 8931 {
		t.Errorf("link = %+v, want PID 4242 and Port 8931", link)
	}
	if len(processes.asked) != 1 || processes.asked[0] != 4242 {
		t.Errorf("process probe asked %v, want exactly [4242]", processes.asked)
	}
	if len(pages.asked) != 1 || pages.asked[0] != "http://localhost:8931/" {
		t.Errorf("page probe asked %v, want exactly [http://localhost:8931/]", pages.asked)
	}
	if records.clears != 0 {
		t.Errorf("Clear called %d times, want 0 for a confirmed session", records.clears)
	}
}

// TestLocateRejectsDeadProcess proves a record naming a dead process yields no
// link, is discarded, and never reaches the network probe.
func TestLocateRejectsDeadProcess(t *testing.T) {
	records := &fakeSessionRecordStore{record: models.SessionRecord{PID: 77, Port: 8931}, present: true}
	processes := &fakeProcessProbe{alive: map[int]bool{}}
	pages := &fakeStatusPageProbe{blowUp: true}

	link, err := services.NewSessionLocator(records, processes, pages).Locate()

	if !errors.Is(err, services.ErrNoSession) {
		t.Fatalf("Locate() error = %v, want ErrNoSession", err)
	}
	if link != (models.SessionLink{}) {
		t.Errorf("link = %+v, want zero value", link)
	}
	if records.clears != 1 {
		t.Errorf("Clear called %d times, want 1 to discard the stale record", records.clears)
	}
}

// TestLocateRejectsPortNotServingPage proves a live process whose port no
// longer answers as the status page — a recycled port, or a wedged server — is
// not reported as a link.
func TestLocateRejectsPortNotServingPage(t *testing.T) {
	records := &fakeSessionRecordStore{record: models.SessionRecord{PID: 4242, Port: 8931}, present: true}
	processes := &fakeProcessProbe{alive: map[int]bool{4242: true}}
	pages := &fakeStatusPageProbe{serving: map[string]bool{}}

	_, err := services.NewSessionLocator(records, processes, pages).Locate()

	if !errors.Is(err, services.ErrNoSession) {
		t.Fatalf("Locate() error = %v, want ErrNoSession", err)
	}
	if len(pages.asked) != 1 {
		t.Errorf("page probe asked %v, want exactly one probe", pages.asked)
	}
	if records.clears != 1 {
		t.Errorf("Clear called %d times, want 1", records.clears)
	}
}

// TestLocateWithoutRecordProbesNothing proves a missing record short-circuits
// before any probe runs and leaves no spurious Clear behind.
func TestLocateWithoutRecordProbesNothing(t *testing.T) {
	records := &fakeSessionRecordStore{present: false}
	processes := &fakeProcessProbe{blowUp: true}
	pages := &fakeStatusPageProbe{blowUp: true}

	_, err := services.NewSessionLocator(records, processes, pages).Locate()

	if !errors.Is(err, services.ErrNoSession) {
		t.Fatalf("Locate() error = %v, want ErrNoSession", err)
	}
	if records.clears != 0 {
		t.Errorf("Clear called %d times, want 0 when there was no record", records.clears)
	}
}

// TestLocateRejectsCorruptRecord proves a record with an unusable port is
// discarded without probing anything.
func TestLocateRejectsCorruptRecord(t *testing.T) {
	records := &fakeSessionRecordStore{record: models.SessionRecord{PID: 4242, Port: 0}, present: true}
	processes := &fakeProcessProbe{blowUp: true}
	pages := &fakeStatusPageProbe{blowUp: true}

	_, err := services.NewSessionLocator(records, processes, pages).Locate()

	if !errors.Is(err, services.ErrNoSession) {
		t.Fatalf("Locate() error = %v, want ErrNoSession", err)
	}
	if records.clears != 1 {
		t.Errorf("Clear called %d times, want 1", records.clears)
	}
}

// TestRememberAndForgetPersistTheSession proves the session lifecycle writes
// the exact record given and clears it on shutdown.
func TestRememberAndForgetPersistTheSession(t *testing.T) {
	records := &fakeSessionRecordStore{}
	locator := services.NewSessionLocator(records, &fakeProcessProbe{}, &fakeStatusPageProbe{})

	if err := locator.Remember(models.SessionRecord{PID: 991, Port: 4100}); err != nil {
		t.Fatalf("Remember() error = %v, want nil", err)
	}
	if len(records.saved) != 1 || records.saved[0] != (models.SessionRecord{PID: 991, Port: 4100}) {
		t.Fatalf("saved = %+v, want exactly [{991 4100}]", records.saved)
	}

	if err := locator.Forget(); err != nil {
		t.Fatalf("Forget() error = %v, want nil", err)
	}
	if records.clears != 1 {
		t.Errorf("Clear called %d times, want 1", records.clears)
	}
	if records.present {
		t.Error("record still present after Forget, want it gone")
	}
}
