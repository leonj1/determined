package tests

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
)

// fakePlanStatusSource is a hand-rolled status source for driving the server.
type fakePlanStatusSource struct {
	snapshot models.PlanSessionStatus
	updates  chan models.PlanSessionStatus
}

func newFakePlanStatusSource(snapshot models.PlanSessionStatus) *fakePlanStatusSource {
	return &fakePlanStatusSource{
		snapshot: snapshot,
		updates:  make(chan models.PlanSessionStatus, 16),
	}
}

func (f *fakePlanStatusSource) Snapshot() models.PlanSessionStatus { return f.snapshot }

func (f *fakePlanStatusSource) Subscribe() (<-chan models.PlanSessionStatus, func()) {
	f.updates <- f.snapshot
	return f.updates, func() {}
}

func (f *fakePlanStatusSource) publish(snapshot models.PlanSessionStatus) {
	f.snapshot = snapshot
	f.updates <- snapshot
}

// fakeAnnotationSink records annotations the server accepts, in order.
type fakeAnnotationSink struct {
	annotations []models.Annotation
}

func (f *fakeAnnotationSink) SubmitAnnotation(a models.Annotation) {
	f.annotations = append(f.annotations, a)
}

// serverClock is a fixed fake clock for deterministic annotation stamps.
type serverClock struct{ t time.Time }

func (c serverClock) Now() time.Time { return c.t }

// TestPlanStatusServerContract exercises the production server end to end:
// bind, serve the page, stream SSE snapshots, and shut down cleanly.
func TestPlanStatusServerContract(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{
		Git:   models.GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"},
		Goal:  "build a todo CLI",
		Phase: models.PlanPhaseRunning,
	})
	sink := &fakeAnnotationSink{}
	server := clients.NewPlanStatusServer(source, sink, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	url := server.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("url = %q, want loopback address", url)
	}

	assertPageServed(t, url)
	assertEventStream(t, url, source)
}

func assertPageServed(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("fetch page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	page := string(body)
	for _, marker := range []string{
		"determined — planning", "EventSource", "banner",
		"step-card", "taskSteps", "Done when: ",
		"log-entry", "renderLog", `data-tab="log"`,
		"renderTable", "unflattenTables", "isSeparatorRow",
		`data-tab="tests"`, `data-tests-tab="journey"`, `data-tests-tab="bdd"`,
		"splitTests", "status.tests",
		"renderSequenceDiagram", "seq-diagram",
		"annotationControls", "/annotate", "pendingAnnotations", "annotate-btn",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing %q", marker)
		}
	}
}

func assertEventStream(t *testing.T, url string, source *fakePlanStatusSource) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"events", nil)
	if err != nil {
		t.Fatalf("build events request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("events content type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(resp.Body)
	first := readEventData(t, reader)
	if !strings.Contains(first, `"goal":"build a todo CLI"`) {
		t.Errorf("initial snapshot = %s, want the current goal", first)
	}

	updated := source.snapshot
	updated.Plan = "# Plan"
	source.publish(updated)

	second := readEventData(t, reader)
	if !strings.Contains(second, `"plan":"# Plan"`) {
		t.Errorf("update snapshot = %s, want the published plan", second)
	}
}

func readEventData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
}

// startAnnotateServer boots the production server over fakes for exercising
// the /annotate endpoint.
func startAnnotateServer(t *testing.T) (string, *fakeAnnotationSink, func()) {
	t.Helper()
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	sink := &fakeAnnotationSink{}
	server := clients.NewPlanStatusServer(source, sink, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return server.URL(), sink, func() { shutdown(t, server) }
}

func postAnnotation(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"annotate", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post annotation: %v", err)
	}
	resp.Body.Close()
	return resp
}

func TestPlanStatusServerAcceptsValidAnnotation(t *testing.T) {
	url, sink, stop := startAnnotateServer(t)
	defer stop()

	resp := postAnnotation(t, url, `{"section":"steps","target":"Step 2: add the store","comment":"split this step"}`)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if len(sink.annotations) != 1 {
		t.Fatalf("sink annotations = %+v, want exactly 1", sink.annotations)
	}
	got := sink.annotations[0]
	want := models.Annotation{
		At:      time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		Section: models.AnnotationSectionSteps,
		Target:  "Step 2: add the store",
		Comment: "split this step",
	}
	if got != want {
		t.Errorf("annotation = %+v, want %+v", got, want)
	}
}

func TestPlanStatusServerRejectsBadAnnotations(t *testing.T) {
	url, sink, stop := startAnnotateServer(t)
	defer stop()

	for name, body := range map[string]string{
		"invalid JSON":    `{not json`,
		"unknown section": `{"section":"banner","comment":"hello"}`,
		"blank comment":   `{"section":"plan","comment":"   "}`,
	} {
		if resp := postAnnotation(t, url, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if len(sink.annotations) != 0 {
		t.Errorf("sink received %+v, want nothing", sink.annotations)
	}
}

func TestPlanStatusServerRejectsAnnotationGet(t *testing.T) {
	url, sink, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(url + "annotate")
	if err != nil {
		t.Fatalf("get annotate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if len(sink.annotations) != 0 {
		t.Errorf("sink received %+v, want nothing", sink.annotations)
	}
}

func TestPlanStatusServerServesTestsPath(t *testing.T) {
	url, _, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(url + "tests")
	if err != nil {
		t.Fatalf("get /tests: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/tests status = %d, want 200", resp.StatusCode)
	}
}

func shutdown(t *testing.T, server *clients.PlanStatusServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
