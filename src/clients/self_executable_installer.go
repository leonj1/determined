package clients

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"determined/src/models"
)

// SelfExecutableInstaller downloads a release asset and replaces this binary.
type SelfExecutableInstaller struct {
	client *http.Client
}

// NewSelfExecutableInstaller constructs a SelfExecutableInstaller.
func NewSelfExecutableInstaller(client *http.Client) SelfExecutableInstaller {
	return SelfExecutableInstaller{client: client}
}

// Install downloads asset and atomically renames it over the running binary.
func (i SelfExecutableInstaller) Install(ctx context.Context, asset models.ReleaseAsset) error {
	executable, err := currentExecutablePath()
	if err != nil {
		return err
	}
	mode, err := executableMode(executable)
	if err != nil {
		return err
	}
	tmp, err := i.download(ctx, asset.URL, filepath.Dir(executable), mode)
	if err != nil {
		return err
	}
	return replaceExecutable(tmp, executable)
}

func currentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func executableMode(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

func (i SelfExecutableInstaller) download(
	ctx context.Context,
	url models.DownloadURL,
	dir string,
	mode os.FileMode,
) (string, error) {
	resp, err := i.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(dir, ".determined-update-*")
	if err != nil {
		return "", err
	}
	return writeDownload(tmp, resp.Body, mode)
}

func (i SelfExecutableInstaller) get(ctx context.Context, url models.DownloadURL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(url), nil)
	if err != nil {
		return nil, err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	return resp, nil
}

func writeDownload(tmp *os.File, content io.Reader, mode os.FileMode) (string, error) {
	path := tmp.Name()
	if _, err := io.Copy(tmp, content); err != nil {
		tmp.Close()
		os.Remove(path)
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(path)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func replaceExecutable(tmp string, executable string) error {
	if err := os.Rename(tmp, executable); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
