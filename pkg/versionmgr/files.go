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
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileTarget represents a file that contains version information
type FileTarget struct {
	Path        string
	Description string
	UpdateFunc  func(path string, version Version) error
	ExtractFunc func(path string) ([]string, error)
}

// GetAllTargets returns all files that need version updates
func GetAllTargets(repoRoot string) []FileTarget {
	return []FileTarget{
		{
			Path:        filepath.Join(repoRoot, "VERSION"),
			Description: "Canonical version file",
			UpdateFunc:  updateVersionFile,
			ExtractFunc: extractVersionFile,
		},
		{
			Path:        filepath.Join(repoRoot, "internal/version/version.go"),
			Description: "Go version constant",
			UpdateFunc:  updateGoVersionFile,
			ExtractFunc: extractGoVersionFile,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/macos/homebrew/loom.rb"),
			Description: "Homebrew loom formula",
			UpdateFunc:  updateHomebrewFormula,
			ExtractFunc: extractHomebrewFormula,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/macos/homebrew/loom-server.rb"),
			Description: "Homebrew loom-server formula",
			UpdateFunc:  updateHomebrewFormula,
			ExtractFunc: extractHomebrewFormula,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/windows/chocolatey/loom.nuspec"),
			Description: "Chocolatey package spec",
			UpdateFunc:  updateChocolateySpec,
			ExtractFunc: extractChocolateySpec,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/windows/scoop/loom.json"),
			Description: "Scoop loom manifest",
			UpdateFunc:  updateScoopManifest,
			ExtractFunc: extractScoopManifest,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/windows/scoop/loom-server.json"),
			Description: "Scoop loom-server manifest",
			UpdateFunc:  updateScoopManifest,
			ExtractFunc: extractScoopManifest,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/windows/winget/Teradata.Loom.installer.yaml"),
			Description: "Winget installer manifest",
			UpdateFunc:  updateWingetInstaller,
			ExtractFunc: extractWingetInstaller,
		},
		{
			Path:        filepath.Join(repoRoot, "packaging/windows/winget/Teradata.Loom.locale.en-US.yaml"),
			Description: "Winget locale manifest",
			UpdateFunc:  updateWingetLocale,
			ExtractFunc: extractWingetLocale,
		},
		{
			Path:        filepath.Join(repoRoot, "README.md"),
			Description: "README version badge",
			UpdateFunc:  updateReadmeVersion,
			ExtractFunc: extractReadmeVersion,
		},
		{
			Path:        filepath.Join(repoRoot, "CLAUDE.md"),
			Description: "CLAUDE.md version reference",
			UpdateFunc:  updateClaudeVersion,
			ExtractFunc: extractClaudeVersion,
		},
	}
}

// updateVersionFile updates the canonical VERSION file (single line)
func updateVersionFile(path string, version Version) error {
	return os.WriteFile(path, []byte(version.String()+"\n"), 0644)
}

// extractVersionFile extracts version from VERSION file
func extractVersionFile(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []string{strings.TrimSpace(string(content))}, nil
}

// updateGoVersionFile updates internal/version/version.go
func updateGoVersionFile(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Replace var Version = "X.Y.Z" pattern
	re := regexp.MustCompile(`var Version = "[^"]*"`)
	newContent := re.ReplaceAllString(string(content), fmt.Sprintf(`var Version = "%s"`, version.String()))

	return os.WriteFile(path, []byte(newContent), 0644)
}

// extractGoVersionFile extracts version from Go version file
func extractGoVersionFile(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`var Version = "([^"]*)"`)
	matches := re.FindStringSubmatch(string(content))
	if len(matches) < 2 {
		return nil, fmt.Errorf("version pattern not found")
	}

	return []string{matches[1]}, nil
}

