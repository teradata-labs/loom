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
	"sync"
	"testing"
)

// mockValidator for testing
type mockValidator struct {
	name   string
	issues []Issue
}

func (m *mockValidator) Name() string {
	return m.name
}

func (m *mockValidator) Validate(ctx context.Context, sql string) []Issue {
	return m.issues
}

func TestNewGuardrailEngine(t *testing.T) {
	engine := NewGuardrailEngine()

	if engine == nil {
		t.Fatal("NewGuardrailEngine returned nil")
	}

	if engine.errorCache == nil {
		t.Error("errorCache not initialized")
	}

	if engine.validators == nil {
		t.Error("validators not initialized")
	}
}

func TestRegisterValidator(t *testing.T) {
	engine := NewGuardrailEngine()

	validator := &mockValidator{
		name:   "test_validator",
		issues: []Issue{},
	}

	engine.RegisterValidator(validator)

	if len(engine.validators) != 1 {
		t.Errorf("expected 1 validator, got %d", len(engine.validators))
	}
}

func TestPreflightCheck(t *testing.T) {
	tests := []struct {
		name       string
		validators []Validator
		sql        string
		wantCount  int
	}{
		{
			name:       "no validators",
			validators: []Validator{},
			sql:        "SELECT * FROM table",
			wantCount:  0,
		},
		{
			name: "validator with issues",
			validators: []Validator{
				&mockValidator{
					name: "syntax_checker",
					issues: []Issue{
						{Severity: "error", Message: "syntax error", Suggestion: "fix syntax"},
					},
				},
			},
			sql:       "SELECT * FROM",
			wantCount: 1,
		},
		{
			name: "multiple validators",
			validators: []Validator{
				&mockValidator{
					name: "syntax_checker",
					issues: []Issue{
						{Severity: "error", Message: "syntax error"},
					},
				},
				&mockValidator{
					name: "security_checker",
					issues: []Issue{
						{Severity: "warning", Message: "potential SQL injection"},
					},
				},
			},
			sql:       "SELECT * FROM users WHERE id = " + "1",
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewGuardrailEngine()
			for _, v := range tt.validators {
				engine.RegisterValidator(v)
			}

			issues := engine.PreflightCheck(context.Background(), tt.sql)

			if len(issues) != tt.wantCount {
				t.Errorf("expected %d issues, got %d", tt.wantCount, len(issues))
			}
		})
	}
}

func TestHandleError(t *testing.T) {
	engine := NewGuardrailEngine()
	ctx := context.Background()

	sessionID := "test-session-1"
	sql := "SELECT * FROM nonexistent_table"
	errorCode := "3807"
	errorMessage := "Object 'nonexistent_table' does not exist"

	correction := engine.HandleError(ctx, sessionID, sql, errorCode, errorMessage)

	if correction == nil {
		t.Fatal("HandleError returned nil")
	}

	if correction.OriginalSQL != sql {
		t.Errorf("expected OriginalSQL %q, got %q", sql, correction.OriginalSQL)
	}

	// Verify error was recorded
	record := engine.GetErrorRecord(sessionID)
	if record == nil {
		t.Fatal("error record not stored")
	}

	if record.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", record.AttemptCount)
	}

	if record.SQL != sql {
		t.Errorf("expected SQL %q, got %q", sql, record.SQL)
	}
}

