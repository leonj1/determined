package tests

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"determined/src/clients"
	"determined/src/models"
)

func TestHttpDocumentSourceDownloadsCompleteDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "# Personal conventions\nUse typed values.\n")
	}))
	defer server.Close()

	content, err := clients.NewHttpDocumentSource(server.Client()).Fetch(
		context.Background(),
		models.DocumentURL(server.URL+"/CLAUDE.md"),
	)

	if err != nil || string(content) != "# Personal conventions\nUse typed values.\n" {
		t.Fatalf("expected complete downloaded document, got %q (err %v)", content, err)
	}
}

func TestOsDocumentStoreCreatesParentAndOverwritesFile(t *testing.T) {
	destination := filepath.Join(t.TempDir(), ".claude", "CLAUDE.md")
	store := clients.NewOsDocumentStore()
	if err := store.Replace(models.DestinationPath(destination), []byte("first")); err != nil {
		t.Fatalf("first document installation should succeed: %v", err)
	}
	if err := store.Replace(models.DestinationPath(destination), []byte("replacement")); err != nil {
		t.Fatalf("replacement document installation should succeed: %v", err)
	}

	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("expected overwritten document, got %q (err %v)", content, err)
	}
}
