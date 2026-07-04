package services

import (
	"context"
	"fmt"
	"io"

	"determined/src/models"
)

// ReleaseSource loads published determined releases from the outside world.
type ReleaseSource interface {
	Latest(ctx context.Context) (models.Release, error)
}

// ExecutableInstaller installs a downloaded release asset over this binary.
type ExecutableInstaller interface {
	Install(ctx context.Context, asset models.ReleaseAsset) error
}

// UpdateService updates the running determined binary to the latest release.
type UpdateService struct {
	releases  ReleaseSource
	installer ExecutableInstaller
	terminal  io.Writer
	cfg       models.UpdateConfig
}

// NewUpdateService wires an UpdateService from its dependencies.
func NewUpdateService(
	releases ReleaseSource,
	installer ExecutableInstaller,
	terminal io.Writer,
	cfg models.UpdateConfig,
) *UpdateService {
	return &UpdateService{releases: releases, installer: installer, terminal: terminal, cfg: cfg}
}

// Run fetches the latest release and installs its binary when it is newer.
func (s *UpdateService) Run(ctx context.Context) error {
	assetName, err := DeterminedAssetName(s.cfg.Platform)
	if err != nil {
		return err
	}
	release, err := s.releases.Latest(ctx)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	asset, err := releaseAsset(release, assetName)
	if err != nil {
		return err
	}
	update, err := shouldUpdate(s.cfg.CurrentVersion, release.Version)
	if err != nil {
		return err
	}
	return s.installIfNeeded(ctx, release.Version, asset, update)
}

// DeterminedAssetName returns the release asset name for a supported platform.
func DeterminedAssetName(platform models.Platform) (models.AssetName, error) {
	if platform.OS == "linux" && platform.Arch == "amd64" {
		return "determined-linux-amd64", nil
	}
	if platform.OS == "linux" && platform.Arch == "arm64" {
		return "determined-linux-arm64", nil
	}
	if platform.OS == "darwin" && platform.Arch == "arm64" {
		return "determined-darwin-arm64", nil
	}
	return "", fmt.Errorf("updates are not available for %s/%s", platform.OS, platform.Arch)
}

func releaseAsset(release models.Release, name models.AssetName) (models.ReleaseAsset, error) {
	asset, ok := release.AssetNamed(name)
	if !ok {
		return models.ReleaseAsset{}, fmt.Errorf("release %s has no asset %s", release.Version, name)
	}
	return asset, nil
}

func shouldUpdate(current models.Version, latest models.Version) (bool, error) {
	if current == "dev" {
		return true, nil
	}
	currentVersion, err := models.ParseSemanticVersion(current)
	if err != nil {
		return false, err
	}
	latestVersion, err := models.ParseSemanticVersion(latest)
	if err != nil {
		return false, err
	}
	return currentVersion.Less(latestVersion), nil
}

func (s *UpdateService) installIfNeeded(
	ctx context.Context,
	latest models.Version,
	asset models.ReleaseAsset,
	update bool,
) error {
	if !update {
		fmt.Fprintf(s.terminal, "determined %s is already up to date\n", s.cfg.CurrentVersion)
		return nil
	}
	fmt.Fprintf(s.terminal, "updating determined from %s to %s\n", s.cfg.CurrentVersion, latest)
	if err := s.installer.Install(ctx, asset); err != nil {
		return fmt.Errorf("install determined %s: %w", latest, err)
	}
	fmt.Fprintf(s.terminal, "updated determined to %s\n", latest)
	return nil
}
