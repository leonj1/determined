package clients

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"determined/src/models"
)

const (
	statusServerLoopbackHost                          = "127.0.0.1"
	statusServerReadHeaderTimeout                     = 5 * time.Second
	statusServerReadTimeout                           = 15 * time.Second
	statusServerWriteTimeout                          = 15 * time.Second
	statusServerIdleTimeout                           = 60 * time.Second
	statusLogBatchSize            models.LogBatchSize = 200
	statusLogBackBatchSize        models.LogBatchSize = 10
	// StatusSessionTokenHeader carries the per-session credential on
	// state-changing requests. The served page reads the token from its
	// session-token meta tag and sends it here; cross-site pages cannot read
	// that page, so they cannot present the token.
	StatusSessionTokenHeader = "X-Session-Token"
	// statusSessionTokenPlaceholder is replaced with the live token when the
	// embedded page is served.
	statusSessionTokenPlaceholder = "{{SESSION_TOKEN}}"
)

//go:embed plan_status_page.html
var planStatusPage []byte

//go:embed assets/diff2html.min.css assets/diff2html.min.js assets/marked.min.js
var planStatusAssets embed.FS

// PlanStatusSource is the slice of session state the server needs: the current
// snapshot for late joiners and a subscription for live updates. The real
// implementation is services.PlanStatusService.
type PlanStatusSource interface {
	Snapshot() models.PlanSessionStatus
	Subscribe() (<-chan models.PlanSessionStatus, func())
}

// LogSource supplies pull-based output batches and payload-free advance
// signals. LogsSince serves the live tail; LogsBefore pages backwards through
// the run's full persisted history.
type LogSource interface {
	LogsSince(models.LogSequence, models.LogBatchSize) models.LogBatch
	LogsBefore(models.LogSequence, models.LogBatchSize) (models.LogBatch, error)
	SubscribeLogOutput() (<-chan struct{}, func())
}

// AnnotationSink receives user feedback submitted from the status page. The
// real implementation is services.PlanStatusService.
type AnnotationSink interface {
	SubmitAnnotation(models.Annotation)
}

// ImplementSink receives the page's request to execute the completed plan. The
// real implementation is services.PlanStatusService.
type ImplementSink interface {
	RequestImplement()
}

// TaskControlSink receives the page's Skip and Stop commands for the active
// task, reporting whether an active task existed to act on. The real
// implementation is services.PlanStatusService.
type TaskControlSink interface {
	RequestSkipActiveTask() bool
	RequestStopRun() bool
}

// ExplainRequester receives the page's request to generate the post-execution
// explanation and quiz. The real implementation is services.PlanStatusService.
type ExplainRequester interface {
	RequestExplain()
}

// StallChoiceSink receives the page's tiebreak verdict for a stalled execute
// run, reporting whether a run was parked waiting to receive it. The real
// implementation is services.PlanStatusService.
type StallChoiceSink interface {
	SubmitStallChoice(models.StallDecision, string) bool
}

// PromptResponseSink receives answers to the application-owned prompt shown
// by the status page.
type PromptResponseSink interface {
	SubmitPromptResponse(models.PromptResponse) models.PromptSubmissionResult
}

// ChatResponder answers requests and derives pushed events from status diffs.
type ChatResponder interface {
	Answer(models.ChatRequest) models.ChatResponse
	Events(models.PlanSessionStatus, models.PlanSessionStatus) []models.ChatResponse
}

// PlanStatusServer serves the interactive planning status page on loopback:
// the embedded HTML at /, a server-sent-events stream of small status snapshots
// and log pings at /events, pull-based output at /logs, and user command routes.
type PlanStatusServer struct {
	source      PlanStatusSource
	logs        LogSource
	annotations AnnotationSink
	implement   ImplementSink
	taskControl TaskControlSink
	stallChoice StallChoiceSink
	prompts     PromptResponseSink
	explain     ExplainRequester
	clock       clock
	listener    net.Listener
	server      *http.Server
	chat        ChatResponder
	host        string
	token       string
	connections map[*WebSocketConn]struct{}
	mu          sync.Mutex
}

// NewPlanStatusServer constructs a PlanStatusServer over a status source, an
// annotation sink, and an implement sink.
func NewPlanStatusServer(source PlanStatusSource, annotations AnnotationSink, implement ImplementSink, clock clock) *PlanStatusServer {
	return &PlanStatusServer{
		source: source, annotations: annotations, implement: implement, clock: clock,
		host:        statusServerLoopbackHost,
		connections: make(map[*WebSocketConn]struct{}),
	}
}