// updateHomebrewFormula updates Homebrew .rb formula files
func updateHomebrewFormula(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	text := string(content)

	// Replace version "X.Y.Z"
	re1 := regexp.MustCompile(`version "[^"]*"`)
	text = re1.ReplaceAllString(text, fmt.Sprintf(`version "%s"`, version.String()))

	// Replace URLs with /vX.Y.Z/
	re2 := regexp.MustCompile(`/v[0-9]+\.[0-9]+\.[0-9]+/`)
	text = re2.ReplaceAllString(text, fmt.Sprintf("/%s/", version.WithV()))

	return os.WriteFile(path, []byte(text), 0644)
}

// extractHomebrewFormula extracts versions from Homebrew formula
func extractHomebrewFormula(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	versions := make(map[string]bool)

	// Extract version "X.Y.Z"
	re1 := regexp.MustCompile(`version "([^"]*)"`)
	if matches := re1.FindStringSubmatch(string(content)); len(matches) >= 2 {
		versions[matches[1]] = true
	}

	// Extract versions from URLs
	re2 := regexp.MustCompile(`/v([0-9]+\.[0-9]+\.[0-9]+)/`)
	for _, match := range re2.FindAllStringSubmatch(string(content), -1) {
		if len(match) >= 2 {
			versions[match[1]] = true
		}
	}

	result := make([]string, 0, len(versions))
	for v := range versions {
		result = append(result, v)
	}
	return result, nil
}

// updateChocolateySpec updates Chocolatey .nuspec XML file
func updateChocolateySpec(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	type Metadata struct {
		XMLName      xml.Name `xml:"metadata"`
		Version      string   `xml:"version"`
		ReleaseNotes string   `xml:"releaseNotes"`
	}

	type Package struct {
		XMLName  xml.Name `xml:"package"`
		Xmlns    string   `xml:"xmlns,attr"`
		Metadata Metadata `xml:"metadata"`
	}

	var pkg Package
	if err := xml.Unmarshal(content, &pkg); err != nil {
		return fmt.Errorf("failed to parse XML: %w", err)
	}

	// Update version
	pkg.Metadata.Version = version.String()

	// Update releaseNotes URL
	releaseNotesURL := fmt.Sprintf("https://github.com/teradata-labs/loom/releases/tag/%s", version.WithV())
	pkg.Metadata.ReleaseNotes = releaseNotesURL

	// Marshal back to XML with proper formatting
	output, err := xml.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal XML: %w", err)
	}

	// Add XML declaration
	final := []byte(xml.Header + string(output) + "\n")

	return os.WriteFile(path, final, 0644)
}

// extractChocolateySpec extracts version from Chocolatey spec
func extractChocolateySpec(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`<version>([^<]+)</version>`)
	matches := re.FindStringSubmatch(string(content))
	if len(matches) < 2 {
		return nil, fmt.Errorf("version not found")
	}

	return []string{matches[1]}, nil
}

// updateScoopManifest updates Scoop JSON manifest
func updateScoopManifest(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var manifest map[string]any
	if err := json.Unmarshal(content, &manifest); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Update version
	manifest["version"] = version.String()

	// Update URLs in architecture section
	if arch, ok := manifest["architecture"].(map[string]any); ok {
		for _, archData := range arch {
			if archMap, ok := archData.(map[string]interface{}); ok {
				if url, ok := archMap["url"].(string); ok {
					// Replace version in URL
					re := regexp.MustCompile(`/v[0-9]+\.[0-9]+\.[0-9]+/`)
					archMap["url"] = re.ReplaceAllString(url, fmt.Sprintf("/%s/", version.WithV()))
				}
			}
		}
	}

	// Marshal back to JSON with proper formatting
	output, err := json.MarshalIndent(manifest, "", "    ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return os.WriteFile(path, append(output, '\n'), 0644)
}

// extractScoopManifest extracts version from Scoop manifest
func extractScoopManifest(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var manifest map[string]any
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, err
	}

	if version, ok := manifest["version"].(string); ok {
		return []string{version}, nil
	}

	return nil, fmt.Errorf("version not found")
}

