package models

import (
	"fmt"
	"strconv"
	"strings"
)

// SemanticVersion is a parsed major.minor.patch version.
type SemanticVersion struct {
	Major int
	Minor int
	Patch int
}

// ParseSemanticVersion parses a version string with an optional leading "v".
func ParseSemanticVersion(version Version) (SemanticVersion, error) {
	parts := strings.Split(strings.TrimPrefix(string(version), "v"), ".")
	if len(parts) != 3 {
		return SemanticVersion{}, fmt.Errorf("version %q is not major.minor.patch", version)
	}
	major, err := parseVersionPart(parts[0], version)
	if err != nil {
		return SemanticVersion{}, err
	}
	minor, err := parseVersionPart(parts[1], version)
	if err != nil {
		return SemanticVersion{}, err
	}
	patch, err := parseVersionPart(parts[2], version)
	if err != nil {
		return SemanticVersion{}, err
	}
	return SemanticVersion{Major: major, Minor: minor, Patch: patch}, nil
}

func parseVersionPart(part string, version Version) (int, error) {
	value, err := strconv.Atoi(part)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("version %q is not major.minor.patch", version)
	}
	return value, nil
}

// Less reports whether v is older than other.
func (v SemanticVersion) Less(other SemanticVersion) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}
