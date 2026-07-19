package clients

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"determined/src/models"
)

//go:embed plan_status_page.html
var planStatusPage []byte

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
}

// NewPlanStatusServer constructs a PlanStatusServer over a status source, an
// annotation sink, and an implement sink.
func NewPlanStatusServer(source PlanStatusSource, annotations AnnotationSink, implement ImplementSink, clock clock) *PlanStatusServer {
	return &PlanStatusServer{source: source, annotations: annotations, implement: implement, clock: clock}
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
	mux.HandleFunc("/", s.servePage)
	mux.HandleFunc("/events", s.serveEvents)
	mux.HandleFunc("/annotate", s.serveAnnotate)
	mux.HandleFunc("/implement", s.serveImplement)
	s.server = &http.Server{Handler: mux}
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
	return s.server.Shutdown(ctx)
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

func writeEvent(w http.ResponseWriter, snapshot models.PlanSessionStatus) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