// updateWingetInstaller updates Winget installer YAML
func updateWingetInstaller(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var data map[string]any
	if err := yaml.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Update PackageVersion
	data["PackageVersion"] = version.String()

	// Update URLs in Installers
	if installers, ok := data["Installers"].([]any); ok {
		for _, installer := range installers {
			if instMap, ok := installer.(map[string]interface{}); ok {
				if url, ok := instMap["InstallerUrl"].(string); ok {
					re := regexp.MustCompile(`/v[0-9]+\.[0-9]+\.[0-9]+/`)
					instMap["InstallerUrl"] = re.ReplaceAllString(url, fmt.Sprintf("/%s/", version.WithV()))
				}
			}
		}
	}

	// Marshal back to YAML
	output, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	return os.WriteFile(path, output, 0644)
}

// extractWingetInstaller extracts version from Winget installer
func extractWingetInstaller(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data map[string]any
	if err := yaml.Unmarshal(content, &data); err != nil {
		return nil, err
	}

	if version, ok := data["PackageVersion"].(string); ok {
		return []string{version}, nil
	}

	return nil, fmt.Errorf("PackageVersion not found")
}

// updateWingetLocale updates Winget locale YAML
func updateWingetLocale(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var data map[string]any
	if err := yaml.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Update PackageVersion
	data["PackageVersion"] = version.String()

	// Update ReleaseNotes and ReleaseNotesUrl
	releaseNotesURL := fmt.Sprintf("See https://github.com/teradata-labs/loom/releases/tag/%s", version.WithV())
	releaseNotesURLFull := fmt.Sprintf("https://github.com/teradata-labs/loom/releases/tag/%s", version.WithV())

	data["ReleaseNotes"] = releaseNotesURL
	data["ReleaseNotesUrl"] = releaseNotesURLFull

	// Marshal back to YAML
	output, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	return os.WriteFile(path, output, 0644)
}

// extractWingetLocale extracts version from Winget locale
func extractWingetLocale(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data map[string]any
	if err := yaml.Unmarshal(content, &data); err != nil {
		return nil, err
	}

	if version, ok := data["PackageVersion"].(string); ok {
		return []string{version}, nil
	}

	return nil, fmt.Errorf("PackageVersion not found")
}

// updateReadmeVersion updates README.md version badge
func updateReadmeVersion(path string, version Version) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Replace **Version**: vX.Y.Z pattern
	re := regexp.MustCompile(`\*\*Version\*\*: v[0-9]+\.[0-9]+\.[0-9]+`)
	newContent := re.ReplaceAllString(string(content), fmt.Sprintf("**Version**: %s", version.WithV()))

	return os.WriteFile(path, []byte(newContent), 0644)
}

// extractReadmeVersion extracts version from README.md
func extractReadmeVersion(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`\*\*Version\*\*: v([0-9]+\.[0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(string(content))
	if len(matches) < 2 {
		return nil, fmt.Errorf("version not found")
	}

	return []string{matches[1]}, nil
}

// updateClaudeVersion updates CLAUDE.md version reference
func updateClaudeVersion(path string, version Version) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Update line 6: **Version**: vX.Y.Z
		if lineNum == 6 && strings.HasPrefix(line, "**Version**:") {
			line = fmt.Sprintf("**Version**: %s", version.WithV())
		}

		lines = append(lines, line)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// extractClaudeVersion extracts version from CLAUDE.md
func extractClaudeVersion(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		if lineNum == 6 {
			line := scanner.Text()
			re := regexp.MustCompile(`\*\*Version\*\*: v([0-9]+\.[0-9]+\.[0-9]+)`)
			if matches := re.FindStringSubmatch(line); len(matches) >= 2 {
				return []string{matches[1]}, nil
			}
			break
		}
	}

	return nil, fmt.Errorf("version not found on line 6")
}
