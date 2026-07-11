package clients

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"determined/src/models"
)

// HttpDocumentSource downloads knowledge documents over HTTP.
type HttpDocumentSource struct {
	client *http.Client
}

// NewHttpDocumentSource constructs an HttpDocumentSource.
func NewHttpDocumentSource(client *http.Client) HttpDocumentSource {
	return HttpDocumentSource{client: client}
}

// Fetch returns the complete remote document content.
func (s HttpDocumentSource) Fetch(
	ctx context.Context,
	url models.DocumentURL,
) (models.DocumentContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(url), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("document request returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