func TestHandleErrorWithAnalysis(t *testing.T) {
	tests := []struct {
		name         string
		errorType    string
		summary      string
		suggestions  []string
		wantContains string
	}{
		{
			name:         "syntax error",
			errorType:    "syntax_error",
			summary:      "Syntax error at position 10",
			suggestions:  []string{"Check parentheses", "Verify keyword usage"},
			wantContains: "syntax",
		},
		{
			name:         "table not found",
			errorType:    "table_not_found",
			summary:      "Table 'customers' does not exist",
			suggestions:  []string{"Verify table name", "Check database schema"},
			wantContains: "does not exist",
		},
		{
			name:         "column not found",
			errorType:    "column_not_found",
			summary:      "Column 'email' not found",
			suggestions:  []string{"Call GetTableSchema"},
			wantContains: "GetTableSchema",
		},
		{
			name:         "permission denied",
			errorType:    "permission_denied",
			summary:      "Insufficient permissions for SELECT",
			suggestions:  []string{"Check user grants"},
			wantContains: "permissions",
		},
		{
			name:         "timeout",
			errorType:    "timeout",
			summary:      "Query timeout after 30 seconds",
			suggestions:  []string{"Add WHERE clause", "Use indexes"},
			wantContains: "timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewGuardrailEngine()
			ctx := context.Background()

			sessionID := "test-session-" + tt.name
			sql := "SELECT * FROM table"

			analysis := &ErrorAnalysisInfo{
				ErrorType:   tt.errorType,
				Summary:     tt.summary,
				Suggestions: tt.suggestions,
			}

			correction := engine.HandleErrorWithAnalysis(ctx, sessionID, sql, analysis)

			if correction == nil {
				t.Fatal("HandleErrorWithAnalysis returned nil")
			}

			if correction.OriginalSQL != sql {
				t.Errorf("expected OriginalSQL %q, got %q", sql, correction.OriginalSQL)
			}

			// Verify error record was created
			record := engine.GetErrorRecord(sessionID)
			if record == nil {
				t.Fatal("error record not created")
			}

			if record.AttemptCount != 1 {
				t.Errorf("expected AttemptCount 1, got %d", record.AttemptCount)
			}

			if record.ErrorAnalysis == nil {
				t.Fatal("ErrorAnalysis not stored")
			}

			if record.ErrorAnalysis.ErrorType != tt.errorType {
				t.Errorf("expected ErrorType %q, got %q", tt.errorType, record.ErrorAnalysis.ErrorType)
			}
		})
	}
}

func TestHandleErrorMultipleAttempts(t *testing.T) {
	engine := NewGuardrailEngine()
	ctx := context.Background()

	sessionID := "test-session-retry"
	sql1 := "SELECT * FROM table1"
	sql2 := "SELECT * FROM table2"

	analysis1 := &ErrorAnalysisInfo{
		ErrorType:   "syntax_error",
		Summary:     "Syntax error in query 1",
		Suggestions: []string{"Fix syntax"},
	}

	analysis2 := &ErrorAnalysisInfo{
		ErrorType:   "table_not_found",
		Summary:     "Table not found in query 2",
		Suggestions: []string{"Verify table name"},
	}

	// First attempt
	_ = engine.HandleErrorWithAnalysis(ctx, sessionID, sql1, analysis1)

	record := engine.GetErrorRecord(sessionID)
	if record.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", record.AttemptCount)
	}

	if len(record.PreviousAttempts) != 1 {
		t.Errorf("expected 1 previous attempt, got %d", len(record.PreviousAttempts))
	}

	// Second attempt
	_ = engine.HandleErrorWithAnalysis(ctx, sessionID, sql2, analysis2)

	record = engine.GetErrorRecord(sessionID)
	if record.AttemptCount != 2 {
		t.Errorf("expected AttemptCount 2, got %d", record.AttemptCount)
	}

	if len(record.PreviousAttempts) != 2 {
		t.Errorf("expected 2 previous attempts, got %d", len(record.PreviousAttempts))
	}

	if record.PreviousAttempts[0] != sql1 {
		t.Errorf("expected first attempt %q, got %q", sql1, record.PreviousAttempts[0])
	}

	if record.PreviousAttempts[1] != sql2 {
		t.Errorf("expected second attempt %q, got %q", sql2, record.PreviousAttempts[1])
	}

	// Verify latest analysis is stored
	if record.ErrorAnalysis.ErrorType != analysis2.ErrorType {
		t.Errorf("expected latest ErrorType %q, got %q", analysis2.ErrorType, record.ErrorAnalysis.ErrorType)
	}
}

