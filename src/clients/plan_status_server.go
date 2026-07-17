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

// PlanStatusServer serves the interactive planning status page on loopback:
// the embedded HTML at / and a server-sent-events stream of full status
// snapshots at /events.
type PlanStatusServer struct {
	source   PlanStatusSource
	listener net.Listener
	server   *http.Server
}

// NewPlanStatusServer constructs a PlanStatusServer over a status source.
func NewPlanStatusServer(source PlanStatusSource) *PlanStatusServer {
	return &PlanStatusServer{source: source}
}

// Start binds an ephemeral loopback port and begins serving. It returns an
// error when the port cannot be bound; the caller treats that as fatal.
func (s *PlanStatusServer) Start() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("could not bind status server: %w", err)
	}
	s.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.servePage)
	mux.HandleFunc("/events", s.serveEvents)
	s.server = &http.Server{Handler: mux}
	go s.server.Serve(listener) //nolint:errcheck // Serve always returns on Shutdown/Close

	return nil
}

// URL returns the address browsers should open. Valid only after Start.
func (s *PlanStatusServer) URL() string {
	return fmt.Sprintf("http://%s/", s.listener.Addr().String())
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
	case "/", "/goal", "/plan", "/steps", "/log":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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

func writeEvent(w http.ResponseWriter, snapshot models.PlanSessionStatus) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
