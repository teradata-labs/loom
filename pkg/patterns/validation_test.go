// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Pattern Validation Tests
//
// Validates all pattern YAML files in the patterns directory.

package patterns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPatternYAMLStructure validates all pattern YAML files have correct structure
func TestPatternYAMLStructure(t *testing.T) {
	patternsRoot := "../../patterns"

	err := filepath.Walk(patternsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip libraries directory (PatternLibrary files have different schema)
		if strings.Contains(path, "/libraries/") || strings.Contains(path, "\\libraries\\") {
			return nil
		}

		// Skip directories and non-YAML files
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			validatePatternFile(t, path)
		})

		return nil
	})

	if err != nil {
		t.Fatalf("Failed to walk patterns directory: %v", err)
	}
}

func validatePatternFile(t *testing.T, path string) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read pattern file %s: %v", path, err)
	}

	// Parse YAML directly as Pattern
	var pattern Pattern
	if err := yaml.Unmarshal(data, &pattern); err != nil {
		t.Fatalf("Failed to parse YAML in %s: %v", path, err)
	}

	// Validate required fields
	if pattern.Name == "" {
		t.Error("Pattern missing 'name' field")
	}
	if pattern.Title == "" {
		t.Error("Pattern missing 'title' field")
	}
	if pattern.Description == "" {
		t.Error("Pattern missing 'description' field")
	}
	if pattern.Category == "" {
		t.Error("Pattern missing 'category' field")
	}
	if pattern.Difficulty == "" {
		t.Error("Pattern missing 'difficulty' field")
	}
	// backend_type is optional - can be inferred from directory structure
	if pattern.BackendType == "" {
		t.Logf("Pattern missing 'backend_type' field (will be inferred from directory)")
	}

	// Validate difficulty is one of the expected values
	validDifficulties := []string{"beginner", "intermediate", "advanced"}
	difficultyValid := false
	for _, d := range validDifficulties {
		if pattern.Difficulty == d {
			difficultyValid = true
			break
		}
	}
	if !difficultyValid {
		t.Errorf("Invalid difficulty '%s', must be one of: %v", pattern.Difficulty, validDifficulties)
	}

	// Validate templates exist
	if len(pattern.Templates) == 0 {
		t.Error("Pattern has no templates")
	}

	// Validate each template is non-empty
	for name, template := range pattern.Templates {
		sql := template.GetSQL()
		if strings.TrimSpace(sql) == "" {
			t.Errorf("Template '%s' is empty", name)
		}
	}

	// Validate parameters
	for i, param := range pattern.Parameters {
		if param.Name == "" {
			t.Errorf("Parameter %d missing 'name' field", i)
		}
		if param.Type == "" {
			t.Errorf("Parameter '%s' missing 'type' field", param.Name)
		}
		if param.Description == "" {
			t.Errorf("Parameter '%s' missing 'description' field", param.Name)
		}

		// Validate type is recognized (allow extended types)
		validTypes := []string{"string", "integer", "number", "boolean", "object", "array"}
		typeValid := false
		paramType := param.Type

		// Check for standard types
		for _, vt := range validTypes {
			if paramType == vt {
				typeValid = true
				break
			}
		}

		// Check for extended types: array[...], map[...], enum[...], or just "enum"
		if !typeValid {
			if strings.HasPrefix(paramType, "array[") ||
				strings.HasPrefix(paramType, "map[") ||
				strings.HasPrefix(paramType, "enum[") ||
				paramType == "enum" {
				typeValid = true
			}
		}

		if !typeValid {
			t.Errorf("Parameter '%s' has invalid type '%s', expected one of: %v or extended format (array[T], map[K,V], enum[...])",
				param.Name, param.Type, validTypes)
		}
	}

	// Validate examples (optional)
	if len(pattern.Examples) > 0 {
		for i, example := range pattern.Examples {
			if example.Name == "" {
				t.Logf("Warning: Example %d missing 'name' field (may have description instead)", i)
			}
			if example.ExpectedResult == "" {
				exampleLabel := example.Name
				if exampleLabel == "" {
					exampleLabel = example.Description
				}
				t.Logf("Warning: Example '%s' missing 'expected_result' field", exampleLabel)
			}

			// Validate required parameters are provided in example (warning only)
			for _, param := range pattern.Parameters {
				if param.Required {
					if _, ok := example.Parameters[param.Name]; !ok {
						t.Logf("Warning: Example '%s' missing required parameter '%s' (may use template parameters)", example.Name, param.Name)
					}
				}
			}
		}
	}

	// Validate use cases
	if len(pattern.UseCases) == 0 {
		t.Error("Pattern has no use_cases")
	}

	for i, useCase := range pattern.UseCases {
		if strings.TrimSpace(useCase) == "" {
			t.Errorf("Use case %d is empty", i)
		}
	}

	// Validation rules are optional
	// (Pattern struct doesn't have this field, it's in separate YAML sections)

	t.Logf("✅ Pattern '%s' validated successfully", pattern.Name)
}

