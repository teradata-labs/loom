// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import "fmt"

// ValidationLevel represents the level of validation that detected an issue.
type ValidationLevel string

const (
	// LevelSyntax indicates a YAML syntax error (parsing failure)
	LevelSyntax ValidationLevel = "SYNTAX"
	// LevelStructure indicates a schema/structure violation (proto compliance)
	LevelStructure ValidationLevel = "STRUCTURE"
	// LevelSemantic indicates a logical consistency issue (missing references, invalid values)
	LevelSemantic ValidationLevel = "SEMANTIC"
)

// ValidationError represents a single validation issue.
type ValidationError struct {
	Level    ValidationLevel `json:"level"`
	Line     int             `json:"line,omitempty"`     // Line number where error occurred (0 if unknown)
	Field    string          `json:"field,omitempty"`    // Field path (e.g., "spec.tools", "metadata.name")
	Message  string          `json:"message"`            // Human-readable error message
	Fix      string          `json:"fix,omitempty"`      // Suggested fix
	Got      string          `json:"got,omitempty"`      // What was provided
	Expected string          `json:"expected,omitempty"` // What was expected
}

// ValidationWarning represents a non-blocking issue that should be reviewed.
type ValidationWarning struct {
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// ValidationResult contains the complete validation result for a YAML file.
type ValidationResult struct {
	Valid    bool                `json:"valid"`
	Errors   []ValidationError   `json:"errors,omitempty"`
	Warnings []ValidationWarning `json:"warnings,omitempty"`
	Kind     string              `json:"kind,omitempty"` // "Agent" or "Workflow"
	FilePath string              `json:"file_path,omitempty"`
}

// HasErrors returns true if there are any validation errors.
func (r *ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// HasWarnings returns true if there are any validation warnings.
func (r *ValidationResult) HasWarnings() bool {
	return len(r.Warnings) > 0
}

// ErrorCount returns the total number of errors.
func (r *ValidationResult) ErrorCount() int {
	return len(r.Errors)
}

// ErrorsByLevel returns errors grouped by validation level.
func (r *ValidationResult) ErrorsByLevel() map[ValidationLevel][]ValidationError {
	byLevel := make(map[ValidationLevel][]ValidationError)
	for _, err := range r.Errors {
		byLevel[err.Level] = append(byLevel[err.Level], err)
	}
	return byLevel
}

// FormatForWeaver formats the validation result in a human-readable format
// optimized for LLM consumption (clear structure, actionable fixes).
func (r *ValidationResult) FormatForWeaver() string {
	if r.Valid {
		return "✅ YAML validation passed - no issues detected"
	}

	output := "\n⚠️  YAML VALIDATION ISSUES DETECTED:\n\n"

	// Group by level for clarity
	byLevel := r.ErrorsByLevel()

	// Syntax errors first (most critical)
	if syntaxErrors, ok := byLevel[LevelSyntax]; ok {
		output += fmt.Sprintf("[SYNTAX] %d issue(s) - YAML parsing failed\n", len(syntaxErrors))
		for _, err := range syntaxErrors {
			output += formatError(err)
		}
		output += "\n"
	}

	// Structure errors (schema compliance)
	if structErrors, ok := byLevel[LevelStructure]; ok {
		output += fmt.Sprintf("[STRUCTURE] %d issue(s) - Schema violations\n", len(structErrors))
		for _, err := range structErrors {
			output += formatError(err)
		}
		output += "\n"
	}

	// Semantic errors (logical issues)
	if semanticErrors, ok := byLevel[LevelSemantic]; ok {
		output += fmt.Sprintf("[SEMANTIC] %d issue(s) - Logical consistency problems\n", len(semanticErrors))
		for _, err := range semanticErrors {
			output += formatError(err)
		}
		output += "\n"
	}

	// Warnings (non-blocking)
	if len(r.Warnings) > 0 {
		output += fmt.Sprintf("[WARNINGS] %d advisory message(s)\n", len(r.Warnings))
		for _, warn := range r.Warnings {
			output += fmt.Sprintf("  ⚡ %s", warn.Message)
			if warn.Field != "" {
				output += fmt.Sprintf(" (field: %s)", warn.Field)
			}
			if warn.Fix != "" {
				output += fmt.Sprintf("\n     Fix: %s", warn.Fix)
			}
			output += "\n"
		}
		output += "\n"
	}

	// Summary
	output += fmt.Sprintf("Summary: %d error(s), %d warning(s)\n", r.ErrorCount(), len(r.Warnings))
	if r.FilePath != "" {
		output += fmt.Sprintf("File: %s\n", r.FilePath)
	}
	output += "⚠️  File may not load correctly until issues are fixed\n"

	return output
}

// formatError formats a single validation error with clear structure.
func formatError(err ValidationError) string {
	output := fmt.Sprintf("  ❌ %s", err.Message)

	if err.Line > 0 {
		output += fmt.Sprintf(" (line %d)", err.Line)
	}
	if err.Field != "" {
		output += fmt.Sprintf("\n     Field: %s", err.Field)
	}
	if err.Got != "" && err.Expected != "" {
		output += fmt.Sprintf("\n     Expected: %s", err.Expected)
		output += fmt.Sprintf("\n     Got: %s", err.Got)
	} else if err.Expected != "" {
		output += fmt.Sprintf("\n     Expected: %s", err.Expected)
	} else if err.Got != "" {
		output += fmt.Sprintf("\n     Got: %s", err.Got)
	}
	if err.Fix != "" {
		output += fmt.Sprintf("\n     Fix: %s", err.Fix)
	}
	output += "\n"

	return output
}