// WithLogSource enables pull-based status-page log streaming.
func (s *PlanStatusServer) WithLogSource(source LogSource) *PlanStatusServer {
	s.logs = source
	return s
}

// WithTaskControl enables the page's Skip and Stop commands on the active task.
func (s *PlanStatusServer) WithTaskControl(sink TaskControlSink) *PlanStatusServer {
	s.taskControl = sink
	return s
}

// WithStallChoice enables the page's verification-deadlock tiebreak modal.
func (s *PlanStatusServer) WithStallChoice(sink StallChoiceSink) *PlanStatusServer {
	s.stallChoice = sink
	return s
}

// WithPromptResponses enables answers from the status-page prompt modal.
func (s *PlanStatusServer) WithPromptResponses(sink PromptResponseSink) *PlanStatusServer {
	s.prompts = sink
	return s
}

// WithExplainSink enables the page's Generate Explanation button.
func (s *PlanStatusServer) WithExplainSink(sink ExplainRequester) *PlanStatusServer {
	s.explain = sink
	return s
}

// WithBindHost overrides the loopback-only default bind interface. Exposing a
// non-loopback interface lets any host that can reach the port drive the
// page's state-changing endpoints — /implement starts unattended execution —
// so remote exposure must be an explicit caller opt-in, never the default.
func (s *PlanStatusServer) WithBindHost(host string) *PlanStatusServer {
	s.host = host
	return s
}

// WithChatResponder enables the read-only chat endpoints.
func (s *PlanStatusServer) WithChatResponder(chat ChatResponder) *PlanStatusServer {
	s.chat = chat
	return s
}

// Start binds an ephemeral port on the configured host — loopback unless
// WithBindHost opted into wider exposure — and begins serving. It returns an
// error when the port cannot be bound; the caller treats that as fatal.
func (s *PlanStatusServer) Start() error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	s.token = token
	listener, err := net.Listen("tcp", net.JoinHostPort(s.host, "0"))
	if err != nil {
		return fmt.Errorf("could not bind status server: %w", err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:           s.hostGuard(s.routes()),
		ReadHeaderTimeout: statusServerReadHeaderTimeout,
		ReadTimeout:       statusServerReadTimeout,
		WriteTimeout:      statusServerWriteTimeout,
		IdleTimeout:       statusServerIdleTimeout,
	}
	go s.server.Serve(listener) //nolint:errcheck // Serve always returns on Shutdown/Close
	return nil
}

func newSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// SessionToken returns the per-session credential required on state-changing
// endpoints. Valid only after Start; the served page embeds it.
func (s *PlanStatusServer) SessionToken() string {
	return s.token
}

// hostGuard rejects every request whose Host header names a non-local origin
// while the server is bound to loopback. DNS rebinding points an
// attacker-controlled name at 127.0.0.1 to make this server same-origin with a
// hostile page; a rebound request carries that foreign name in Host, so
// refusing it here closes the browser read-back vector.
func (s *PlanStatusServer) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "forbidden: unrecognized Host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHost admits only local names with the bound port under a loopback
// bind; an explicit non-loopback opt-in disables the check because the server
// is then reached through arbitrary hostnames by design.
func (s *PlanStatusServer) allowedHost(host string) bool {
	if !loopbackName(s.host) {
		return true
	}
	name, port := splitHostOptionalPort(host)
	return loopbackName(name) && (port == "" || port == strconv.Itoa(s.Port()))
}

func splitHostOptionalPort(host string) (string, string) {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return strings.Trim(host, "[]"), ""
	}
	return name, port
}

func loopbackName(name string) bool {
	switch strings.ToLower(name) {
	case "localhost", statusServerLoopbackHost, "::1":
		return true
	}
	return false
}

