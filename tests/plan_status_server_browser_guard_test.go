package tests

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"determined/src/clients"
)

// These tests prove the browser-borne defenses from FIXES.md (2026-08-02,
// second finding): a Host allowlist against DNS rebinding, a per-session token
// against cross-site request forgery, and Origin validation on the chat
// WebSocket upgrade.

// sendWithHost issues a request whose Host header names an arbitrary origin —
// the shape a DNS-rebound page produces — over a plain loopback connection.
func sendWithHost(t *testing.T, server *clients.PlanStatusServer, method, path, host, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL()+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Host = host
	if token != "" {
		req.Header.Set(clients.StatusSessionTokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp
}

func TestHostGuardRejectsForeignHostUnderDefaultBind(t *testing.T) {
	server, _, implement, stop := startAnnotateServer(t)
	defer stop()

	if resp := sendWithHost(t, server, http.MethodGet, "", "attacker.example", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET / with foreign Host status = %d, want 403", resp.StatusCode)
	}
	post := sendWithHost(t, server, http.MethodPost, "implement", "attacker.example", server.SessionToken())
	if post.StatusCode != http.StatusForbidden {
		t.Errorf("POST /implement with foreign Host status = %d, want 403", post.StatusCode)
	}
	if implement.requests != 0 {
		t.Errorf("implement requests = %d, want 0 after rebound requests", implement.requests)
	}
	wrongPort := fmt.Sprintf("127.0.0.1:%d", server.Port()+1)
	if resp := sendWithHost(t, server, http.MethodGet, "", wrongPort, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET / with Host %s status = %d, want 403", wrongPort, resp.StatusCode)
	}
	for _, host := range []string{fmt.Sprintf("localhost:%d", server.Port()), fmt.Sprintf("127.0.0.1:%d", server.Port()), "localhost"} {
		if resp := sendWithHost(t, server, http.MethodGet, "", host, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("GET / with local Host %s status = %d, want 200", host, resp.StatusCode)
		}
	}
}

func TestStateChangingPostsRequireSessionToken(t *testing.T) {
	server, annotations, implement, stop := startAnnotateServer(t)
	defer stop()

	paths := []string{"implement", "annotate", "task/skip", "task/stop", "stall/choice", "explain/start", "chat/ask"}
	for _, path := range paths {
		resp, err := http.Post(server.URL()+path, "text/plain", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("tokenless POST /%s status = %d, want 403", path, resp.StatusCode)
		}
	}
	if implement.requests != 0 || len(annotations.annotations) != 0 {
		t.Fatalf("sinks saw %d implement requests and %+v annotations, want none from tokenless posts", implement.requests, annotations.annotations)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL()+"implement", nil)
	if err != nil {
		t.Fatalf("build tokened implement: %v", err)
	}
	req.Header.Set(clients.StatusSessionTokenHeader, pageEmbeddedToken(t, server))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post tokened implement: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("page-token POST /implement status = %d, want 202", resp.StatusCode)
	}
	if implement.requests != 1 {
		t.Fatalf("implement requests = %d, want exactly 1 from the page flow", implement.requests)
	}
}

// pageEmbeddedToken fetches the served page and pulls the credential out of
// its session-token meta tag — the exact flow the browser page uses.
func pageEmbeddedToken(t *testing.T, server *clients.PlanStatusServer) string {
	t.Helper()
	resp, err := http.Get(server.URL())
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	match := regexp.MustCompile(`name="session-token" content="([0-9a-f]{64})"`).FindSubmatch(body)
	if match == nil {
		t.Fatal("served page does not embed a session token")
	}
	return string(match[1])
}

// upgradeStatus performs a raw, otherwise-valid WebSocket handshake against
// /chat with the supplied Origin and returns the HTTP status line.
func upgradeStatus(t *testing.T, server *clients.PlanStatusServer, origin string) string {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", server.Port()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	request := "GET /chat HTTP/1.1\r\n" +
		fmt.Sprintf("Host: 127.0.0.1:%d\r\n", server.Port()) +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n" +
		fmt.Sprintf("Origin: %s\r\n\r\n", origin)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	return strings.TrimSpace(status)
}

func TestChatUpgradeRejectsForeignOrigin(t *testing.T) {
	server, _, stop := startChatServer(t)
	defer stop()

	foreign := upgradeStatus(t, server, "http://attacker.example")
	if !strings.Contains(foreign, "403") {
		t.Errorf("foreign-Origin upgrade status = %q, want 403", foreign)
	}
	local := upgradeStatus(t, server, fmt.Sprintf("http://localhost:%d", server.Port()))
	if !strings.Contains(local, "101") {
		t.Errorf("local-Origin upgrade status = %q, want 101", local)
	}
}
