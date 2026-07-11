package tests

import (
	"context"
	"errors"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

func TestInitializationReplacesBothKnowledgeDocuments(t *testing.T) {
	source := &fakeDocumentSource{contents: map[models.DocumentURL]models.DocumentContent{
		"https://example.com/CLAUDE.md": []byte("new Claude conventions\n"),
		"https://example.com/AGENTS.md": []byte("new agent conventions\n"),
	}}
	store := &fakeDocumentStore{contents: map[models.DestinationPath]models.DocumentContent{
		"/home/jose/.claude/CLAUDE.md": []byte("old Claude content"),
		"/home/jose/AGENTS.md":         []byte("old agent content"),
	}}

	err := services.NewInitializationService(source, store, initializationConfig()).Run(context.Background())

	if err != nil {
		t.Fatalf("initialization should succeed: %v", err)
	}
	assertDocument(t, store, "/home/jose/.claude/CLAUDE.md", "new Claude conventions\n")
	assertDocument(t, store, "/home/jose/AGENTS.md", "new agent conventions\n")
}

func TestInitializationDoesNotReplaceFilesWhenADownloadFails(t *testing.T) {
	source := &fakeDocumentSource{
		contents: map[models.DocumentURL]models.DocumentContent{
			"https://example.com/CLAUDE.md": []byte("new Claude conventions\n"),
		},
		failureURL: "https://example.com/AGENTS.md",
	}
	store := &fakeDocumentStore{contents: map[models.DestinationPath]models.DocumentContent{
		"/home/jose/.claude/CLAUDE.md": []byte("old Claude content"),
	}}

	err := services.NewInitializationService(source, store, initializationConfig()).Run(context.Background())

	if err == nil {
		t.Fatal("a failed document download should fail initialization")
	}
	assertDocument(t, store, "/home/jose/.claude/CLAUDE.md", "old Claude content")
}

func initializationConfig() models.InitializationConfig {
	return models.InitializationConfig{Documents: []models.InitializationDocument{
		{Source: "https://example.com/CLAUDE.md", Destination: "/home/jose/.claude/CLAUDE.md"},
		{Source: "https://example.com/AGENTS.md", Destination: "/home/jose/AGENTS.md"},
	}}
}

func assertDocument(t *testing.T, store *fakeDocumentStore, path models.DestinationPath, want string) {
	t.Helper()
	if got := string(store.contents[path]); got != want {
		t.Fatalf("document %s = %q, want %q", path, got, want)
	}
}

type fakeDocumentSource struct {
	contents   map[models.DocumentURL]models.DocumentContent
	failureURL models.DocumentURL
}

func (s *fakeDocumentSource) Fetch(
	_ context.Context,
	url models.DocumentURL,
) (models.DocumentContent, error) {
	if url == s.failureURL {
		return nil, errors.New("download failed")
	}
	return s.contents[url], nil
}

type fakeDocumentStore struct {
	contents map[models.DestinationPath]models.DocumentContent
}

func (s *fakeDocumentStore) Replace(path models.DestinationPath, content models.DocumentContent) error {
	s.contents[path] = content
	return nil
}
