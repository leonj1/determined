package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	snapshot     models.PlanSessionStatus
	updates      chan models.PlanSessionStatus
	logBatch     models.LogBatch
	beforeBatch  models.LogBatch
	beforeErr    error
	logUpdates   chan struct{}
	requestedSeq models.LogSequence
	requestLimit models.LogBatchSize
	beforeSeq    models.LogSequence
	beforeLimit  models.LogBatchSize
	beforeCalls  int
}

func newFakePlanStatusSource(snapshot models.PlanSessionStatus) *fakePlanStatusSource {
	return &fakePlanStatusSource{
		snapshot:   snapshot,
		updates:    make(chan models.PlanSessionStatus, 16),
		logUpdates: make(chan struct{}, 1),
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

func (f *fakePlanStatusSource) LogsSince(since models.LogSequence, limit models.LogBatchSize) models.LogBatch {
	f.requestedSeq = since
	f.requestLimit = limit
	return f.logBatch
}

func (f *fakePlanStatusSource) LogsBefore(before models.LogSequence, limit models.LogBatchSize) (models.LogBatch, error) {
	f.beforeSeq = before
	f.beforeLimit = limit
	f.beforeCalls++
	if f.beforeErr != nil {
		return models.LogBatch{}, f.beforeErr
	}
	return f.beforeBatch, nil
}

func (f *fakePlanStatusSource) SubscribeLogOutput() (<-chan struct{}, func()) {
	return f.logUpdates, func() {}
}

func (f *fakePlanStatusSource) publishLogUpdate() {
	f.logUpdates <- struct{}{}
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

type assetExpectation struct {
	path        string
	contentType string
	marker      string
}

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
			CorrectIndex: 2, Rationale: "C describes the diff.", SourceSection: "Status reporting",
		}},
		QuizPhase: models.QuizPhaseSucceeded,
		Log:       []models.LogEntry{{Message: "planning", Body: "large streamed body"}},
	})
	sink := &fakeAnnotationSink{}
	server := clients.NewPlanStatusServer(source, sink, &fakeImplementSink{}, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	url := server.URL()
	if !strings.HasPrefix(url, "http://localhost:") {
		t.Fatalf("url = %q, want localhost address", url)
	}

	assertPageServed(t, url)
	assertDiffAssetsServed(t, url)
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
		`id="active-task"`, `id="active-task-message"`, `id="active-task-elapsed"`,
		"const active = activeActivity(activitySteps, activityIsRunning(status));",
		"renderActiveTask(active)", "renderActivity(activitySteps, active, !!status.taskControlAvailable, repoFull)",
		"step-card", "taskSteps", "Done when: ",
		"log-entry", "renderLog", `data-tab="log"`, `fetch("/logs?since="`,
		`addEventListener("logs"`, "lines skipped",
		"unflattenTables",
		`data-tab="tests"`, `data-tests-tab="journey"`, `data-tests-tab="bdd"`,
		`id="plan-demo"`, `id="demo-frame"`, `sandbox="allow-scripts"`, "renderDemo", "status.demo",
		"splitTests", "status.tests",
		"renderTests", "alignmentOf", "test-card",
		"align-aligned", "align-partial", "align-misaligned", "align-note",
		"renderSequenceDiagram", "seq-diagram",
		"annotationControls", "/annotate", "pendingAnnotations", "annotate-btn",
		`data-tab="exec"`, `id="implement"`, "/implement", "renderExec",
		"implementOffered", "execLog", "execPhase",
		`data-tab="explain"`, `id="explanation"`, "renderExplanation",
		"explainPhase", "renderDiff", "Diff2Html.html", "matching: \"words\"",
		`href="/assets/diff2html.min.css"`, `src="/assets/diff2html.min.js"`,
		`src="/assets/marked.min.js"`,
		`data-tab="quiz"`, `id="quiz-state"`, `id="quiz-card"`, "renderQuiz",
		"quizPhase", "Question ", "Score: ", "Retake quiz", "shuffleQuizQuestions",
		"slugify", "sourceSection", "quiz-source-link", "followQuizSource", `headingIds: true`,
		"pendingExplanationHash", "restoreExplanationHash",
		"syncStickyOffsets", "renderTabStates", "renderDocumentTitle",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing %q", marker)
		}
	}

	for _, removed := range []string{"diff-add", "diff-del", "diff-hunk", "diff-meta"} {
		if strings.Contains(page, removed) {
			t.Errorf("page still contains custom diff renderer class %q", removed)
		}
	}

	// --- fixed light palette ---
	for _, decl := range []string{
		"color-scheme: light;",
		"--text: #1a1a1a;", "--text-muted: #6b6b6b;", "--accent: #d97757;",
		"--bg: #ffffff;", "--bg-muted: #f7f7f8;", "--border: #e5e5e5;",
	} {
		if !strings.Contains(page, decl) {
			t.Errorf(":root palette declaration missing or changed: %s", decl)
		}
	}

	// --- typography and component contract ---
	for _, marker := range []string{
		"font-family: system-ui, -apple-system, sans-serif;",
		"input, textarea, button { font: inherit; }",
		"outline: none;",
		"border-color: var(--accent);",
		"font-variant-numeric: tabular-nums;",
		"border-radius: 8px",
		"font-size: 1.5rem; text-align: center",
		"font-size: 0.85rem; font-weight: 400; line-height: 1.5",
		"grid-template-columns: minmax(0, 1fr)",
		"#2ea043",
		"#e5484d",
		`content: "\2192"`,
		`rx="0" class="seq-actor"`,
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing typography style marker %q", marker)
		}
	}

	for _, gone := range []string{
		`id="theme-toggle"`, `data-theme="dark"`, "prefers-color-scheme: dark",
		"Georgia", "ui-monospace", "SF Mono", "Menlo, monospace",
		"font-weight: 500", "font-weight: 650", "font-weight: 700",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still contains conflicting style %q", gone)
		}
	}
}

