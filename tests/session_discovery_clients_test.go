package tests

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
)

// TestFileSessionRecordStoreRoundTrip exercises the real filesystem boundary:
// a saved record reads back identically, and Clear removes it.
func TestFileSessionRecordStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "session.json")
	store := clients.NewFileSessionRecordStore(path)

	if _, err := store.Load(); err == nil {
		t.Fatal("Load() before Save returned nil error, want an error")
	}
	if err := store.Save(models.SessionRecord{PID: 321, Port: 5150}); err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if loaded != (models.SessionRecord{PID: 321, Port: 5150}) {
		t.Errorf("loaded = %+v, want {321 5150}", loaded)
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("record file still exists after Clear, want it removed")
	}
	if err := store.Clear(); err != nil {
		t.Errorf("Clear() on a missing file error = %v, want nil", err)
	}
}

// TestSignalProcessProbeDetectsLiveAndDeadProcesses proves the probe confirms
// this very process and rejects an unusable PID.
func TestSignalProcessProbeDetectsLiveAndDeadProcesses(t *testing.T) {
	probe := clients.NewSignalProcessProbe()

	if !probe.Running(os.Getpid()) {
		t.Error("Running(own pid) = false, want true")
	}
	if probe.Running(0) {
		t.Error("Running(0) = true, want false")
	}
	if probe.Running(-1) {
		t.Error("Running(-1) = true, want false")
	}
}

// TestHttpStatusPageProbeRequiresTheDeterminedPage proves the probe accepts the
// real status server but rejects an unrelated server on a recycled port, a
// non-200 response, and a closed port.
func TestHttpStatusPageProbeRequiresTheDeterminedPage(t *testing.T) {
	probe := clients.NewHttpStatusPageProbe(2 * time.Second)

	source := newFakePlanStatusSource(models.PlanSessionStatus{Goal: "build a todo CLI"})
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{})
	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	defer server.Shutdown(t.Context()) //nolint:errcheck // test cleanup

	if !probe.Serving(server.URL()) {
		t.Errorf("Serving(%q) = false, want true for the real status server", server.URL())
	}

	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>some other app</html>")) //nolint:errcheck
	}))
	defer stranger.Close()
	if probe.Serving(stranger.URL) {
		t.Error("Serving(unrelated server) = true, want false — a recycled port must not pass")
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(clients.StatusPageHeader, "1")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if probe.Serving(broken.URL) {
		t.Error("Serving(500 response) = true, want false")
	}

	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	if probe.Serving(closedURL) {
		t.Error("Serving(closed port) = true, want false")
	}
}
