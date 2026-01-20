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
	"os"
	"path/filepath"
	"strings"
)

// FileVersionInfo contains version information for a single file
type FileVersionInfo struct {
	Target         FileTarget
	Exists         bool
	Versions       []string
	ParsedVersions []Version
	InSync         bool
	Error          error
}

// DriftReport contains the results of drift detection
type DriftReport struct {
	CanonicalVersion Version
	Files            []FileVersionInfo
	HasDrift         bool
	TotalFiles       int
	FilesInSync      int
	FilesWithDrift   int
	MissingFiles     int
}

// DetectDrift scans all files and compares versions to the VERSION file
func DetectDrift(repoRoot string) (*DriftReport, error) {
	// Read canonical version
	canonical, err := ReadCanonicalVersion(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read canonical version: %w", err)
	}

	targets := GetAllTargets(repoRoot)
	report := &DriftReport{
		CanonicalVersion: canonical,
		Files:            make([]FileVersionInfo, 0, len(targets)),
		TotalFiles:       len(targets),
	}

	for _, target := range targets {
		info := FileVersionInfo{
			Target: target,
		}

		// Check if file exists
		if _, err := os.Stat(target.Path); err != nil {
			if os.IsNotExist(err) {
				info.Exists = false
				report.MissingFiles++
			} else {
				info.Error = err
			}
			report.Files = append(report.Files, info)
			continue
		}

		info.Exists = true

		// Extract versions from file
		versions, err := target.ExtractFunc(target.Path)
		if err != nil {
			info.Error = fmt.Errorf("failed to extract version: %w", err)
			report.Files = append(report.Files, info)
			continue
		}

		info.Versions = versions

		// Parse and compare versions
		allMatch := true
		for _, versionStr := range versions {
			parsed, err := ParseVersion(versionStr)
			if err != nil {
				info.Error = fmt.Errorf("failed to parse version %q: %w", versionStr, err)
				allMatch = false
				break
			}

			info.ParsedVersions = append(info.ParsedVersions, parsed)

			if !parsed.Equal(canonical) {
				allMatch = false
			}
		}

		info.InSync = allMatch && info.Error == nil

		if info.InSync {
			report.FilesInSync++
		} else {
			report.FilesWithDrift++
			report.HasDrift = true
		}

		report.Files = append(report.Files, info)
	}

	return report, nil
}

// PrintReport prints a human-readable drift report
func (r *DriftReport) PrintReport(verbose bool) {
	fmt.Printf("\n📋 Version Drift Report\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
	fmt.Printf("Canonical Version (VERSION file): %s\n\n", r.CanonicalVersion.WithV())

	fmt.Printf("Summary:\n")
	fmt.Printf("  Total files:        %d\n", r.TotalFiles)
	fmt.Printf("  ✓ In sync:          %d\n", r.FilesInSync)
	fmt.Printf("  ✗ With drift:       %d\n", r.FilesWithDrift)
	fmt.Printf("  ⚠  Missing:          %d\n", r.MissingFiles)
	fmt.Printf("\n")

	if r.HasDrift || r.MissingFiles > 0 {
		fmt.Printf("Files with issues:\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		for _, file := range r.Files {
			if !file.InSync || !file.Exists {
				r.printFileInfo(file, verbose)
			}
		}

		fmt.Printf("\n💡 To fix version drift, run:\n")
		fmt.Printf("   just version-sync\n\n")
	} else {
		fmt.Printf("✅ All files are in sync!\n\n")
	}
}

func (r *DriftReport) printFileInfo(file FileVersionInfo, verbose bool) {
	relPath := file.Target.Path
	if strings.HasPrefix(relPath, "/") {
		// Try to make it relative for cleaner display
		if cwd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(cwd, relPath); err == nil {
				relPath = rel
			}
		}
	}

	if !file.Exists {
		fmt.Printf("\n⚠️  %s\n", file.Target.Description)
		fmt.Printf("   File: %s\n", relPath)
		fmt.Printf("   Status: Missing\n")
		return
	}

	if file.Error != nil {
		fmt.Printf("\n❌ %s\n", file.Target.Description)
		fmt.Printf("   File: %s\n", relPath)
		fmt.Printf("   Error: %v\n", file.Error)
		return
	}

	fmt.Printf("\n✗ %s\n", file.Target.Description)
	fmt.Printf("   File: %s\n", relPath)
	fmt.Printf("   Expected: %s\n", r.CanonicalVersion.String())

	if len(file.ParsedVersions) > 0 {
		if len(file.ParsedVersions) == 1 {
			fmt.Printf("   Found:    %s\n", file.ParsedVersions[0].String())
		} else {
			fmt.Printf("   Found:    %s (and %d other version references)\n",
				file.ParsedVersions[0].String(), len(file.ParsedVersions)-1)
			if verbose {
				for i, v := range file.ParsedVersions[1:] {
					fmt.Printf("             %d. %s\n", i+2, v.String())
				}
			}
		}
	}
}

// ExitCode returns the appropriate exit code for the drift report
// 0 = no drift, 1 = drift detected
func (r *DriftReport) ExitCode() int {
	if r.HasDrift || r.MissingFiles > 0 {
		return 1
	}
	return 0
}

// PrintSummary prints a brief summary without detailed file info
func (r *DriftReport) PrintSummary() {
	if r.HasDrift || r.MissingFiles > 0 {
		fmt.Printf("❌ Version drift detected (%d files out of sync)\n", r.FilesWithDrift)
	} else {
		fmt.Printf("✅ All files in sync with version %s\n", r.CanonicalVersion.WithV())
	}
}