// allowedOrigin admits an absent Origin (non-browser clients such as the CLI
// chat dialer send none) and any local page origin under a loopback bind. A
// browser always stamps the true page origin, so refusing foreign ones blocks
// cross-site WebSocket use.
func (s *PlanStatusServer) allowedOrigin(origin string) bool {
	if origin == "" || !loopbackName(s.host) {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return s.allowedHost(parsed.Host)
}

// authorized admits requests presenting the per-session token; everything else
// — including requests arriving before Start assigned one — is refused.
func (s *PlanStatusServer) authorized(w http.ResponseWriter, r *http.Request) bool {
	presented := r.Header.Get(StatusSessionTokenHeader)
	if s.token != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1 {
		return true
	}
	http.Error(w, "missing or invalid session token", http.StatusForbidden)
	return false
}

func (s *PlanStatusServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/assets/", http.FileServer(http.FS(planStatusAssets)))
	mux.HandleFunc("/", s.servePage)
	mux.HandleFunc("/events", s.serveEvents)
	mux.HandleFunc("/logs", s.serveLogs)
	mux.HandleFunc("/annotate", s.serveAnnotate)
	mux.HandleFunc("/implement", s.serveImplement)
	mux.HandleFunc("/task/skip", s.serveTaskSkip)
	mux.HandleFunc("/task/stop", s.serveTaskStop)
	mux.HandleFunc("/stall/choice", s.serveStallChoice)
	mux.HandleFunc("/prompt/respond", s.servePromptResponse)
	mux.HandleFunc("/explain/start", s.serveExplainStart)
	mux.HandleFunc("/chat", s.serveChat)
	mux.HandleFunc("/chat/ask", s.serveChatAsk)
	return mux
}

// URL returns the address browsers should open. The server listens on
// loopback by default, so the printed host is localhost; only when the caller
// opted into a wider bind via WithBindHost can remote users substitute the
// machine's external IP with the same port. Valid only after Start.
func (s *PlanStatusServer) URL() string {
	return fmt.Sprintf("http://localhost:%d/", s.Port())
}

// Port returns the bound port. Valid only after Start.
func (s *PlanStatusServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Shutdown stops the server, releasing the port.
func (s *PlanStatusServer) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	err := s.server.Shutdown(ctx)
	s.closeChatConnections()
	return err
}

func (s *PlanStatusServer) closeChatConnections() {
	s.mu.Lock()
	connections := make([]*WebSocketConn, 0, len(s.connections))
	for connection := range s.connections {
		connections = append(connections, connection)
	}
	s.mu.Unlock()
	for _, connection := range connections {
		connection.Close() //nolint:errcheck
	}
}

func (s *PlanStatusServer) servePage(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/goal", "/plan", "/tests", "/tests/journey", "/tests/bdd", "/steps", "/log", "/exec", "/explain":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(StatusPageHeader, "1")
	// The status page is never legitimately framed; forbid embedding so a
	// hostile site cannot clickjack the token-bearing Implement button.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	page := bytes.ReplaceAll(planStatusPage, []byte(statusSessionTokenPlaceholder), []byte(s.token))
	w.Write(page) //nolint:errcheck // best-effort page write
}

func (s *PlanStatusServer) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// SSE responses intentionally remain open. Each heartbeat proves the
	// connection is still live, so the general HTTP write timeout does not fit.
	http.NewResponseController(w).SetWriteDeadline(time.Time{}) //nolint:errcheck
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	snapshots, cancel := s.source.Subscribe()
	defer cancel()
	logUpdates, cancelLogs := s.subscribeLogOutput()
	defer cancelLogs()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for nextStatusEvent(r.Context(), w, flusher, heartbeat.C, snapshots, logUpdates) {
	}
}

func nextStatusEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, heartbeat <-chan time.Time, snapshots <-chan models.PlanSessionStatus, logUpdates <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case <-heartbeat:
		fmt.Fprint(w, ": keepalive\n\n") //nolint:errcheck
		flusher.Flush()
		return true
	case snapshot, open := <-snapshots:
		if !open {
			return false
		}
		return flushStatusEvent(flusher, writeEvent(w, snapshot))
	case <-logUpdates:
		return flushStatusEvent(flusher, writeLogSignal(w))
	}
}

func flushStatusEvent(flusher http.Flusher, err error) bool {
	if err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (s *PlanStatusServer) subscribeLogOutput() (<-chan struct{}, func()) {
	if s.logs == nil {
		return nil, func() {}
	}
	return s.logs.SubscribeLogOutput()
}

func (s *PlanStatusServer) serveLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.logs == nil {
		http.Error(w, "log stream unavailable", http.StatusServiceUnavailable)
		return
	}
	if before := r.URL.Query().Get("before"); before != "" {
		s.serveLogsBefore(w, before)
		return
	}
	since, err := parseLogSequence(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, "since must be a non-negative integer", http.StatusBadRequest)
		return
	}
	writeLogBatch(w, s.logs.LogsSince(since, statusLogBatchSize))
}

