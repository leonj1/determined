package clients

import (
	"encoding/json"
	"os"
	"path/filepath"

	"determined/src/models"
)

// FileSessionRecordStore keeps the running session's record in a JSON file
// under the user's home directory, so a -link call from any terminal — not just
// the one that started the session — can find it.
type FileSessionRecordStore struct {
	path string
}

// NewFileSessionRecordStore constructs a store over an explicit file path.
func NewFileSessionRecordStore(path string) FileSessionRecordStore {
	return FileSessionRecordStore{path: path}
}

// DefaultSessionRecordPath returns the well-known record location,
// ~/.determined/session.json. It fails when the home directory is unknown
// rather than falling back to a directory the next call might not agree on.
func DefaultSessionRecordPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".determined", "session.json"), nil
}

// Load returns the recorded session, or an error when none is readable.
func (s FileSessionRecordStore) Load() (models.SessionRecord, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return models.SessionRecord{}, err
	}
	var record models.SessionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return models.SessionRecord{}, err
	}
	return record, nil
}

// Save writes the record, creating the containing directory if needed.
func (s FileSessionRecordStore) Save(record models.SessionRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o644)
}

// Clear removes the record. A missing file is not an error.
func (s FileSessionRecordStore) Clear() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
