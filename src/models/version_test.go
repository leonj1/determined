package models_test

import (
	"testing"

	"determined/src/models"
)

func TestUserCanComparePublishedVersions(t *testing.T) {
	oldPatch, err := models.ParseSemanticVersion("v1.2.3")
	if err != nil {
		t.Fatalf("published version should parse: %v", err)
	}
	newPatch, err := models.ParseSemanticVersion("1.2.4")
	if err != nil {
		t.Fatalf("published version should parse without v prefix: %v", err)
	}
	newMinor, err := models.ParseSemanticVersion("1.3.0")
	if err != nil {
		t.Fatalf("published minor version should parse: %v", err)
	}
	newMajor, err := models.ParseSemanticVersion("2.0.0")
	if err != nil {
		t.Fatalf("published major version should parse: %v", err)
	}

	if !oldPatch.Less(newPatch) {
		t.Fatal("1.2.3 should be older than 1.2.4")
	}
	if !newPatch.Less(newMinor) {
		t.Fatal("1.2.4 should be older than 1.3.0")
	}
	if !newMinor.Less(newMajor) {
		t.Fatal("1.3.0 should be older than 2.0.0")
	}
	if newPatch.Less(oldPatch) {
		t.Fatal("1.2.4 should not be older than 1.2.3")
	}
}

func TestUserGetsClearFailureForInvalidPublishedVersion(t *testing.T) {
	_, err := models.ParseSemanticVersion("latest")

	if err == nil {
		t.Fatal("invalid versions should be rejected")
	}
}

func TestUserCanSelectReleaseAssetForTheirPlatform(t *testing.T) {
	release := models.Release{Assets: []models.ReleaseAsset{{
		Name: "determined-linux-amd64",
		URL:  "https://example.com/bin",
	}}}

	asset, ok := release.AssetNamed("determined-linux-amd64")

	if !ok || asset.URL == "" {
		t.Fatalf("expected release asset, got %#v", asset)
	}
}

func TestUserCanSeeReleaseAssetIsUnavailable(t *testing.T) {
	release := models.Release{}

	_, ok := release.AssetNamed("determined-linux-amd64")

	if ok {
		t.Fatal("missing release asset should be reported")
	}
}
