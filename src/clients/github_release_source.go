package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"determined/src/models"
)

// GitHubReleaseSource reads determined release metadata from the GitHub API.
type GitHubReleaseSource struct {
	repository models.Repository
	baseURL    models.APIBaseURL
	client     *http.Client
}

// NewGitHubReleaseSource constructs a GitHubReleaseSource.
func NewGitHubReleaseSource(
	repository models.Repository,
	baseURL models.APIBaseURL,
	client *http.Client,
) GitHubReleaseSource {
	return GitHubReleaseSource{repository: repository, baseURL: baseURL, client: client}
}

// Latest returns the latest published release and its downloadable assets.
func (s GitHubReleaseSource) Latest(ctx context.Context) (models.Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.latestURL(), nil)
	if err != nil {
		return models.Release{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return models.Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return models.Release{}, fmt.Errorf("github latest release returned %s", resp.Status)
	}
	return decodeGitHubRelease(resp)
}

func (s GitHubReleaseSource) latestURL() string {
	base := strings.TrimRight(string(s.baseURL), "/")
	return fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, s.repository.Owner, s.repository.Name)
}

func decodeGitHubRelease(resp *http.Response) (models.Release, error) {
	var body githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return models.Release{}, err
	}
	return body.release(), nil
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (r githubRelease) release() models.Release {
	assets := make([]models.ReleaseAsset, 0, len(r.Assets))
	for _, asset := range r.Assets {
		assets = append(assets, asset.releaseAsset())
	}
	return models.Release{Version: models.Version(r.TagName), Assets: assets}
}

func (a githubAsset) releaseAsset() models.ReleaseAsset {
	return models.ReleaseAsset{
		Name: models.AssetName(a.Name),
		URL:  models.DownloadURL(a.BrowserDownloadURL),
	}
}
