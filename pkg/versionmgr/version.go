// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package versionmgr

import (
	"fmt"
	"strconv"
	"strings"
)

// Version represents a semantic version (MAJOR.MINOR.PATCH)
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion parses a semantic version string.
// Accepts formats: "1.0.1", "v1.0.1", "1.0", "v1.0"
func ParseVersion(s string) (Version, error) {
	// Remove 'v' prefix if present
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimSpace(s)

	if s == "" {
		return Version{}, fmt.Errorf("empty version string")
	}

	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid version format: %q (expected MAJOR.MINOR or MAJOR.MINOR.PATCH)", s)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Version{}, fmt.Errorf("invalid major version: %w", err)
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid minor version: %w", err)
	}

	patch := 0
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return Version{}, fmt.Errorf("invalid patch version: %w", err)
		}
	}

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
	}, nil
}

// String returns the version as "MAJOR.MINOR.PATCH" (no 'v' prefix)
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// WithV returns the version as "vMAJOR.MINOR.PATCH" (with 'v' prefix)
func (v Version) WithV() string {
	return "v" + v.String()
}

// BumpMajor increments the major version and resets minor and patch to 0
func (v Version) BumpMajor() Version {
	return Version{
		Major: v.Major + 1,
		Minor: 0,
		Patch: 0,
	}
}

// BumpMinor increments the minor version and resets patch to 0
func (v Version) BumpMinor() Version {
	return Version{
		Major: v.Major,
		Minor: v.Minor + 1,
		Patch: 0,
	}
}

// BumpPatch increments the patch version
func (v Version) BumpPatch() Version {
	return Version{
		Major: v.Major,
		Minor: v.Minor,
		Patch: v.Patch + 1,
	}
}

// Equal returns true if versions are equal
func (v Version) Equal(other Version) bool {
	return v.Major == other.Major && v.Minor == other.Minor && v.Patch == other.Patch
}

// Compare returns:
//   -1 if v < other
//    0 if v == other
//    1 if v > other
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}
