package tests

import (
	"net/http"
	"testing"
)

// TestServedPageForbidsFraming asserts the clickjacking defense: every page
// response carries both anti-framing headers, so a hostile site cannot iframe
// the status page and clickjack the token-bearing Implement button.
func TestServedPageForbidsFraming(t *testing.T) {
	server, _, _, stop := startAnnotateServer(t)
	defer stop()

	resp, err := http.Get(server.URL() + "/")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want %q", got, "DENY")
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("Content-Security-Policy = %q, want %q", got, "frame-ancestors 'none'")
	}
}
