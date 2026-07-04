package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"determined/src/models"
)

func TestUserCanDownloadReleaseBinaryForInstallation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("new binary"))
	}))
	defer server.Close()
	installer := NewSelfExecutableInstaller(server.Client())

	path, err := installer.download(context.Background(), models.DownloadURL(server.URL), t.TempDir(), 0o755)

	if err != nil {
		t.Fatalf("downloading a release binary should succeed: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "new binary" {
		t.Fatalf("expected downloaded binary content, got %q (err %v)", content, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected downloaded binary on disk: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected executable permissions, got %v", info.Mode().Perm())
	}
}

func TestInstallerReportsFailedBinaryDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	installer := NewSelfExecutableInstaller(server.Client())

	_, err := installer.download(context.Background(), models.DownloadURL(server.URL), t.TempDir(), 0o755)

	if err == nil || !strings.Contains(err.Error(), "download returned 404 Not Found") {
		t.Fatalf("expected download failure, got %v", err)
	}
}

func TestInstallerReportsSlowBinaryDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("new binary"))
	}))
	defer server.Close()
	installer := SelfExecutableInstaller{client: server.Client(), downloadTimeout: 10 * time.Millisecond}
	started := time.Now()

	_, err := installer.download(context.Background(), models.DownloadURL(server.URL), t.TempDir(), 0o755)

	if err == nil {
		t.Fatal("expected a slow download to fail")
	}
	if time.Since(started) > time.Second {
		t.Fatal("expected a slow download to stop before hanging")
	}
}

func TestInstallerCanInspectCurrentExecutable(t *testing.T) {
	path, err := currentExecutablePath()
	if err != nil {
		t.Fatalf("current executable should be discoverable: %v", err)
	}

	mode, err := executableMode(path)

	if err != nil {
		t.Fatalf("current executable permissions should be readable: %v", err)
	}
	if mode == 0 {
		t.Fatal("current executable should have filesystem permissions")
	}
}

func TestUserCanReplaceExecutableWithDownloadedBinary(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "determined")
	tmp := filepath.Join(dir, ".determined-update")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("creating current binary should succeed: %v", err)
	}
	if err := os.WriteFile(tmp, []byte("new binary"), 0o755); err != nil {
		t.Fatalf("creating downloaded binary should succeed: %v", err)
	}

	err := replaceExecutable(tmp, executable)

	if err != nil {
		t.Fatalf("replacing the binary should succeed: %v", err)
	}
	content, err := os.ReadFile(executable)
	if err != nil || string(content) != "new binary" {
		t.Fatalf("expected replacement binary content, got %q (err %v)", content, err)
	}
}
