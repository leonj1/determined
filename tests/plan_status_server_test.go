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

// fakeImplementSink counts execution requests the server accepts.
type fakeImplementSink struct {
	requests int
}

func (f *fakeImplementSink) RequestImplement() { f.requests++ }

// serverClock is a fixed fake clock for deterministic annotation stamps.
type serverClock struct{ t time.Time }

func (c serverClock) Now() time.Time { return c.t }

// TestPlanStatusServerContract exercises the production server end to end:
// bind, serve the page, stream SSE snapshots, and shut down cleanly.
func TestPlanStatusServerContract(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{
		Git:          models.GitContext{Remote: "git@github.com:leonj1/determined.git", Branch: "master"},
		Goal:         "build a todo CLI",
		Phase:        models.PlanPhaseRunning,
		Explanation:  "The implementation is complete.",
		ExplainPhase: models.ExplainPhaseSucceeded,
		Quiz: []models.QuizQuestion{{
			Question: "What changed?", Choices: []string{"A", "B", "C", "D"},
			CorrectIndex: 2, Rationale: "C describes the diff.",
		}},
		QuizPhase: models.QuizPhaseSucceeded,
	})
	sink := &fakeAnnotationSink{}
	server := clients.NewPlanStatusServer(source, sink, &fakeImplementSink{}, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	url := server.URL()
	if !strings.HasPrefix(url, "http://localhost:") {
		t.Fatalf("url = %q, want localhost address", url)
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
		"renderTests", "alignmentOf", "test-card",
		"align-aligned", "align-partial", "align-misaligned", "align-note",
		"renderSequenceDiagram", "seq-diagram",
		"annotationControls", "/annotate", "pendingAnnotations", "annotate-btn",
		`data-tab="exec"`, `id="implement"`, "/implement", "renderExec",
		"implementOffered", "execLog", "execPhase",
		`data-tab="explain"`, `id="explanation"`, "renderExplanation",
		"explainPhase", "renderDiff", "diff-add", "diff-del", "diff-hunk", "diff-meta",
		`data-tab="quiz"`, `id="quiz-state"`, `id="quiz-card"`, "renderQuiz",
		"quizPhase", "Question ", "Score: ", "Retake quiz",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing %q", marker)
		}
	}

	// --- theme contract: presence markers (byte-identical to the page source) ---
	for _, marker := range []string{
		`id="theme-toggle"`,
		`:root[data-theme="dark"]`,
		`:root:not([data-theme="light"])`,
		`localStorage.getItem("theme")`,
		`localStorage.setItem("theme", next)`,
		`localStorage.removeItem("theme")`,
		"renderThemeToggle",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing theme marker %q", marker)
		}
	}

	// --- theme contract: the two-state implementation must be gone ---
	for _, gone := range []string{"effectiveTheme", "darkQuery"} {
		if strings.Contains(page, gone) {
			t.Errorf("page still contains removed two-state code %q", gone)
		}
	}

	// --- anti-flash script must run before the body renders ---
	flashIdx := strings.Index(page, `localStorage.getItem("theme")`)
	bodyIdx := strings.Index(page, "<body>")
	if flashIdx == -1 || bodyIdx == -1 || flashIdx >= bodyIdx {
		t.Errorf("anti-flash script at byte %d, <body> at byte %d — want script before body", flashIdx, bodyIdx)
	}

	// --- storage access must be exception-safe in both scripts ---
	if n := strings.Count(page, "} catch (e) {}"); n < 2 {
		t.Errorf("} catch (e) {} count = %d, want at least 2 (anti-flash IIFE + toggle handler)", n)
	}

	// --- dark palette sync: every declaration must appear in exactly the
	// two dark blocks (:root[data-theme="dark"] and the media query) ---
	for _, decl := range []string{
		"--bg: #111111;", "--fg: #eeeeee;", "--card: #111111;",
		"--muted: #8f8f8f;", "--border: #eeeeee;", "--rule-light: #343434;",
		"--ok-bg: #111714;", "--ok-fg: #82b995;", "--ok-border: #4f755b;",
		"--bad-bg: #191211;", "--bad-fg: #d98d80;", "--bad-border: #87584f;",
		"color-scheme: dark;",
	} {
		if n := strings.Count(page, decl); n != 2 {
			t.Errorf("dark declaration %q count = %d, want exactly 2", decl, n)
		}
	}
	if n := strings.Count(page, "--accent: #e05d38;"); n != 3 {
		t.Errorf("shared accent declaration count = %d, want light plus two dark blocks", n)
	}

	// --- light editorial palette ---
	for _, decl := range []string{
		"color-scheme: light;",
		"--bg: #ffffff;", "--fg: #111111;", "--card: #ffffff;",
		"--muted: #9a9a9a;", "--border: #111111;", "--rule-light: #e5e5e5;", "--accent: #e05d38;",
		"--ok-bg: #fbfdfb;", "--ok-fg: #39734f;", "--ok-border: #8eb99c;",
		"--bad-bg: #fdfbfa;", "--bad-fg: #a04436;", "--bad-border: #d5a198;",
	} {
		if !strings.Contains(page, decl) {
			t.Errorf(":root light declaration missing or changed: %s", decl)
		}
	}

	// --- editorial visual contract ---
	for _, marker := range []string{
		"Georgia, 'Times New Roman', Times, serif",
		"border-bottom: 1px solid var(--rule-light)",
		"letter-spacing: 0.08em; text-transform: uppercase",
		"border-radius: 0",
		".doc a { color: var(--accent); }",
		`content: "\2192"`,
		`rx="0" class="seq-actor"`,
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing editorial style marker %q", marker)
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
	if !strings.Contains(first, `"explanation":"The implementation is complete."`) ||
		!strings.Contains(first, `"explainPhase":"succeeded"`) {
		t.Errorf("initial snapshot = %s, want the completed explanation", first)
	}
	if !strings.Contains(first, `"quiz":[{"question":"What changed?"`) ||
		!strings.Contains(first, `"quizPhase":"succeeded"`) {
		t.Errorf("initial snapshot = %s, want the completed quiz", first)
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
// the /annotate and /implement endpoints.
func startAnnotateServer(t *testing.T) (string, *fakeAnnotationSink, *fakeImplementSink, func()) {
	t.Helper()
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	sink := &fakeAnnotationSink{}
	implement := &fakeImplementSink{}
	server := clients.NewPlanStatusServer(source, sink, implement, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return server.URL(), sink, implement, func() { shutdown(t, server) }
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
	url, sink, _, stop := startAnnotateServer(t)
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
	url, sink, _, stop := startAnnotateServer(t)
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
	url, sink, _, stop := startAnnotateServer(t)
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

func TestPlanStatusServerAcceptsImplementRequest(t *testing.T) {
	url, _, implement, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Post(url+"implement", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("post implement: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if implement.requests != 1 {
		t.Errorf("implement requests = %d, want exactly 1", implement.requests)
	}
}

func TestPlanStatusServerRejectsImplementGet(t *testing.T) {
	url, _, implement, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(url + "implement")
	if err != nil {
		t.Fatalf("get implement: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if implement.requests != 0 {
		t.Errorf("implement requests = %d, want none", implement.requests)
	}
}

func TestPlanStatusServerServesTestsPath(t *testing.T) {
	url, _, _, stop := startAnnotateServer(t)
	defer stop()

	for _, path := range []string{"tests", "tests/journey", "tests/bdd", "exec", "explain"} {
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatalf("get /%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/%s status = %d, want 200", path, resp.StatusCode)
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
