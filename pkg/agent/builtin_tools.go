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
package agent

import (
	"context"
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// GetErrorDetailsTool is a built-in tool that fetches complete error information
// for a previously failed tool execution.
//
// This tool is automatically registered on all agents to support the error
// submission channel pattern where error summaries are sent to the LLM by default,
// and full details are fetched on demand.
type GetErrorDetailsTool struct {
	store ErrorStore
}

// NewGetErrorDetailsTool creates a new GetErrorDetailsTool.
func NewGetErrorDetailsTool(store ErrorStore) *GetErrorDetailsTool {
	return &GetErrorDetailsTool{
		store: store,
	}
}

// Name returns the tool name.
func (t *GetErrorDetailsTool) Name() string {
	return "get_error_details"
}

// Description returns the tool description for the LLM.
func (t *GetErrorDetailsTool) Description() string {
	return `Fetches complete error information for a previously failed tool execution.

Use this when you need the full error message, stack trace, or detailed debugging information
that was omitted from the error summary. Most errors can be handled with just the summary -
only use this for complex debugging scenarios.

Input:
- error_id: The error ID from a tool failure message (e.g., "err_20241121_230334_abc123")

Output:
- error_id: The error ID
- timestamp: When the error occurred (RFC3339 format)
- tool_name: Name of the tool that failed
- short_summary: Brief summary of the error
- raw_error: Complete original error message including stack traces

Example usage:
When you see: "Tool 'teradata_sample_table' failed: Code 3523: Permission denied [Error ID: err_20241121_abc123]"
You can call: get_error_details(error_id="err_20241121_abc123") to get the full stack trace.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *GetErrorDetailsTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"error_id": {
				Type:        "string",
				Description: "The error ID from a failed tool execution (e.g., 'err_20241121_abc123')",
			},
		},
		Required: []string{"error_id"},
	}
}

// Execute fetches the error details from the error store.
func (t *GetErrorDetailsTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Validate input
	errorID, ok := input["error_id"].(string)
	if !ok {
		// Debug: Show what was actually passed
		actualType := "nil"
		actualValue := "nil"
		if val, exists := input["error_id"]; exists {
			actualType = fmt.Sprintf("%T", val)
			actualValue = fmt.Sprintf("%v", val)
		}

		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: fmt.Sprintf("error_id must be a string, got type=%s value=%s", actualType, actualValue),
			},
		}, nil
	}

	if errorID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "error_id cannot be empty",
			},
		}, nil
	}

	// Fetch error from store
	stored, err := t.store.Get(ctx, errorID)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Error %s not found. It may have been deleted or the ID is incorrect.", errorID),
			},
		}, nil
	}

	// Return full error details
	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"error_id":      stored.ID,
			"timestamp":     stored.Timestamp.Format("2006-01-02T15:04:05Z07:00"), // RFC3339
			"tool_name":     stored.ToolName,
			"short_summary": stored.ShortSummary,
			"raw_error":     string(stored.RawError),
		},
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *GetErrorDetailsTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure GetErrorDetailsTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*GetErrorDetailsTool)(nil)

// GetToolResultTool is a built-in tool that retrieves large tool results stored in shared memory.
//
// When a tool execution produces a large result (>1000 tokens), it's stored in shared memory
// with a reference ID. The LLM receives a summary + reference ID, and can use this tool to
// retrieve the full result when needed for detailed analysis.
type GetToolResultTool struct {
	store *storage.SharedMemoryStore
}

// NewGetToolResultTool creates a new GetToolResultTool.
func NewGetToolResultTool(store *storage.SharedMemoryStore) *GetToolResultTool {
	return &GetToolResultTool{
		store: store,
	}
}

// Name returns the tool name.
func (t *GetToolResultTool) Name() string {
	return "get_tool_result"
}

// Description returns the tool description for the LLM.
func (t *GetToolResultTool) Description() string {
	return `Retrieves the complete data from a previously executed tool that returned a large result.

When a tool returns more than 1000 tokens of data, the full result is stored in shared memory
and you receive a summary with a reference ID. Use this tool to retrieve the complete data
when you need it for detailed analysis.

Input:
- reference_id: The reference ID from a tool result message (e.g., "ref_abc123...")

Output:
- The complete tool result data

Example usage:
When you see:
  📊 1500 rows returned • 12 columns
  📎 Full result stored in memory (ID: ref_abc123...)
  💡 Use get_tool_result(reference_id="ref_abc123...") to retrieve complete data

You can call: get_tool_result(reference_id="ref_abc123...") to get all 1500 rows.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *GetToolResultTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"reference_id": {
				Type:        "string",
				Description: "The reference ID from a large tool result (e.g., 'ref_abc123...')",
			},
		},
		Required: []string{"reference_id"},
	}
}

