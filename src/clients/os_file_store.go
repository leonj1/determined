package clients

import "os"

// OsFileStore reads and writes the planning protocol files on the real
// filesystem.
type OsFileStore struct{}

// NewOsFileStore constructs an OsFileStore.
func NewOsFileStore() OsFileStore { return OsFileStore{} }

// Exists reports whether path is present.
func (OsFileStore) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Read returns the full contents of path.
func (OsFileStore) Read(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Write replaces path with content.
func (OsFileStore) Write(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// Append adds content to the end of path, creating it if absent.
func (OsFileStore) Append(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// Remove deletes path. A missing file is not an error.
func (OsFileStore) Remove(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
