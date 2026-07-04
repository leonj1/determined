package services

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"determined/src/models"
)

func TestUserCanUpdateDeterminedToLatestRelease(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{
		Version: "v1.0.12",
		Assets: []models.ReleaseAsset{{
			Name: "determined-linux-amd64",
			URL:  "https://example.com/determined-linux-amd64",
		}},
	}}
	installer := &fakeExecutableInstaller{}
	var terminal bytes.Buffer

	err := updateForTest("1.0.11", models.Platform{OS: "linux", Arch: "amd64"}, &releases, installer, &terminal)

	if err != nil {
		t.Fatalf("updating should succeed: %v", err)
	}
	if !installer.installed {
		t.Fatal("the latest release binary should be installed")
	}
	if !strings.Contains(terminal.String(), "updated determined to v1.0.12") {
		t.Fatalf("expected update confirmation, got %q", terminal.String())
	}
}

func TestUserCanSeeDeterminedIsAlreadyCurrent(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{
		Version: "v1.0.12",
		Assets:  []models.ReleaseAsset{{Name: "determined-linux-amd64", URL: "https://example.com/bin"}},
	}}
	installer := &fakeExecutableInstaller{}
	var terminal bytes.Buffer

	err := updateForTest("1.0.12", models.Platform{OS: "linux", Arch: "amd64"}, &releases, installer, &terminal)

	if err != nil {
		t.Fatalf("checking the latest release should succeed: %v", err)
	}
	if installer.installed {
		t.Fatal("an already-current binary should not be installed again")
	}
	if !strings.Contains(terminal.String(), "determined 1.0.12 is already up to date") {
		t.Fatalf("expected current-version confirmation, got %q", terminal.String())
	}
}

func TestUserCanUpdateDevelopmentBuild(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{
		Version: "v1.0.12",
		Assets:  []models.ReleaseAsset{{Name: "determined-darwin-arm64", URL: "https://example.com/bin"}},
	}}
	installer := &fakeExecutableInstaller{}

	err := updateForTest("dev", models.Platform{OS: "darwin", Arch: "arm64"}, &releases, installer, &bytes.Buffer{})

	if err != nil {
		t.Fatalf("updating a development build should succeed: %v", err)
	}
	if !installer.installed {
		t.Fatal("a development binary should install the latest release")
	}
}

func TestUserGetsClearUpdateFailureWhenPlatformHasNoRelease(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{Version: "v1.0.12"}}
	installer := &fakeExecutableInstaller{}

	err := updateForTest("1.0.11", models.Platform{OS: "windows", Arch: "amd64"}, &releases, installer, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "updates are not available for windows/amd64") {
		t.Fatalf("expected unsupported platform error, got %v", err)
	}
	if releases.called {
		t.Fatal("unsupported platforms should fail before fetching a release")
	}
}

func TestUserGetsClearUpdateFailureWhenReleaseAssetIsMissing(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{Version: "v1.0.12"}}
	installer := &fakeExecutableInstaller{}

	err := updateForTest("1.0.11", models.Platform{OS: "linux", Arch: "amd64"}, &releases, installer, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "release v1.0.12 has no asset determined-linux-amd64") {
		t.Fatalf("expected missing asset error, got %v", err)
	}
	if installer.installed {
		t.Fatal("missing release assets should not install anything")
	}
}

func TestUserGetsClearUpdateFailureWhenCurrentVersionCannotBeCompared(t *testing.T) {
	releases := fakeReleaseSource{release: models.Release{
		Version: "v1.0.12",
		Assets:  []models.ReleaseAsset{{Name: "determined-linux-amd64", URL: "https://example.com/bin"}},
	}}

	err := updateForTest("local", models.Platform{OS: "linux", Arch: "amd64"}, &releases, &fakeExecutableInstaller{}, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "version \"local\" is not major.minor.patch") {
		t.Fatalf("expected invalid version error, got %v", err)
	}
}

func updateForTest(
	current models.Version,
	platform models.Platform,
	releases *fakeReleaseSource,
	installer *fakeExecutableInstaller,
	terminal *bytes.Buffer,
) error {
	cfg := models.UpdateConfig{CurrentVersion: current, Platform: platform}
	return NewUpdateService(releases, installer, terminal, cfg).Run(context.Background())
}

type fakeReleaseSource struct {
	release models.Release
	err     error
	called  bool
}

func (s *fakeReleaseSource) Latest(context.Context) (models.Release, error) {
	s.called = true
	if s.err != nil {
		return models.Release{}, s.err
	}
	return s.release, nil
}

type fakeExecutableInstaller struct {
	installed bool
	err       error
}

func (i *fakeExecutableInstaller) Install(context.Context, models.ReleaseAsset) error {
	i.installed = true
	if i.err != nil {
		return errors.New("install failed")
	}
	return nil
}