// serveLogsBefore pages backwards: up to statusLogBackBatchSize persisted
// lines strictly older than the supplied sequence.
func (s *PlanStatusServer) serveLogsBefore(w http.ResponseWriter, value string) {
	before, err := parseLogSequence(value)
	if err != nil || before == 0 {
		http.Error(w, "before must be a positive integer", http.StatusBadRequest)
		return
	}
	batch, err := s.logs.LogsBefore(before, statusLogBackBatchSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeLogBatch(w, batch)
}

func writeLogBatch(w http.ResponseWriter, batch models.LogBatch) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(batch) //nolint:errcheck
}

func parseLogSequence(value string) (models.LogSequence, error) {
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseUint(value, 10, 64)
	return models.LogSequence(sequence), err
}

// serveAnnotate accepts one user annotation from the page, stamps its arrival
// time server-side, and queues it on the sink. Invalid payloads are rejected
// so the queue only ever holds actionable feedback.
func (s *PlanStatusServer) serveAnnotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	var annotation models.Annotation
	if err := json.NewDecoder(r.Body).Decode(&annotation); err != nil {
		http.Error(w, "invalid annotation payload", http.StatusBadRequest)
		return
	}
	annotation.At = s.clock.Now()
	if !annotation.Valid() {
		http.Error(w, "annotation requires a known section and a non-blank comment", http.StatusBadRequest)
		return
	}
	s.annotations.SubmitAnnotation(annotation)
	w.WriteHeader(http.StatusAccepted)
}

// serveImplement accepts the page's request to execute the completed plan and
// queues it on the sink; the sink ignores requests the session cannot honour.
func (s *PlanStatusServer) serveImplement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	s.implement.RequestImplement()
	w.WriteHeader(http.StatusAccepted)
}

// serveTaskSkip relays the page's confirmed Skip: abort the active task and
// let the run move on.
func (s *PlanStatusServer) serveTaskSkip(w http.ResponseWriter, r *http.Request) {
	s.serveTaskAction(w, r, func(sink TaskControlSink) bool { return sink.RequestSkipActiveTask() })
}

// serveTaskStop relays the page's confirmed Stop: abort the active task and
// end the whole run.
func (s *PlanStatusServer) serveTaskStop(w http.ResponseWriter, r *http.Request) {
	s.serveTaskAction(w, r, func(sink TaskControlSink) bool { return sink.RequestStopRun() })
}

// serveStallChoice relays the page's tiebreak verdict for a stalled run. It
// answers 409 when no run is parked waiting — the run may have been cancelled
// between the page's last snapshot and the click.
func (s *PlanStatusServer) serveStallChoice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if s.stallChoice == nil {
		http.Error(w, "stall choice unavailable", http.StatusServiceUnavailable)
		return
	}
	var guidance models.StallGuidance
	if err := json.NewDecoder(r.Body).Decode(&guidance); err != nil {
		http.Error(w, "invalid stall choice payload", http.StatusBadRequest)
		return
	}
	if !guidance.Valid() {
		http.Error(w, "stall choice requires a known decision and, for other, a non-blank comment", http.StatusBadRequest)
		return
	}
	if !s.stallChoice.SubmitStallChoice(guidance.Decision, guidance.Comment) {
		http.Error(w, "no run awaiting a stall choice", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// servePromptResponse relays one answer to the currently published prompt.
func (s *PlanStatusServer) servePromptResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if s.prompts == nil {
		http.Error(w, "prompt responses unavailable", http.StatusServiceUnavailable)
		return
	}
	var response models.PromptResponse
	if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
		http.Error(w, "invalid prompt response payload", http.StatusBadRequest)
		return
	}
	switch s.prompts.SubmitPromptResponse(response) {
	case models.PromptSubmissionAccepted:
		w.WriteHeader(http.StatusAccepted)
	case models.PromptSubmissionInvalid:
		http.Error(w, "invalid answer for active prompt", http.StatusBadRequest)
	default:
		http.Error(w, "no matching active prompt", http.StatusConflict)
	}
}

// serveExplainStart relays the page's confirmed request to generate the
// explanation and quiz, answering 409 when the session cannot honour the
// request (execution is still running, no execution has run, or the
// explanation has already been generated).
func (s *PlanStatusServer) serveExplainStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if s.explain == nil {
		http.Error(w, "explain unavailable", http.StatusServiceUnavailable)
		return
	}
	s.explain.RequestExplain()
	w.WriteHeader(http.StatusAccepted)
}

