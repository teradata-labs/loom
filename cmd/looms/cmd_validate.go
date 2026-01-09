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
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/evals"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/patterns"
)

var validateFilesCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate configuration files",
	Long: `Validate Loom configuration files (project config, backends, patterns).

Automatically detects file type based on 'kind' field in YAML.`,
}

var validateFileCmd = &cobra.Command{
	Use:   "file [path]",
	Short: "Validate a single configuration file",
	Long: `Validate a single configuration file.

The command automatically detects the file type by reading the 'kind' field:
- kind: Project -> Project configuration
- kind: Backend -> Backend configuration
- kind: PatternLibrary -> Pattern library configuration

Examples:
  looms validate file loom.yaml
  looms validate file backends/postgres.yaml
  looms validate file patterns/sql-optimization.yaml`,
	Args: cobra.ExactArgs(1),
	Run:  runValidateFile,
}

var validateDirCmd = &cobra.Command{
	Use:   "dir [path]",
	Short: "Validate all YAML files in a directory",
	Long: `Validate all .yaml and .yml files in a directory recursively.

Examples:
  looms validate dir examples/
  looms validate dir examples/backends/`,
	Args: cobra.ExactArgs(1),
	Run:  runValidateDir,
}

func init() {
	rootCmd.AddCommand(validateFilesCmd)
	validateFilesCmd.AddCommand(validateFileCmd)
	validateFilesCmd.AddCommand(validateDirCmd)
}

func runValidateFile(cmd *cobra.Command, args []string) {
	filePath := args[0]

	// Validate file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "❌ File not found: %s\n", filePath)
		os.Exit(1)
	}

	// Validate the file
	if err := validateSingleFile(filePath); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Validation failed for %s:\n   %v\n", filePath, err)
		os.Exit(1)
	}

	fmt.Printf("✅ %s is valid\n", filePath)
}

func runValidateDir(cmd *cobra.Command, args []string) {
	dirPath := args[0]

	// Check if directory exists
	info, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "❌ Directory not found: %s\n", dirPath)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "❌ Not a directory: %s\n", dirPath)
		os.Exit(1)
	}

	// Find all YAML files
	var yamlFiles []string
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			yamlFiles = append(yamlFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error walking directory: %v\n", err)
		os.Exit(1)
	}

	if len(yamlFiles) == 0 {
		fmt.Printf("No YAML files found in %s\n", dirPath)
		return
	}

	// Validate each file
	fmt.Printf("Validating %d YAML files in %s...\n\n", len(yamlFiles), dirPath)

	validCount := 0
	invalidCount := 0
	var errors []string

	for _, file := range yamlFiles {
		relPath, _ := filepath.Rel(dirPath, file)
		if err := validateSingleFile(file); err != nil {
			fmt.Printf("❌ %s\n", relPath)
			errors = append(errors, fmt.Sprintf("%s: %v", relPath, err))
			invalidCount++
		} else {
			fmt.Printf("✅ %s\n", relPath)
			validCount++
		}
	}

	// Summary
	fmt.Println()
	fmt.Println("Summary:")
	fmt.Printf("  Valid:   %d\n", validCount)
	fmt.Printf("  Invalid: %d\n", invalidCount)
	fmt.Printf("  Total:   %d\n", len(yamlFiles))

	if invalidCount > 0 {
		fmt.Println("\nErrors:")
		for _, errMsg := range errors {
			fmt.Printf("  - %s\n", errMsg)
		}
		os.Exit(1)
	}
}

// validateSingleFile validates a single file by detecting its type
func validateSingleFile(path string) error {
	// Read file to detect kind
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	content := string(data)

	// Detect file type by 'kind:' field
	var kind string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "kind:") {
			kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
			break
		}
	}

	// Validate based on kind
	switch kind {
	case "Project":
		_, err := loomconfig.LoadProject(path)
		return err

	case "Backend":
		_, err := fabric.LoadBackend(path)
		return err

	case "PatternLibrary":
		_, err := patterns.LoadPatternLibrary(path)
		return err

	case "EvalSuite":
		_, err := evals.LoadEvalSuite(path)
		return err

	case "":
		return fmt.Errorf("no 'kind' field found in YAML")

	default:
		return fmt.Errorf("unknown kind: %s (expected: Project, Backend, PatternLibrary, or EvalSuite)", kind)
	}
}
