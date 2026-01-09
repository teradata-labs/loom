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
package fabric

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Correction represents a suggested fix for an error.
type Correction struct {
	OriginalSQL     string
	CorrectedSQL    string
	Explanation     string
	ErrorCode       string
	ConfidenceLevel string // "high", "medium", "low"
}

// ErrorAnalysisInfo contains detailed error analysis for self-correction.
type ErrorAnalysisInfo struct {
	ErrorType   string   // e.g., "syntax_error", "permission_denied", "table_not_found"
	Summary     string   // Human-readable error summary
	Suggestions []string // Actionable suggestions for fixing
}

// ErrorRecord stores recent error information for self-correction.
type ErrorRecord struct {
	SQL              string
	ErrorCode        string
	ErrorMessage     string
	Timestamp        string
	AttemptCount     int                // Tracks retry attempts
	PreviousAttempts []string           // History of failed SQL
	ErrorAnalysis    *ErrorAnalysisInfo // Enhanced error analysis
}

// Issue represents a validation issue found during pre-flight check.
type Issue struct {
	Severity    string // "error", "warning", "info"
	Message     string
	Suggestion  string
	LineNumber  int
	ColumnRange string
}

// GuardrailEngine performs pre-flight validation and error correction.
// It tracks errors across attempts and provides self-correction suggestions.
type GuardrailEngine struct {
	mu         sync.RWMutex
	errorCache map[string]*ErrorRecord // keyed by session ID
	validators []Validator             // Pluggable validators
}

// Validator interface allows backend-specific validation rules.
type Validator interface {
	Name() string
	Validate(ctx context.Context, sql string) []Issue
}

// NewGuardrailEngine creates a new guardrail engine.
func NewGuardrailEngine() *GuardrailEngine {
	return &GuardrailEngine{
		errorCache: make(map[string]*ErrorRecord),
		validators: make([]Validator, 0),
	}
}

// RegisterValidator adds a backend-specific validator.
func (g *GuardrailEngine) RegisterValidator(v Validator) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.validators = append(g.validators, v)
}

// PreflightCheck performs validation before SQL execution.
// Returns issues found across all registered validators.
func (g *GuardrailEngine) PreflightCheck(ctx context.Context, sql string) []Issue {
	g.mu.RLock()
	validators := g.validators
	g.mu.RUnlock()

	issues := make([]Issue, 0)
	for _, validator := range validators {
		validatorIssues := validator.Validate(ctx, sql)
		issues = append(issues, validatorIssues...)
	}

	return issues
}

// HandleError analyzes an error and suggests corrections.
// This is the basic version without enhanced error analysis.
func (g *GuardrailEngine) HandleError(ctx context.Context, sessionID, sql, errorCode, errorMessage string) *Correction {
	analysis := &ErrorAnalysisInfo{
		ErrorType:   InferErrorType(errorCode, errorMessage),
		Summary:     errorMessage,
		Suggestions: []string{},
	}
	return g.HandleErrorWithAnalysis(ctx, sessionID, sql, analysis)
}

// HandleErrorWithAnalysis analyzes an error with enhanced context and suggests corrections.
// This is the primary entry point for self-correction.
func (g *GuardrailEngine) HandleErrorWithAnalysis(ctx context.Context, sessionID, sql string, analysis *ErrorAnalysisInfo) *Correction {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Store error in cache
	record, exists := g.errorCache[sessionID]
	if !exists {
		record = &ErrorRecord{
			SQL:              sql,
			ErrorCode:        "", // Filled by backend
			ErrorMessage:     analysis.Summary,
			AttemptCount:     0,
			PreviousAttempts: []string{},
			ErrorAnalysis:    analysis,
		}
		g.errorCache[sessionID] = record
	}

	record.AttemptCount++
	record.PreviousAttempts = append(record.PreviousAttempts, sql)
	record.ErrorAnalysis = analysis

	// Generate correction based on error type
	return g.suggestCorrection(analysis, sql, record.AttemptCount)
}

