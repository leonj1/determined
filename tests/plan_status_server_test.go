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

// TestPlanStatusServerContract exercises the production server end to end:
// bind, serve the page, stream SSE snapshots, and shut down cleanly.
func TestPlanStatusServerContract(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{
		Git:   models.GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"},
		Goal:  "build a todo CLI",
		Phase: models.PlanPhaseRunning,
	})
	server := clients.NewPlanStatusServer(source)
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

func shutdown(t *testing.T, server *clients.PlanStatusServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
