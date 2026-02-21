// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStructuredContext(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	assert.Equal(t, "workflow-123", ctx.WorkflowContext.WorkflowID)
	assert.Equal(t, "npath-discovery", ctx.WorkflowContext.WorkflowType)
	assert.Equal(t, "1.0", ctx.WorkflowContext.SchemaVer)
	assert.NotZero(t, ctx.WorkflowContext.StartedAt)
	assert.NotNil(t, ctx.StageOutputs)
	assert.Empty(t, ctx.StageOutputs)
}

func TestAddStageOutput(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	output := StageOutput{
		StageID:     "td-expert-analytics-stage-1",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Outputs: map[string]interface{}{
			"discovered_databases": []interface{}{"demo", "data_scientist"},
		},
	}

	err := ctx.AddStageOutput("stage-1", output)
	require.NoError(t, err)

	retrieved, exists := ctx.GetStageOutput("stage-1")
	assert.True(t, exists)
	assert.Equal(t, "td-expert-analytics-stage-1", retrieved.StageID)
}

func TestAddStageOutput_ValidationErrors(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	tests := []struct {
		name        string
		output      StageOutput
		expectedErr string
	}{
		{
			name: "missing stage_id",
			output: StageOutput{
				Status: "completed",
			},
			expectedErr: "stage_id is required",
		},
		{
			name: "missing status",
			output: StageOutput{
				StageID: "stage-1",
			},
			expectedErr: "status is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ctx.AddStageOutput("stage-1", tt.output)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

// TestValidateTableReference_PreventHallucination demonstrates how the validation
// prevents the hallucination issue observed in nPath v3.6 execution.
//
// Observed Issue:
// - Stage 2 recommended: demo.bank_events
// - Stage 6 hallucinated: val_telco_churn.customer_churn
func TestValidateTableReference_PreventHallucination(t *testing.T) {
	ctx := NewStructuredContext("npath-v3.6-test", "npath-discovery")

	// Stage 1: Database discovery
	stage1Output := StageOutput{
		StageID:     "td-expert-analytics-stage-1",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Outputs: map[string]interface{}{
			"discovered_databases": []interface{}{"demo", "data_scientist", "financial"},
			"database_count":       3,
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{
					ToolName: "teradata_query",
					Parameters: map[string]interface{}{
						"query": "SELECT DatabaseName FROM DBC.Databases",
					},
					ResultSummary: "3 databases returned",
				},
			},
		},
	}
	err := ctx.AddStageOutput("stage-1", stage1Output)
	require.NoError(t, err)

	// Stage 2: Table discovery - recommends demo.bank_events
	stage2Output := StageOutput{
		StageID:     "td-expert-analytics-stage-2",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Inputs: map[string]interface{}{
			"databases_from": "stage-1",
			"databases":      []string{"demo", "data_scientist", "financial"},
		},
		Outputs: map[string]interface{}{
			"recommended_table": map[string]interface{}{
				"database":             "demo",
				"table":                "bank_events",
				"fully_qualified_name": "demo.bank_events",
				"reason":               "Sequential banking events with clear customer journey",
				"npath_config": map[string]interface{}{
					"partition_by":   "customer_id",
					"order_by":       "event_timestamp",
					"pattern_column": "event_type",
				},
			},
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{
					ToolName: "teradata_query",
					Parameters: map[string]interface{}{
						"query": "SELECT COUNT(*) FROM demo.bank_events",
					},
					ResultSummary: "130000 rows",
				},
			},
		},
	}
	err = ctx.AddStageOutput("stage-2", stage2Output)
	require.NoError(t, err)

	// Test Case 1: Valid reference (demo.bank_events) - should PASS
	t.Run("valid_reference", func(t *testing.T) {
		err := ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-2")
		assert.NoError(t, err, "Valid table reference should pass validation")
	})

	// Test Case 2: Hallucinated database (val_telco_churn) - should FAIL
	t.Run("hallucinated_database", func(t *testing.T) {
		err := ctx.ValidateTableReference("stage-6", "val_telco_churn", "customer_churn", "stage-2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database mismatch")
		assert.Contains(t, err.Error(), "val_telco_churn")
		assert.Contains(t, err.Error(), "demo")
	})

	// Test Case 3: Wrong table in correct database - should FAIL
	t.Run("wrong_table", func(t *testing.T) {
		err := ctx.ValidateTableReference("stage-6", "demo", "customer_churn", "stage-2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "table mismatch")
		assert.Contains(t, err.Error(), "customer_churn")
		assert.Contains(t, err.Error(), "bank_events")
	})

	// Test Case 4: Missing source stage - should FAIL
	t.Run("missing_source_stage", func(t *testing.T) {
		err := ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-99")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stage-99")
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestValidateDatabaseList(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	// Stage 1 discovers databases
	stage1Output := StageOutput{
		StageID:     "td-expert-analytics-stage-1",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Outputs: map[string]interface{}{
			"discovered_databases": []interface{}{"demo", "data_scientist", "financial"},
		},
	}
	err := ctx.AddStageOutput("stage-1", stage1Output)
	require.NoError(t, err)

	tests := []struct {
		name        string
		database    string
		shouldError bool
		errorMsg    string
	}{
		{
			name:        "valid_database",
			database:    "demo",
			shouldError: false,
		},
		{
			name:        "another_valid_database",
			database:    "data_scientist",
			shouldError: false,
		},
		{
			name:        "hallucinated_database",
			database:    "val_telco_churn",
			shouldError: true,
			errorMsg:    "not found in source stage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ctx.ValidateDatabaseList(tt.database, "stage-1")
			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetTargetTable(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	stage2Output := StageOutput{
		StageID:     "td-expert-analytics-stage-2",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Outputs: map[string]interface{}{
			"recommended_table": map[string]interface{}{
				"database": "demo",
				"table":    "bank_events",
			},
		},
	}
	err := ctx.AddStageOutput("stage-2", stage2Output)
	require.NoError(t, err)

	database, table, err := ctx.GetTargetTable("stage-2")
	require.NoError(t, err)
	assert.Equal(t, "demo", database)
	assert.Equal(t, "bank_events", table)
}

func TestGetTargetTable_Errors(t *testing.T) {
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	tests := []struct {
		name        string
		stageKey    string
		output      *StageOutput
		expectedErr string
	}{
		{
			name:        "missing_stage",
			stageKey:    "stage-99",
			expectedErr: "not found",
		},
		{
			name:     "missing_outputs",
			stageKey: "stage-2",
			output: &StageOutput{
				StageID: "stage-2",
				Status:  "completed",
				Outputs: nil,
			},
			expectedErr: "has no outputs",
		},
		{
			name:     "missing_recommended_table",
			stageKey: "stage-2",
			output: &StageOutput{
				StageID: "stage-2",
				Status:  "completed",
				Outputs: map[string]interface{}{
					"other_field": "value",
				},
			},
			expectedErr: "no 'recommended_table'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.output != nil {
				err := ctx.AddStageOutput(tt.stageKey, *tt.output)
				require.NoError(t, err)
			}

			database, table, err := ctx.GetTargetTable(tt.stageKey)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
			assert.Empty(t, database)
			assert.Empty(t, table)
		})
	}
}

func TestToJSON_FromJSON(t *testing.T) {
	// Create context with data
	ctx := NewStructuredContext("workflow-123", "npath-discovery")

	stage1Output := StageOutput{
		StageID:     "td-expert-analytics-stage-1",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Outputs: map[string]interface{}{
			"discovered_databases": []interface{}{"demo", "data_scientist"},
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{
					ToolName:      "teradata_query",
					Parameters:    map[string]interface{}{"query": "SELECT *"},
					ResultSummary: "2 rows",
				},
			},
		},
	}
	err := ctx.AddStageOutput("stage-1", stage1Output)
	require.NoError(t, err)

	// Serialize to JSON
	jsonStr, err := ctx.ToJSON()
	require.NoError(t, err)
	assert.NotEmpty(t, jsonStr)

	// Verify it's valid JSON
	var decoded map[string]interface{}
	err = json.Unmarshal([]byte(jsonStr), &decoded)
	require.NoError(t, err)

	// Deserialize back
	ctx2 := &StructuredContext{}
	err = ctx2.FromJSON(jsonStr)
	require.NoError(t, err)

	// Verify data matches
	assert.Equal(t, ctx.WorkflowContext.WorkflowID, ctx2.WorkflowContext.WorkflowID)
	assert.Equal(t, ctx.WorkflowContext.WorkflowType, ctx2.WorkflowContext.WorkflowType)
	assert.Len(t, ctx2.StageOutputs, 1)

	retrieved, exists := ctx2.GetStageOutput("stage-1")
	assert.True(t, exists)
	assert.Equal(t, "td-expert-analytics-stage-1", retrieved.StageID)
}

// TestRealWorldScenario_nPathV36 simulates the actual nPath v3.6 workflow
// with structured context validation at each stage
func TestRealWorldScenario_nPathV36(t *testing.T) {
	ctx := NewStructuredContext("npath-v3.6-real", "npath-discovery")

	// Stage 1: Discover databases
	stage1Output := StageOutput{
		StageID:     "td-expert-analytics-stage-1",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(5 * time.Second),
		Outputs: map[string]interface{}{
			"discovered_databases": []interface{}{"demo", "data_scientist", "financial"},
			"database_count":       3,
		},
		Evidence: Evidence{
			QueriesExecuted: []string{
				"SELECT DatabaseName FROM DBC.Databases WHERE ...",
			},
		},
	}
	err := ctx.AddStageOutput("stage-1", stage1Output)
	require.NoError(t, err, "Stage 1 should complete successfully")

	// Stage 2: Find candidate tables
	stage2Output := StageOutput{
		StageID:     "td-expert-analytics-stage-2",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(45 * time.Second),
		Inputs: map[string]interface{}{
			"databases_from": "stage-1",
		},
		Outputs: map[string]interface{}{
			"candidate_tables": []interface{}{
				map[string]interface{}{
					"database":  "demo",
					"table":     "bank_events",
					"row_count": 130000,
				},
				map[string]interface{}{
					"database":  "data_scientist",
					"table":     "telco_events",
					"row_count": 1500000,
				},
			},
			"recommended_table": map[string]interface{}{
				"database":             "demo",
				"table":                "bank_events",
				"fully_qualified_name": "demo.bank_events",
				"reason":               "Banking application funnel with clear event sequences",
				"npath_config": map[string]interface{}{
					"partition_by":   "customer_id",
					"order_by":       "event_timestamp",
					"pattern_column": "event_type",
				},
			},
		},
		Evidence: Evidence{
			QueriesExecuted: []string{
				"SELECT COUNT(*) FROM demo.bank_events",
				"SELECT TOP 3 * FROM demo.bank_events",
			},
		},
	}
	err = ctx.AddStageOutput("stage-2", stage2Output)
	require.NoError(t, err, "Stage 2 should complete successfully")

	// Stage 6: Data profiling - MUST validate table reference
	t.Run("stage_6_validation", func(t *testing.T) {
		// Extract target table from Stage 2
		database, table, err := ctx.GetTargetTable("stage-2")
		require.NoError(t, err)
		assert.Equal(t, "demo", database)
		assert.Equal(t, "bank_events", table)

		// Validate that Stage 6 is using the correct table
		err = ctx.ValidateTableReference("stage-6", database, table, "stage-2")
		assert.NoError(t, err, "Stage 6 should reference demo.bank_events from Stage 2")

		// This would FAIL (as it did in the real execution)
		err = ctx.ValidateTableReference("stage-6", "val_telco_churn", "customer_churn", "stage-2")
		assert.Error(t, err, "Stage 6 should NOT be able to reference hallucinated table")
		assert.Contains(t, err.Error(), "database mismatch")
	})

	// Stage 6 completes with correct table
	stage6Output := StageOutput{
		StageID:     "td-expert-analytics-stage-6",
		Status:      "completed",
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(30 * time.Second),
		Inputs: map[string]interface{}{
			"target_table_from": "stage-2",
			"target_table": map[string]interface{}{
				"database": "demo",
				"table":    "bank_events",
			},
		},
		Outputs: map[string]interface{}{
			"data_profile": map[string]interface{}{
				"total_rows":            130000,
				"distinct_entities":     20500,
				"avg_events_per_entity": 6.3,
				"date_range": map[string]interface{}{
					"min": "2023-01-01",
					"max": "2023-12-31",
				},
				"top_events": []interface{}{
					map[string]interface{}{"event": "login", "count": 45000},
					map[string]interface{}{"event": "view_balance", "count": 38000},
					map[string]interface{}{"event": "transfer", "count": 25000},
					map[string]interface{}{"event": "pay_bill", "count": 15000},
					map[string]interface{}{"event": "logout", "count": 7000},
				},
			},
		},
		Evidence: Evidence{
			QueriesExecuted: []string{
				"SELECT COUNT(*), COUNT(DISTINCT customer_id) FROM demo.bank_events",
				"SELECT event_type, COUNT(*) FROM demo.bank_events GROUP BY 1 ORDER BY 2 DESC TOP 5",
			},
		},
	}
	err = ctx.AddStageOutput("stage-6", stage6Output)
	require.NoError(t, err, "Stage 6 should complete with validated table")

	// Verify final context can be serialized
	jsonStr, err := ctx.ToJSON()
	require.NoError(t, err)
	assert.Contains(t, jsonStr, "demo.bank_events")
	assert.NotContains(t, jsonStr, "val_telco_churn", "Hallucinated table should not appear in context")
}

// TestValidateToolExecutions_PreventActionHallucination verifies that agents actually execute tools
func TestValidateToolExecutions_PreventActionHallucination(t *testing.T) {
	ctx := NewStructuredContext("workflow-test", "tool-validation")

	// Stage 10 with NO tool executions (action hallucination)
	stage10Hallucinated := StageOutput{
		StageID: "td-expert-insights-stage-10",
		Status:  "completed",
		Outputs: map[string]interface{}{
			"report_path": "/tmp/report.html",
		},
		Evidence: Evidence{
			ToolCalls:       []ToolCall{}, // ZERO tools executed!
			QueriesExecuted: []string{},
		},
	}
	err := ctx.AddStageOutput("stage-10", stage10Hallucinated)
	require.NoError(t, err)

	// Validation should FAIL - zero tools executed
	err = ctx.ValidateToolExecutions("stage-10", []string{"generate_visualization"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "executed zero tools")
	assert.Contains(t, err.Error(), "action hallucination")

	// Stage 10 with correct tool executions
	stage10Valid := StageOutput{
		StageID: "td-expert-insights-stage-10",
		Status:  "completed",
		Outputs: map[string]interface{}{
			"report_path": "/tmp/report.html",
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{
					ToolName: "generate_visualization",
					Parameters: map[string]interface{}{
						"data_query": "SELECT...",
						"chart_type": "bar",
					},
					ResultSummary: "Chart generated",
				},
				{
					ToolName:      "group_by_query",
					Parameters:    map[string]interface{}{"query": "SELECT..."},
					ResultSummary: "5 rows returned",
				},
			},
		},
	}
	err = ctx.AddStageOutput("stage-10-valid", stage10Valid)
	require.NoError(t, err)

	// Validation should PASS - tools were executed
	err = ctx.ValidateToolExecutions("stage-10-valid", []string{"generate_visualization", "group_by_query"})
	assert.NoError(t, err)

	// Validation should FAIL if required tool missing
	err = ctx.ValidateToolExecutions("stage-10-valid", []string{"generate_visualization", "top_n_query"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required tool executions")
	assert.Contains(t, err.Error(), "top_n_query")
}

// TestValidateFileCreation_PreventFakeFiles verifies that claimed file outputs actually exist
func TestValidateFileCreation_PreventFakeFiles(t *testing.T) {
	ctx := NewStructuredContext("workflow-test", "file-validation")

	// Create a temporary file for testing
	tmpFile, err := os.CreateTemp("", "test-report-*.html")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }() // Clean up after test
	_ = tmpFile.Close()

	// Stage 10 claiming non-existent file (hallucination)
	stage10Hallucinated := StageOutput{
		StageID: "td-expert-insights-stage-10",
		Status:  "completed",
		Outputs: map[string]interface{}{
			"report_path": "/tmp/banking_customer_journey_report.html", // DOES NOT EXIST
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{ToolName: "generate_visualization", ResultSummary: "Chart generated"},
			},
		},
	}
	err = ctx.AddStageOutput("stage-10-fake", stage10Hallucinated)
	require.NoError(t, err)

	// Validation should FAIL - file doesn't exist
	err = ctx.ValidateFileCreation("stage-10-fake", "report_path")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file does not exist")
	assert.Contains(t, err.Error(), "action hallucination detected")

	// Stage 10 with actual file creation
	stage10Valid := StageOutput{
		StageID: "td-expert-insights-stage-10",
		Status:  "completed",
		Outputs: map[string]interface{}{
			"report_path": tmpFile.Name(), // REAL FILE
		},
		Evidence: Evidence{
			ToolCalls: []ToolCall{
				{ToolName: "generate_visualization", ResultSummary: "Chart generated"},
			},
		},
	}
	err = ctx.AddStageOutput("stage-10-valid", stage10Valid)
	require.NoError(t, err)

	// Validation should PASS - file exists
	err = ctx.ValidateFileCreation("stage-10-valid", "report_path")
	assert.NoError(t, err)
}

// TestValidateOutputStructure_DeterministicValidation tests deterministic JSON validation
func TestValidateOutputStructure_DeterministicValidation(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		shouldError bool
		errorMsg    string
	}{
		{
			name: "valid_json_with_markdown",
			output: `Here's my analysis:

` + "```json" + `
{
  "stage_outputs": {
    "stage-1": {
      "stage_id": "test-agent",
      "status": "completed",
      "outputs": {
        "databases": ["demo", "test"]
      },
      "evidence": {
        "tool_calls": [
          {"tool_name": "execute_sql", "result_summary": "3 rows"}
        ]
      }
    }
  }
}
` + "```",
			shouldError: false,
		},
		{
			name: "valid_raw_json",
			output: `{
  "stage_outputs": {
    "stage-2": {
      "stage_id": "test-agent",
      "status": "completed",
      "outputs": {"table": "bank_events"}
    }
  }
}`,
			shouldError: false,
		},
		{
			name:        "no_json_in_output",
			output:      "This is just plain text without any JSON",
			shouldError: true,
			errorMsg:    "no JSON object found",
		},
		{
			name: "invalid_json_syntax",
			output: `{
  "stage_outputs": {
    "stage-1": {
      "stage_id": "test",
      "status": "completed",  // trailing comma
    }
  }
}`,
			shouldError: true,
			errorMsg:    "invalid JSON",
		},
		{
			name: "missing_stage_outputs",
			output: `{
  "results": {
    "stage-1": {"data": "test"}
  }
}`,
			shouldError: true,
			// When stage_outputs is missing, the code assumes v3.9 flat format
			// and checks for stage_id first
			errorMsg: "missing required field: 'stage_id'",
		},
		{
			name: "missing_stage_id",
			output: `{
  "stage_outputs": {
    "stage-1": {
      "status": "completed",
      "outputs": {}
    }
  }
}`,
			shouldError: true,
			errorMsg:    "missing required field: 'stage_id'",
		},
		{
			name: "missing_status",
			output: `{
  "stage_outputs": {
    "stage-1": {
      "stage_id": "test",
      "outputs": {}
    }
  }
}`,
			shouldError: true,
			errorMsg:    "missing required field: 'status'",
		},
		{
			name: "missing_outputs",
			output: `{
  "stage_outputs": {
    "stage-1": {
      "stage_id": "test",
      "status": "completed"
    }
  }
}`,
			shouldError: true,
			errorMsg:    "missing required field: 'outputs'",
		},
		{
			name: "invalid_tool_calls_type",
			output: `{
  "stage_outputs": {
    "stage-1": {
      "stage_id": "test",
      "status": "completed",
      "outputs": {},
      "evidence": {
        "tool_calls": "not an array"
      }
    }
  }
}`,
			shouldError: true,
			errorMsg:    "tool_calls must be an array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOutputStructure(tt.output)
			if tt.shouldError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