// serveTaskAction applies one task command, answering 409 when no active task
// exists to act on — the invocation may have finished between the page's last
// snapshot and the click.
func (s *PlanStatusServer) serveTaskAction(w http.ResponseWriter, r *http.Request, request func(TaskControlSink) bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if s.taskControl == nil {
		http.Error(w, "task control unavailable", http.StatusServiceUnavailable)
		return
	}
	if !request(s.taskControl) {
		http.Error(w, "no active task", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *PlanStatusServer) serveChat(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.Error(w, "chat unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.allowedOrigin(r.Header.Get("Origin")) {
		http.Error(w, "forbidden: foreign origin", http.StatusForbidden)
		return
	}
	connection, err := UpgradeWebSocket(w, r)
	if err != nil {
		return
	}
	s.rememberConnection(connection)
	defer s.forgetConnection(connection)
	defer connection.Close() //nolint:errcheck
	s.chatLoop(connection)
}

func (s *PlanStatusServer) chatLoop(connection *WebSocketConn) {
	subscribed := false
	done := make(chan struct{})
	defer close(done)
	for {
		payload, err := connection.ReadText()
		if err != nil {
			return
		}
		request, failure, valid := validatedChatRequest(payload)
		if !valid {
			s.writeChat(connection, failure) //nolint:errcheck
			continue
		}
		if request.Type == models.ChatRequestSubscribe {
			if !subscribed {
				subscribed = true
				go s.streamChatEvents(connection, done)
			}
			continue
		}
		if err := s.writeChat(connection, s.chat.Answer(request)); err != nil {
			return
		}
	}
}

func validatedChatRequest(payload []byte) (models.ChatRequest, models.ChatResponse, bool) {
	var request models.ChatRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		failure := models.ChatResponse{Type: models.ChatResponseError, Error: "malformed JSON"}
		return models.ChatRequest{}, failure, false
	}
	request = request.Normalized()
	if request.ID == "" {
		failure := models.ChatResponse{Type: models.ChatResponseError, Error: "request id is required"}
		return models.ChatRequest{}, failure, false
	}
	return request, models.ChatResponse{}, true
}

func (s *PlanStatusServer) streamChatEvents(connection *WebSocketConn, done <-chan struct{}) {
	snapshots, cancel := s.source.Subscribe()
	defer cancel()
	previous, open := firstChatSnapshot(snapshots, done)
	if !open {
		return
	}
	for {
		select {
		case <-done:
			return
		case snapshot, open := <-snapshots:
			if !open {
				return
			}
			if !s.sendChatEvents(connection, s.chat.Events(previous, snapshot)) {
				connection.Close() //nolint:errcheck
				return
			}
			previous = snapshot
		}
	}
}

func firstChatSnapshot(snapshots <-chan models.PlanSessionStatus, done <-chan struct{}) (models.PlanSessionStatus, bool) {
	select {
	case <-done:
		return models.PlanSessionStatus{}, false
	case snapshot, open := <-snapshots:
		return snapshot, open
	}
}

func (s *PlanStatusServer) sendChatEvents(connection *WebSocketConn, events []models.ChatResponse) bool {
	for _, event := range events {
		if err := s.writeChat(connection, event); err != nil {
			return false
		}
	}
	return true
}

func (s *PlanStatusServer) writeChat(connection *WebSocketConn, response models.ChatResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	connection.SetWriteDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	return connection.WriteText(payload)
}

func (s *PlanStatusServer) serveChatAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if s.chat == nil {
		http.Error(w, "chat unavailable", http.StatusServiceUnavailable)
		return
	}
	var request models.ChatRequest
	reader := http.MaxBytesReader(w, r.Body, webSocketMaxFrame)
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&request); err != nil || !jsonBodyEnded(decoder) {
		http.Error(w, "invalid chat payload", http.StatusBadRequest)
		return
	}
	request = request.AsHTTPMessage()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.chat.Answer(request)) //nolint:errcheck
}

func jsonBodyEnded(decoder *json.Decoder) bool {
	var extra json.RawMessage
	return decoder.Decode(&extra) == io.EOF
}

func (s *PlanStatusServer) rememberConnection(connection *WebSocketConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[connection] = struct{}{}
}

func (s *PlanStatusServer) forgetConnection(connection *WebSocketConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connections, connection)
}

func writeEvent(w http.ResponseWriter, snapshot models.PlanSessionStatus) error {
	payload, err := json.Marshal(snapshot.WithoutLogBodies())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func writeLogSignal(w http.ResponseWriter) error {
	_, err := fmt.Fprint(w, "event: logs\ndata:\n\n")
	return err
}
