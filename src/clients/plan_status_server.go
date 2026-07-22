package clients

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"determined/src/models"
)

const (
	statusServerReadHeaderTimeout = 5 * time.Second
	statusServerReadTimeout       = 15 * time.Second
	statusServerWriteTimeout      = 15 * time.Second
	statusServerIdleTimeout       = 60 * time.Second
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

// ChatResponder answers requests and derives pushed events from status diffs.
type ChatResponder interface {
	Answer(models.ChatRequest) models.ChatResponse
	Events(models.PlanSessionStatus, models.PlanSessionStatus) []models.ChatResponse
}

// PlanStatusServer serves the interactive planning status page on loopback:
// the embedded HTML at /, a server-sent-events stream of full status snapshots
// at /events, and an annotation intake at /annotate.
type PlanStatusServer struct {
	source      PlanStatusSource
	annotations AnnotationSink
	implement   ImplementSink
	clock       clock
	listener    net.Listener
	server      *http.Server
	chat        ChatResponder
	connections map[*WebSocketConn]struct{}
	mu          sync.Mutex
}

// NewPlanStatusServer constructs a PlanStatusServer over a status source, an
// annotation sink, and an implement sink.
func NewPlanStatusServer(source PlanStatusSource, annotations AnnotationSink, implement ImplementSink, clock clock) *PlanStatusServer {
	return &PlanStatusServer{
		source: source, annotations: annotations, implement: implement, clock: clock,
		connections: make(map[*WebSocketConn]struct{}),
	}
}

// WithChatResponder enables the read-only chat endpoints.
func (s *PlanStatusServer) WithChatResponder(chat ChatResponder) *PlanStatusServer {
	s.chat = chat
	return s
}

// Start binds an ephemeral port on all interfaces and begins serving. It
// returns an error when the port cannot be bound; the caller treats that as
// fatal.
func (s *PlanStatusServer) Start() error {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("could not bind status server: %w", err)
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.Handle("/assets/", http.FileServer(http.FS(planStatusAssets)))
	mux.HandleFunc("/", s.servePage)
	mux.HandleFunc("/events", s.serveEvents)
	mux.HandleFunc("/annotate", s.serveAnnotate)
	mux.HandleFunc("/implement", s.serveImplement)
	mux.HandleFunc("/chat", s.serveChat)
	mux.HandleFunc("/chat/ask", s.serveChatAsk)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: statusServerReadHeaderTimeout,
		ReadTimeout:       statusServerReadTimeout,
		WriteTimeout:      statusServerWriteTimeout,
		IdleTimeout:       statusServerIdleTimeout,
	}
	go s.server.Serve(listener) //nolint:errcheck // Serve always returns on Shutdown/Close

	return nil
}

// URL returns the address browsers should open. The server listens on all
// interfaces, so the printed host is localhost; remote users substitute the
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
	w.Write(planStatusPage) //nolint:errcheck // best-effort page write
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

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n") //nolint:errcheck
			flusher.Flush()
		case snapshot, open := <-snapshots:
			if !open {
				return
			}
			if err := writeEvent(w, snapshot); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// serveAnnotate accepts one user annotation from the page, stamps its arrival
// time server-side, and queues it on the sink. Invalid payloads are rejected
// so the queue only ever holds actionable feedback.
func (s *PlanStatusServer) serveAnnotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	s.implement.RequestImplement()
	w.WriteHeader(http.StatusAccepted)
}

func (s *PlanStatusServer) serveChat(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.Error(w, "chat unavailable", http.StatusServiceUnavailable)
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
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
