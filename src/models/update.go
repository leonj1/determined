package models

// Version is the semantic version stamped into a determined binary.
type Version string

// Repository identifies the GitHub repository that publishes releases.
type Repository struct {
	Owner string
	Name  string
}

// APIBaseURL is the base URL for a GitHub-compatible API.
type APIBaseURL string

// Platform identifies the operating system and CPU architecture of a binary.
type Platform struct {
	OS   string
	Arch string
}

// AssetName is the name of a downloadable release asset.
type AssetName string

// DownloadURL is the URL used to download a release asset.
type DownloadURL string

// ReleaseAsset is a downloadable binary attached to a release.
type ReleaseAsset struct {
	Name AssetName
	URL  DownloadURL
}

// Release is the latest published determined version and its assets.
type Release struct {
	Version Version
	Assets  []ReleaseAsset
}

// AssetNamed returns the release asset with the requested name.
func (r Release) AssetNamed(name AssetName) (ReleaseAsset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return ReleaseAsset{}, false
}

// UpdateConfig holds the inputs for one self-update run.
type UpdateConfig struct {
	CurrentVersion Version
	Platform       Platform
}