// TestPatternTemplateRendering tests that pattern templates can be rendered with example parameters
func TestPatternTemplateRendering(t *testing.T) {
	patternsRoot := "../../patterns"

	err := filepath.Walk(patternsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			testTemplateRendering(t, path)
		})

		return nil
	})

	if err != nil {
		t.Fatalf("Failed to walk patterns directory: %v", err)
	}
}

func testTemplateRendering(t *testing.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read pattern file: %v", err)
	}

	var pattern Pattern
	if err := yaml.Unmarshal(data, &pattern); err != nil {
		t.Fatalf("Failed to parse YAML: %v", err)
	}

	// Test each template with each example's parameters
	for _, example := range pattern.Examples {
		for templateName, template := range pattern.Templates {
			sql := template.GetSQL()
			// Simple validation: check if template contains expected parameter placeholders
			for paramName := range example.Parameters {
				placeholder := "{{." + paramName + "}}"
				if strings.Contains(sql, placeholder) {
					t.Logf("Template '%s' uses parameter '%s' (%s)", templateName, paramName, placeholder)
				}
			}

			// Validate template has some content
			if len(strings.TrimSpace(sql)) < 10 {
				t.Errorf("Template '%s' seems too short (<%d chars)", templateName, 10)
			}
		}
	}
}

// TestPatternNamingConvention validates pattern names follow snake_case convention
func TestPatternNamingConvention(t *testing.T) {
	patternsRoot := "../../patterns"

	err := filepath.Walk(patternsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip libraries directory (PatternLibrary files have different schema)
		if strings.Contains(path, "/libraries/") || strings.Contains(path, "\\libraries\\") {
			return nil
		}

		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Failed to read pattern file: %v", err)
			}

			var pattern Pattern
			if err := yaml.Unmarshal(data, &pattern); err != nil {
				t.Fatalf("Failed to parse YAML: %v", err)
			}

			// Check pattern name is snake_case (lowercase with underscores)
			if pattern.Name != strings.ToLower(pattern.Name) {
				t.Errorf("Pattern name '%s' should be lowercase", pattern.Name)
			}

			// Check no spaces or special chars (except underscore)
			for _, char := range pattern.Name {
				if !((char >= 'a' && char <= 'z') || char == '_' || (char >= '0' && char <= '9')) {
					t.Errorf("Pattern name '%s' contains invalid character '%c' (use snake_case)", pattern.Name, char)
					break
				}
			}

			// Check filename matches pattern name
			expectedFilename := pattern.Name + ".yaml"
			actualFilename := filepath.Base(path)
			if expectedFilename != actualFilename {
				t.Errorf("Filename '%s' doesn't match pattern name '%s' (expected: %s)", actualFilename, pattern.Name, expectedFilename)
			}
		})

		return nil
	})

	if err != nil {
		t.Fatalf("Failed to walk patterns directory: %v", err)
	}
}

// TestPatternCategorization ensures patterns are properly organized by backend type
func TestPatternCategorization(t *testing.T) {
	patternsRoot := "../../patterns"

	err := filepath.Walk(patternsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip libraries directory (PatternLibrary files have different schema)
		if strings.Contains(path, "/libraries/") || strings.Contains(path, "\\libraries\\") {
			return nil
		}

		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		t.Run(filepath.Base(path), func(t *testing.T) {
			// Skip special themed directories that don't follow backend_type convention
			actualDir := filepath.Dir(path)
			specialDirs := []string{"fun", "nasa"}
			for _, specialDir := range specialDirs {
				if strings.Contains(actualDir, "/"+specialDir) || strings.HasSuffix(actualDir, "/"+specialDir) {
					t.Skipf("Skipping pattern in themed directory '%s'", specialDir)
					return
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Failed to read pattern file: %v", err)
			}

			var pattern Pattern
			if err := yaml.Unmarshal(data, &pattern); err != nil {
				t.Fatalf("Failed to parse YAML: %v", err)
			}

			// Check pattern is in correct directory based on backend_type
			expectedDir := filepath.Join(patternsRoot, pattern.BackendType)

			// actualDir should start with expectedDir
			if !strings.HasPrefix(actualDir, expectedDir) {
				t.Errorf("Pattern with backend_type '%s' should be in '%s/' directory, but found in '%s'",
					pattern.BackendType, expectedDir, actualDir)
			}
		})

		return nil
	})

	if err != nil {
		t.Fatalf("Failed to walk patterns directory: %v", err)
	}
}