func TestClearErrorRecord(t *testing.T) {
	engine := NewGuardrailEngine()
	ctx := context.Background()

	sessionID := "test-session-clear"
	sql := "SELECT * FROM table"

	analysis := &ErrorAnalysisInfo{
		ErrorType: "syntax_error",
		Summary:   "Test error",
	}

	// Create error record
	_ = engine.HandleErrorWithAnalysis(ctx, sessionID, sql, analysis)

	// Verify it exists
	if engine.GetErrorRecord(sessionID) == nil {
		t.Fatal("error record not created")
	}

	// Clear it
	engine.ClearErrorRecord(sessionID)

	// Verify it's gone
	if engine.GetErrorRecord(sessionID) != nil {
		t.Error("error record not cleared")
	}
}

func TestInferErrorType(t *testing.T) {
	tests := []struct {
		name         string
		errorCode    string
		errorMessage string
		wantType     string
	}{
		{
			name:         "syntax error",
			errorCode:    "3706",
			errorMessage: "Syntax error: expected something between",
			wantType:     "syntax_error",
		},
		{
			name:         "table not found",
			errorCode:    "3807",
			errorMessage: "Object 'customers' does not exist",
			wantType:     "table_not_found",
		},
		{
			name:         "column not found",
			errorCode:    "3810",
			errorMessage: "Column 'email' not found in table",
			wantType:     "column_not_found",
		},
		{
			name:         "permission denied",
			errorCode:    "3523",
			errorMessage: "The user does not have SELECT access",
			wantType:     "permission_denied",
		},
		{
			name:         "timeout",
			errorCode:    "2643",
			errorMessage: "Query timeout exceeded",
			wantType:     "timeout",
		},
		{
			name:         "unknown error",
			errorCode:    "9999",
			errorMessage: "Some unknown error occurred",
			wantType:     "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType := InferErrorType(tt.errorCode, tt.errorMessage)

			if gotType != tt.wantType {
				t.Errorf("InferErrorType() = %q, want %q", gotType, tt.wantType)
			}
		})
	}
}

// TestConcurrentErrorHandling tests concurrent access to GuardrailEngine with -race detector
func TestConcurrentErrorHandling(t *testing.T) {
	engine := NewGuardrailEngine()
	ctx := context.Background()

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				sessionID := "session-" + string(rune(workerID))
				sql := "SELECT * FROM table"

				analysis := &ErrorAnalysisInfo{
					ErrorType: "syntax_error",
					Summary:   "Concurrent test error",
				}

				// Concurrent writes
				_ = engine.HandleErrorWithAnalysis(ctx, sessionID, sql, analysis)

				// Concurrent reads
				_ = engine.GetErrorRecord(sessionID)

				// Occasional clears
				if j%10 == 0 {
					engine.ClearErrorRecord(sessionID)
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentValidatorRegistration tests concurrent validator registration with -race detector
func TestConcurrentValidatorRegistration(t *testing.T) {
	engine := NewGuardrailEngine()

	const numGoroutines = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			validator := &mockValidator{
				name:   "validator-" + string(rune(workerID)),
				issues: []Issue{},
			}

			engine.RegisterValidator(validator)
		}(i)
	}

	wg.Wait()

	if len(engine.validators) != numGoroutines {
		t.Errorf("expected %d validators, got %d", numGoroutines, len(engine.validators))
	}
}

// TestConcurrentPreflightCheck tests concurrent preflight checks with -race detector
func TestConcurrentPreflightCheck(t *testing.T) {
	engine := NewGuardrailEngine()

	// Register validators
	engine.RegisterValidator(&mockValidator{
		name: "validator1",
		issues: []Issue{
			{Severity: "error", Message: "test error"},
		},
	})

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				_ = engine.PreflightCheck(context.Background(), "SELECT * FROM table")
			}
		}()
	}

	wg.Wait()
}