func assertDiffAssetsServed(t *testing.T, url string) {
	t.Helper()
	for _, asset := range []assetExpectation{
		{path: "assets/diff2html.min.css", contentType: "text/css", marker: ".d2h-wrapper"},
		{path: "assets/diff2html.min.js", contentType: "text/javascript", marker: "Diff2Html"},
		{path: "assets/marked.min.js", contentType: "text/javascript", marker: "marked"},
	} {
		assertAssetServed(t, url, asset)
	}
}

func assertAssetServed(t *testing.T, url string, asset assetExpectation) {
	t.Helper()
	resp, err := http.Get(url + asset.path)
	if err != nil {
		t.Fatalf("fetch %s: %v", asset.path, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read %s: %v", asset.path, readErr)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), asset.contentType) || !strings.Contains(string(body), asset.marker) {
		t.Errorf("asset %s was not served as %s with marker %q", asset.path, asset.contentType, asset.marker)
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
	if strings.Contains(first, "large streamed body") || strings.Contains(first, `"body"`) {
		t.Errorf("initial snapshot = %s, want log headers without output bodies", first)
	}

	updated := source.snapshot
	updated.Plan = "# Plan"
	source.publish(updated)

	second := readEventData(t, reader)
	if !strings.Contains(second, `"plan":"# Plan"`) {
		t.Errorf("update snapshot = %s, want the published plan", second)
	}

	source.publishLogUpdate()
	if event := readEventType(t, reader); event != "logs" {
		t.Errorf("output signal event = %q, want logs", event)
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

func readEventType(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event type: %v", err)
		}
		if strings.HasPrefix(line, "event: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		}
	}
}

func TestPlanStatusServerLetsBrowserFetchSequencedLogBatch(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	source.logBatch = models.LogBatch{
		Lines: []models.LogLine{{
			Sequence: 9, Stream: models.LogStreamExec, Entry: 2, Text: "tests pass\n",
		}},
		Next: 9, Latest: 11,
		Gap: &models.LogGap{Dropped: 3, Stream: models.LogStreamExec, Entry: 2},
	}
	server := clients.NewPlanStatusServer(
		source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	resp, err := http.Get(server.URL() + "logs?since=5")
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}
	defer resp.Body.Close()
	var batch models.LogBatch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if resp.StatusCode != http.StatusOK || source.requestedSeq != 5 {
		t.Fatalf("status/sequence = %d/%d, want 200/5", resp.StatusCode, source.requestedSeq)
	}
	if source.requestLimit < 1 || batch.Next != 9 || batch.Latest != 11 {
		t.Fatalf("batch = %+v with limit %d, want cursor 9/11", batch, source.requestLimit)
	}
	if batch.Gap == nil || batch.Gap.Dropped != 3 || len(batch.Lines) != 1 {
		t.Fatalf("batch = %+v, want gap marker and oldest available line", batch)
	}
}

func TestPlanStatusServerPagesBackwardsThroughLogHistory(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	source.beforeBatch = models.LogBatch{
		Lines: []models.LogLine{
			{Sequence: 3, Stream: models.LogStreamExec, Entry: 1, Text: "older\n"},
			{Sequence: 4, Stream: models.LogStreamExec, Entry: 1, Text: "old\n"},
		},
		Next: 4, Latest: 40,
	}
	server := clients.NewPlanStatusServer(
		source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	resp, err := http.Get(server.URL() + "logs?before=5")
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}
	defer resp.Body.Close()
	var batch models.LogBatch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if resp.StatusCode != http.StatusOK || source.beforeSeq != 5 || source.beforeLimit != 10 {
		t.Fatalf("status/before/limit = %d/%d/%d, want 200/5/10",
			resp.StatusCode, source.beforeSeq, source.beforeLimit)
	}
	if len(batch.Lines) != 2 || batch.Lines[0].Sequence != 3 || batch.Latest != 40 {
		t.Fatalf("batch = %+v, want the two persisted lines and latest 40", batch)
	}
}

func TestPlanStatusServerRejectsInvalidBackwardsCursors(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	server := clients.NewPlanStatusServer(
		source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	for _, cursor := range []string{"0", "not-a-sequence"} {
		resp, err := http.Get(server.URL() + "logs?before=" + cursor)
		if err != nil {
			t.Fatalf("fetch logs before %q: %v", cursor, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("before %q status = %d, want 400", cursor, resp.StatusCode)
		}
	}
	if source.beforeCalls != 0 {
		t.Fatalf("invalid cursors reached the source %d times, want 0", source.beforeCalls)
	}
}

func TestPlanStatusServerReportsLogHistoryFailure(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	source.beforeErr = errors.New("log history unavailable")
	server := clients.NewPlanStatusServer(
		source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	resp, err := http.Get(server.URL() + "logs?before=5")
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "log history unavailable") {
		t.Fatalf("body = %q, want the history failure reason", body)
	}
}

func TestPlanStatusServerRejectsInvalidLogCursor(t *testing.T) {
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	server := clients.NewPlanStatusServer(
		source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).
		WithLogSource(source)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer shutdown(t, server)

	resp, err := http.Get(server.URL() + "logs?since=not-a-sequence")
	if err != nil {
		t.Fatalf("fetch logs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// startAnnotateServer boots the production server over fakes for exercising
// the /annotate and /implement endpoints.
func startAnnotateServer(t *testing.T) (*clients.PlanStatusServer, *fakeAnnotationSink, *fakeImplementSink, func()) {
	t.Helper()
	source := newFakePlanStatusSource(models.PlanSessionStatus{})
	sink := &fakeAnnotationSink{}
	implement := &fakeImplementSink{}
	server := clients.NewPlanStatusServer(source, sink, implement, serverClock{t: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return server, sink, implement, func() { shutdown(t, server) }
}

// postWithToken issues a state-changing POST carrying the server's session
// token, the way the served page does.
func postWithToken(t *testing.T, server *clients.PlanStatusServer, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build post %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(clients.StatusSessionTokenHeader, server.SessionToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	resp.Body.Close()
	return resp
}

func postAnnotation(t *testing.T, server *clients.PlanStatusServer, body string) *http.Response {
	t.Helper()
	return postWithToken(t, server, "annotate", body)
}

func TestPlanStatusServerAcceptsValidAnnotation(t *testing.T) {
	server, sink, _, stop := startAnnotateServer(t)
	defer stop()

	resp := postAnnotation(t, server, `{"section":"steps","target":"Step 2: add the store","comment":"split this step"}`)

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
	server, sink, _, stop := startAnnotateServer(t)
	defer stop()

	for name, body := range map[string]string{
		"invalid JSON":    `{not json`,
		"unknown section": `{"section":"banner","comment":"hello"}`,
		"blank comment":   `{"section":"plan","comment":"   "}`,
	} {
		if resp := postAnnotation(t, server, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if len(sink.annotations) != 0 {
		t.Errorf("sink received %+v, want nothing", sink.annotations)
	}
}

func TestPlanStatusServerRejectsAnnotationGet(t *testing.T) {
	server, sink, _, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(server.URL() + "annotate")
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
	server, _, implement, stop := startAnnotateServer(t)
	defer stop()

	resp := postWithToken(t, server, "implement", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if implement.requests != 1 {
		t.Errorf("implement requests = %d, want exactly 1", implement.requests)
	}
}

func TestPlanStatusServerRejectsImplementGet(t *testing.T) {
	server, _, implement, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(server.URL() + "implement")
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
	server, _, _, stop := startAnnotateServer(t)
	defer stop()

	for _, path := range []string{"tests", "tests/journey", "tests/bdd", "exec", "explain"} {
		resp, err := http.Get(server.URL() + path)
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