// Execute retrieves the data from shared memory.
func (t *GetToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Validate input
	refID, ok := input["reference_id"].(string)
	if !ok {
		// Debug: Show what was actually passed
		actualType := "nil"
		actualValue := "nil"
		if val, exists := input["reference_id"]; exists {
			actualType = fmt.Sprintf("%T", val)
			actualValue = fmt.Sprintf("%v", val)
		}

		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: fmt.Sprintf("reference_id must be a string, got type=%s value=%s", actualType, actualValue),
			},
		}, nil
	}

	if refID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "reference_id cannot be empty",
			},
		}, nil
	}

	// Parse DataRef format if provided: "DataRef[ID, LOCATION, SIZE]"
	// Extract just the ID portion
	if strings.HasPrefix(refID, "DataRef[") {
		// Extract ID from "DataRef[117bcbb..., MEMORY, 1301891 bytes (compressed)]"
		parts := strings.SplitN(strings.TrimPrefix(refID, "DataRef["), ",", 2)
		if len(parts) > 0 {
			refID = strings.TrimSpace(parts[0])
		}
	}

	// Create a minimal DataReference to fetch the data
	// We only need the ID field for retrieval
	ref := &loomv1.DataReference{
		Id:       refID,
		Location: loomv1.StorageLocation_STORAGE_LOCATION_MEMORY,
	}

	// Fetch data from store
	data, err := t.store.Get(ref)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Reference %s not found. It may have expired or the ID is incorrect.", refID),
			},
		}, nil
	}

	// Return the complete data as a string
	// The agent can then parse it as JSON or use it directly
	return &shuttle.Result{
		Success: true,
		Data:    string(data),
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *GetToolResultTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure GetToolResultTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*GetToolResultTool)(nil)

// QueryToolResultTool is a built-in tool that queries SQL results stored in queryable tables.
//
// When a tool returns large SQL results (rows/columns), they're stored in a SQLite table
// that can be queried with SQL. This prevents context blowout by allowing the LLM to
// filter/aggregate data without loading everything.
type QueryToolResultTool struct {
	store *storage.SQLResultStore
}

// NewQueryToolResultTool creates a new QueryToolResultTool.
func NewQueryToolResultTool(store *storage.SQLResultStore) *QueryToolResultTool {
	return &QueryToolResultTool{
		store: store,
	}
}

// Name returns the tool name.
func (t *QueryToolResultTool) Name() string {
	return "query_tool_result"
}

// Description returns the tool description for the LLM.
func (t *QueryToolResultTool) Description() string {
	return `Query large SQL results without loading all rows into context.

When a tool returns SQL data with many rows (databases, tables, query results), the data
is stored in a queryable SQLite table. Use this tool to filter, aggregate, or analyze
the data using SQL queries.

Examples:
- COUNT rows: SELECT COUNT(*) as count FROM results
- Filter: SELECT * FROM results WHERE column_name LIKE 'pattern%' LIMIT 20
- Aggregate: SELECT column1, COUNT(*) as count FROM results GROUP BY column1
- Sample: SELECT * FROM results LIMIT 10

The table name is always "results" and column names match the original SQL result columns.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *QueryToolResultTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"reference_id": {
				Type:        "string",
				Description: "The reference ID from the large SQL result (e.g., the ref ID from the summary)",
			},
			"query": {
				Type:        "string",
				Description: "SQL query to execute against the stored result. Table name is 'results'. Use SELECT to filter/aggregate the data.",
			},
		},
		Required: []string{"reference_id", "query"},
	}
}

// Execute runs a SQL query against stored result data.
func (t *QueryToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Validate inputs
	refID, ok := input["reference_id"].(string)
	if !ok || refID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "reference_id must be a non-empty string",
			},
		}, nil
	}

	query, ok := input["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "query must be a non-empty string",
			},
		}, nil
	}

	// Get metadata to show table info
	meta, err := t.store.GetMetadata(refID)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Result %s not found. It may have expired.", refID),
			},
		}, nil
	}

	// Replace "results" table name with actual table name
	actualQuery := strings.ReplaceAll(query, "FROM results", fmt.Sprintf("FROM %s", meta.TableName))
	actualQuery = strings.ReplaceAll(actualQuery, "from results", fmt.Sprintf("FROM %s", meta.TableName))
	actualQuery = strings.ReplaceAll(actualQuery, "from RESULTS", fmt.Sprintf("FROM %s", meta.TableName))
	actualQuery = strings.ReplaceAll(actualQuery, "FROM RESULTS", fmt.Sprintf("FROM %s", meta.TableName))

	// Execute query
	result, err := t.store.Query(refID, actualQuery)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "query_failed",
				Message:    fmt.Sprintf("Query failed: %v", err),
				Suggestion: "Check your SQL syntax. Table name is 'results', columns are: " + strings.Join(meta.Columns, ", "),
			},
		}, nil
	}

	// Return result in same format as original SQL results
	return &shuttle.Result{
		Success: true,
		Data:    result,
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *QueryToolResultTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure QueryToolResultTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*QueryToolResultTool)(nil)
