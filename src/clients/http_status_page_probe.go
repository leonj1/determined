package clients

import (
	"net/http"
	"time"
)

// StatusPageHeader is the response header the status server stamps on its page
// so a probe can tell it apart from an unrelated server that happens to have
// been handed the same recycled port.
const StatusPageHeader = "X-Determined-Status-Page"

// HttpStatusPageProbe confirms a URL is serving the interactive status page by
// fetching it. A listening socket is not enough: the port may since have been
// reused by another program, so the response must also identify itself.
type HttpStatusPageProbe struct {
	client *http.Client
}

// NewHttpStatusPageProbe constructs a probe with a short timeout, so a hung or
// unresponsive listener reports "not serving" instead of blocking the caller.
func NewHttpStatusPageProbe(timeout time.Duration) HttpStatusPageProbe {
	return HttpStatusPageProbe{client: &http.Client{Timeout: timeout}}
}

// Serving reports whether url answers with the determined status page.
func (p HttpStatusPageProbe) Serving(url string) bool {
	response, err := p.client.Get(url)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	return response.Header.Get(StatusPageHeader) == "1"
}
