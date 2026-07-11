package clients

import (
	"os"
	"path/filepath"

	"determined/src/models"
)

// OsDocumentStore installs knowledge documents on the real filesystem.
type OsDocumentStore struct{}

// NewOsDocumentStore constructs an OsDocumentStore.
func NewOsDocumentStore() OsDocumentStore { return OsDocumentStore{} }

// Replace creates the parent directory and overwrites the destination.
func (OsDocumentStore) Replace(
	path models.DestinationPath,
	content models.DocumentContent,
) error {
	destination := string(path)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, content, 0o644)
}
