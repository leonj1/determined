package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

type chatStatusSource struct {
	mu         sync.Mutex
	snapshot   models.PlanSessionStatus
	updates    chan models.PlanSessionStatus
	subscribed chan struct{}
	once       sync.Once
}

func newChatStatusSource(snapshot models.PlanSessionStatus) *chatStatusSource {
	return &chatStatusSource{snapshot: snapshot, updates: make(chan models.PlanSessionStatus, 16), subscribed: make(chan struct{})}
}

func (s *chatStatusSource) Snapshot() models.PlanSessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *chatStatusSource) Subscribe() (<-chan models.PlanSessionStatus, func()) {
	s.mu.Lock()
	s.updates <- s.snapshot
	s.mu.Unlock()
	s.once.Do(func() { close(s.subscribed) })
	return s.updates, func() {}
}

func (s *chatStatusSource) publish(snapshot models.PlanSessionStatus) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	s.updates <- snapshot
}

func startChatServer(t *testing.T) (string, *chatStatusSource, func()) {
	t.Helper()
	source := newChatStatusSource(chatSnapshot())
	clock := serverClock{t: time.Date(2026, 7, 21, 10, 2, 0, 0, time.UTC)}
	chat := services.NewChatService(source, clock)
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{}, clock).WithChatResponder(chat)
	if err := server.Start(); err != nil {
		t.Fatalf("start chat server: %v", err)
	}
	return server.URL(), source, func() { shutdown(t, server) }
}

func dialChat(t *testing.T, url string) *clients.WebSocketConn {
	t.Helper()
	address := strings.Replace(url, "http://", "ws://", 1) + "chat"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := clients.DialWebSocket(ctx, address)
	if err != nil {
		t.Fatalf("dial chat: %v", err)
	}
	connection.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	return connection
}

func writeChatJSON(t *testing.T, connection *clients.WebSocketConn, request models.ChatRequest) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := connection.WriteText(payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readChatJSON(t *testing.T, connection *clients.WebSocketConn) models.ChatResponse {
	t.Helper()
	payload, err := connection.ReadText()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response models.ChatResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode response %q: %v", payload, err)
	}
	return response
}

func TestWebSocketChatCorrelatesMessageAndReply(t *testing.T) {
	url, _, stop := startChatServer(t)
	defer stop()
	connection := dialChat(t, url)
	defer connection.Close() //nolint:errcheck

	writeChatJSON(t, connection, models.ChatRequest{ID: "41", Type: models.ChatRequestMessage, Text: "status"})
	response := readChatJSON(t, connection)

	if response.ID != "41" || response.Type != models.ChatResponseReply || response.Data == nil {
		t.Fatalf("response = %+v, want correlated structured reply", response)
	}
}

func TestMalformedWebSocketPayloadKeepsChatOpen(t *testing.T) {
	url, _, stop := startChatServer(t)
	defer stop()
	connection := dialChat(t, url)
	defer connection.Close() //nolint:errcheck

	if err := connection.WriteText([]byte("{not-json")); err != nil {
		t.Fatalf("write malformed payload: %v", err)
	}
	if response := readChatJSON(t, connection); response.Type != models.ChatResponseError {
		t.Fatalf("malformed response = %+v, want typed error", response)
	}
	writeChatJSON(t, connection, models.ChatRequest{ID: "after", Type: models.ChatRequestMessage, Text: "help"})
	if response := readChatJSON(t, connection); response.ID != "after" || response.Type != models.ChatResponseReply {
		t.Fatalf("response after malformed payload = %+v, want open connection", response)
	}
	if err := connection.WriteText([]byte(`{"id":"missing-type","text":"status"}`)); err != nil {
		t.Fatalf("write missing-type request: %v", err)
	}
	if response := readChatJSON(t, connection); response.ID != "missing-type" || response.Type != models.ChatResponseError {
		t.Fatalf("missing-type response = %+v, want correlated validation error", response)
	}
}

func TestSubscribedWebSocketReceivesStatusEvent(t *testing.T) {
	url, source, stop := startChatServer(t)
	defer stop()
	connection := dialChat(t, url)
	defer connection.Close() //nolint:errcheck

	writeChatJSON(t, connection, models.ChatRequest{ID: "sub", Type: models.ChatRequestSubscribe})
	select {
	case <-source.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not subscribe to status source")
	}
	updated := source.Snapshot()
	updated.Steps = append(updated.Steps, models.PlanStep{Message: "verifying chat"})
	source.publish(updated)
	response := readChatJSON(t, connection)

	if response.ID != "" || response.Type != models.ChatResponseEvent || response.Event != "step" || response.Text != "verifying chat" {
		t.Fatalf("event = %+v, want uncorrelated step event", response)
	}
}

func TestHTTPChatAskUsesTheSameStructuredReply(t *testing.T) {
	url, _, stop := startChatServer(t)
	defer stop()
	response, err := http.Post(url+"chat/ask", "application/json", strings.NewReader(`{"text":"progress"}`))
	if err != nil {
		t.Fatalf("post chat ask: %v", err)
	}
	defer response.Body.Close()
	var reply models.ChatResponse
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		t.Fatalf("decode ask response: %v", err)
	}
	if response.StatusCode != http.StatusOK || reply.Type != models.ChatResponseReply || reply.Data.Intent != models.ChatIntentSteps {
		t.Fatalf("status = %d reply = %+v, want steps reply", response.StatusCode, reply)
	}
}

func TestHTTPChatAskRejectsBadMethodAndJSON(t *testing.T) {
	url, _, stop := startChatServer(t)
	defer stop()
	get, err := http.Get(url + "chat/ask")
	if err != nil {
		t.Fatalf("get chat ask: %v", err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", get.StatusCode)
	}
	post, err := http.Post(url+"chat/ask", "application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("post bad chat ask: %v", err)
	}
	io.Copy(io.Discard, post.Body) //nolint:errcheck
	post.Body.Close()
	if post.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON status = %d, want 400", post.StatusCode)
	}
	extra, err := http.Post(url+"chat/ask", "application/json", strings.NewReader(`{"text":"status"}{}`))
	if err != nil {
		t.Fatalf("post trailing JSON: %v", err)
	}
	extra.Body.Close()
	if extra.StatusCode != http.StatusBadRequest {
		t.Errorf("trailing JSON status = %d, want 400", extra.StatusCode)
	}
}
