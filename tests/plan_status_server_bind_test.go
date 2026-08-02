package tests

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
)

// TestImplementUnreachableFromNonLoopbackByDefault proves the security fix
// from FIXES.md (2026-08-02): with the default bind, a client on a
// non-loopback interface cannot reach POST /implement at all — no connection,
// no execution request — while the legitimate loopback browser flow still can.
func TestImplementUnreachableFromNonLoopbackByDefault(t *testing.T) {
	implement := &fakeImplementSink{}
	server := clients.NewPlanStatusServer(newFakePlanStatusSource(models.PlanSessionStatus{}), &fakeAnnotationSink{}, implement, serverClock{})
	startBindServer(t, server)

	external := nonLoopbackAddress(t)
	address := net.JoinHostPort(external, fmt.Sprintf("%d", server.Port()))
	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		conn.Close()
		t.Fatalf("dial %s succeeded, want refused: default bind must exclude non-loopback interfaces", address)
	}
	if implement.requests != 0 {
		t.Fatalf("implement requests = %d, want 0 before any loopback call", implement.requests)
	}

	response := postImplementWithToken(t, server, fmt.Sprintf("http://127.0.0.1:%d/implement", server.Port()))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("loopback POST /implement status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if implement.requests != 1 {
		t.Fatalf("implement requests = %d, want 1 from the loopback flow", implement.requests)
	}
}

// TestImplementReachableAfterExplicitBindOptIn proves remote exposure remains
// available, but only through the explicit WithBindHost opt-in.
func TestImplementReachableAfterExplicitBindOptIn(t *testing.T) {
	implement := &fakeImplementSink{}
	server := clients.NewPlanStatusServer(newFakePlanStatusSource(models.PlanSessionStatus{}), &fakeAnnotationSink{}, implement, serverClock{}).
		WithBindHost("0.0.0.0")
	startBindServer(t, server)

	external := nonLoopbackAddress(t)
	response := postImplementWithToken(t, server, fmt.Sprintf("http://%s/implement", net.JoinHostPort(external, fmt.Sprintf("%d", server.Port()))))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("opted-in POST /implement status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if implement.requests != 1 {
		t.Fatalf("implement requests = %d, want 1 after the explicit opt-in", implement.requests)
	}
}

// postImplementWithToken posts /implement carrying the session token, so these
// bind-focused tests keep exercising the network layer rather than the
// token gate.
func postImplementWithToken(t *testing.T, server *clients.PlanStatusServer, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set(clients.StatusSessionTokenHeader, server.SessionToken())
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s error = %v, want nil", url, err)
	}
	response.Body.Close()
	return response
}

func startBindServer(t *testing.T, server *clients.PlanStatusServer) {
	t.Helper()
	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
	t.Cleanup(func() { server.Shutdown(t.Context()) }) //nolint:errcheck
}

// nonLoopbackAddress finds an IPv4 address of this machine outside loopback —
// the vantage point a LAN peer would use. Environments without one cannot
// exercise the remote path and skip.
func nonLoopbackAddress(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs() error = %v, want nil", err)
	}
	for _, address := range addresses {
		network, ok := address.(*net.IPNet)
		if !ok || network.IP.IsLoopback() || network.IP.To4() == nil {
			continue
		}
		return network.IP.String()
	}
	t.Skip("no non-loopback IPv4 interface available")
	return ""
}