// suggestCorrection generates correction based on error analysis.
// Returns generic suggestions - backend-specific corrections should use SQLCorrector.
func (g *GuardrailEngine) suggestCorrection(analysis *ErrorAnalysisInfo, sql string, attemptCount int) *Correction {
	correction := &Correction{
		OriginalSQL: sql,
	}

	// Generic corrections based on error type
	switch analysis.ErrorType {
	case "syntax_error":
		correction.Explanation = "SQL syntax error detected. Check for:\n" +
			"- Missing or extra parentheses\n" +
			"- Reserved keyword usage (quote with double quotes)\n" +
			"- Comma placement in SELECT/WHERE clauses"
		correction.ConfidenceLevel = "medium"
		if len(analysis.Suggestions) > 0 {
			correction.Explanation += "\n\nSuggestions:\n- " + strings.Join(analysis.Suggestions, "\n- ")
		}

	case "table_not_found", "object_not_found":
		correction.Explanation = "Table/object does not exist. Verify:\n" +
			"- Table name spelling and case sensitivity\n" +
			"- Database/schema qualification (database.table)\n" +
			"- Table exists in the current session"
		correction.ConfidenceLevel = "high"

	case "column_not_found":
		correction.Explanation = "Column not found. Call GetTableSchema to discover actual column names"
		correction.ConfidenceLevel = "high"

	case "permission_denied":
		correction.Explanation = "Insufficient permissions. Check:\n" +
			"- User has required grants (SELECT, INSERT, UPDATE, etc.)\n" +
			"- Database access permissions\n" +
			"- Object ownership"
		correction.ConfidenceLevel = "high"

	case "timeout":
		correction.Explanation = "Query timeout. Consider:\n" +
			"- Adding WHERE clause to limit data\n" +
			"- Using indexes for better performance\n" +
			"- Breaking into smaller queries\n" +
			"- Caching intermediate results"
		correction.ConfidenceLevel = "medium"

	default:
		correction.Explanation = fmt.Sprintf("Error encountered (attempt %d): %s", attemptCount, analysis.Summary)
		correction.ConfidenceLevel = "low"
		if len(analysis.Suggestions) > 0 {
			correction.Explanation += "\n\nSuggestions:\n- " + strings.Join(analysis.Suggestions, "\n- ")
		}
	}

	return correction
}

// GetErrorRecord retrieves the error history for a session.
func (g *GuardrailEngine) GetErrorRecord(sessionID string) *ErrorRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.errorCache[sessionID]
}

// ClearErrorRecord removes error history for a session (e.g., on successful execution).
func (g *GuardrailEngine) ClearErrorRecord(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.errorCache, sessionID)
}

// InferErrorType attempts to classify error from code/message.
// Backend-specific implementations can override this.
func InferErrorType(errorCode, errorMessage string) string {
	messageLower := strings.ToLower(errorMessage)

	if strings.Contains(messageLower, "syntax") {
		return "syntax_error"
	}
	// Check for permission errors first
	if strings.Contains(messageLower, "permission") || strings.Contains(messageLower, "access denied") || strings.Contains(messageLower, "does not have") {
		return "permission_denied"
	}
	// Check for column errors before table errors (more specific first)
	if strings.Contains(messageLower, "column") && (strings.Contains(messageLower, "not found") || strings.Contains(messageLower, "does not exist")) {
		return "column_not_found"
	}
	// Check for table/object errors
	if (strings.Contains(messageLower, "table") || strings.Contains(messageLower, "object")) && (strings.Contains(messageLower, "not found") || strings.Contains(messageLower, "does not exist")) {
		return "table_not_found"
	}
	if strings.Contains(messageLower, "timeout") || strings.Contains(messageLower, "exceeded") {
		return "timeout"
	}

	return "unknown"
}
