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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/session"
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

// GetToolResultTool retrieves METADATA about large tool results.
// BREAKING CHANGE in v1.0.1: Now returns ONLY metadata, never full data.
// Use query_tool_result to retrieve filtered/paginated data.
//
// This implements progressive disclosure - agents inspect metadata before retrieving data,
// preventing context blowout from accidentally loading 50MB results.
type GetToolResultTool struct {
	memoryStore *storage.SharedMemoryStore
	sqlStore    storage.ResultStore
}

// NewGetToolResultTool creates a new GetToolResultTool.
func NewGetToolResultTool(memoryStore *storage.SharedMemoryStore, sqlStore storage.ResultStore) *GetToolResultTool {
	return &GetToolResultTool{
		memoryStore: memoryStore,
		sqlStore:    sqlStore,
	}
}

// Name returns the tool name.
func (t *GetToolResultTool) Name() string {
	return "get_tool_result"
}

// Description returns the tool description for the LLM.
func (t *GetToolResultTool) Description() string {
	return `Retrieves METADATA about large tool results stored in shared memory.

IMPORTANT: This tool returns ONLY metadata (size, schema, preview), never full data.
Use query_tool_result to retrieve filtered/paginated data based on this metadata.

Input:
- reference_id: The reference ID from a tool result message (e.g., "ref_abc123...")

Output:
- reference_id: The reference ID
- content_type: MIME type (e.g., "application/json", "text/csv")
- data_type: Structure type (e.g., "json_array", "sql_result")
- size_bytes: Total size in bytes
- estimated_tokens: Rough token estimate
- schema: Data structure (columns, fields, item count)
- preview: Sample data (first 5 + last 5 items)
- retrieval_hints: Suggestions for querying the data

Example workflow:
1. get_tool_result(reference_id="ref_abc123") → Returns metadata + preview
2. Analyze preview to understand data structure
3. query_tool_result(reference_id="ref_abc123", sql="SELECT * WHERE score > 90") → Get filtered data`
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

// Execute retrieves metadata from either shared memory or SQL store.
func (t *GetToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Validate input
	refID, ok := input["reference_id"].(string)
	if !ok || refID == "" {
		actualType := "nil"
		if val, exists := input["reference_id"]; exists {
			actualType = fmt.Sprintf("%T", val)
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: fmt.Sprintf("reference_id must be a non-empty string, got type=%s", actualType),
			},
		}, nil
	}

	// Parse DataRef format: "DataRef[ID, LOCATION, SIZE]"
	location := loomv1.StorageLocation_STORAGE_LOCATION_MEMORY
	if strings.HasPrefix(refID, "DataRef[") {
		parts := strings.SplitN(strings.TrimPrefix(refID, "DataRef["), ",", 3)
		if len(parts) >= 2 {
			refID = strings.TrimSpace(parts[0])
			locStr := strings.TrimSpace(parts[1])
			if strings.Contains(locStr, "DATABASE") {
				location = loomv1.StorageLocation_STORAGE_LOCATION_DATABASE
			}
		}
	}

	// Route to appropriate store based on location
	var metadata interface{}
	var err error

	switch location {
	case loomv1.StorageLocation_STORAGE_LOCATION_DATABASE:
		if t.sqlStore == nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "store_not_available",
					Message: "SQL result store not configured",
				},
			}, nil
		}
		metadata, err = t.sqlStore.GetMetadata(ctx, refID)

	default: // MEMORY or DISK
		if t.memoryStore == nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "store_not_available",
					Message: "Shared memory store not configured",
				},
			}, nil
		}
		ref := &loomv1.DataReference{
			Id:       refID,
			Location: location,
		}
		metadata, err = t.memoryStore.GetMetadata(ref)
	}

	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Reference %s not found: %v", refID, err),
			},
		}, nil
	}

	// Format metadata response with retrieval hints
	response := formatMetadataResponse(metadata, refID)

	return &shuttle.Result{
		Success: true,
		Data:    response,
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *GetToolResultTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// formatMetadataResponse converts metadata to LLM-friendly format with hints.
func formatMetadataResponse(metadata interface{}, refID string) map[string]interface{} {
	switch meta := metadata.(type) {
	case *storage.DataMetadata:
		return map[string]interface{}{
			"reference_id":     refID,
			"content_type":     meta.ContentType,
			"data_type":        meta.DataType,
			"size_bytes":       meta.SizeBytes,
			"estimated_tokens": meta.EstimatedTokens,
			"schema":           meta.Schema,
			"preview":          meta.Preview,
			"created_at":       meta.CreatedAt.Format(time.RFC3339),
			"retrieval_hints":  generateRetrievalHints(meta),
		}

	case *storage.SQLResultMetadata:
		return map[string]interface{}{
			"reference_id":     refID,
			"content_type":     "application/sql",
			"data_type":        "sql_result",
			"size_bytes":       meta.SizeBytes,
			"estimated_tokens": meta.SizeBytes / 4,
			"schema": map[string]interface{}{
				"type":         "table",
				"row_count":    meta.RowCount,
				"column_count": meta.ColumnCount,
				"columns":      meta.Columns,
			},
			"preview":         meta.Preview,
			"created_at":      meta.StoredAt.Format(time.RFC3339),
			"retrieval_hints": generateSQLRetrievalHints(meta),
		}

	default:
		return map[string]interface{}{
			"reference_id": refID,
			"error":        "Unknown metadata type",
		}
	}
}

// generateRetrievalHints creates actionable hints for retrieving data.
func generateRetrievalHints(meta *storage.DataMetadata) []string {
	hints := []string{}

	switch meta.DataType {
	case "json_object":
		// json_object doesn't support direct retrieval - data is too large for context
		// Agent should understand structure from schema/preview and extract needed fields
		hints = append(hints,
			"⚠️ Large json_object cannot be retrieved directly",
			"💡 Review the preview and schema to understand structure",
		)
		if meta.Schema != nil && len(meta.Schema.Fields) > 0 {
			fieldNames := make([]string, 0, len(meta.Schema.Fields))
			for _, field := range meta.Schema.Fields {
				fieldNames = append(fieldNames, field.Name)
			}
			hints = append(hints, fmt.Sprintf("📋 Available fields: %s", strings.Join(fieldNames, ", ")))
		}
		// Add warning about size
		if meta.SizeBytes > 100000 { // >100KB
			hints = append(hints, fmt.Sprintf("⚠️ Object size: %d bytes - too large for context window", meta.SizeBytes))
		}

	case "json_array":
		hints = append(hints,
			fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', offset=0, limit=100) for pagination", meta.ID),
			fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...') for SQL filtering", meta.ID),
		)
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			hints = append(hints, "⚠️ Large dataset - use filtering to avoid context blowout")
		}

	case "csv":
		hints = append(hints,
			fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...') for SQL queries", meta.ID),
			"💡 Auto-converts CSV to queryable SQLite table",
		)

	case "text":
		hints = append(hints,
			fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', offset=0, limit=100) for line-based pagination", meta.ID),
			fmt.Sprintf("💡 Total lines: %d - paginate to avoid loading all at once", meta.Schema.ItemCount),
		)
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			hints = append(hints, "⚠️ Large text file - use pagination to avoid context blowout")
		}
	}

	return hints
}

// generateSQLRetrievalHints creates hints for SQL results.
func generateSQLRetrievalHints(meta *storage.SQLResultMetadata) []string {
	hints := []string{
		fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...') to filter", meta.ID),
		fmt.Sprintf("💡 Use query_tool_result(reference_id='%s', sql='SELECT * FROM results LIMIT 100') for first 100 rows", meta.ID),
	}

	if meta.RowCount > 1000 {
		hints = append(hints, "⚠️ Large result set - use WHERE clause to filter or LIMIT to paginate")
	}

	hints = append(hints, fmt.Sprintf("📊 Columns: %s", strings.Join(meta.Columns, ", ")))

	return hints
}

// Ensure GetToolResultTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*GetToolResultTool)(nil)

// QueryToolResultTool queries large results with filtering/pagination.
// Enhanced in v1.0.1: Now supports non-SQL data (JSON arrays) via pagination.
//
// For SQL results: Use SQL queries to filter/aggregate
// For JSON arrays: Use offset/limit for pagination (SQL support coming in Phase 4.5)
// For CSV data: SQL queries coming in Phase 4.5
type QueryToolResultTool struct {
	sqlStore    storage.ResultStore
	memoryStore *storage.SharedMemoryStore
}

// NewQueryToolResultTool creates a new QueryToolResultTool.
func NewQueryToolResultTool(sqlStore storage.ResultStore, memoryStore *storage.SharedMemoryStore) *QueryToolResultTool {
	return &QueryToolResultTool{
		sqlStore:    sqlStore,
		memoryStore: memoryStore,
	}
}

// Name returns the tool name.
func (t *QueryToolResultTool) Name() string {
	return "query_tool_result"
}

// Description returns the tool description for the LLM.
func (t *QueryToolResultTool) Description() string {
	return `Query large results with SQL filtering and pagination.

For SQL results (DATABASE location):
- Use SQL queries to filter/aggregate: sql="SELECT * FROM results WHERE score > 90"
- Table name is always "results"

For JSON objects (MEMORY/DISK location):
- No parameters needed: query_tool_result(reference_id="ref_123")
- Returns the complete object structure
- Use for discovery results, metadata, and structured configuration

For JSON arrays (MEMORY/DISK location):
- Simple pagination: offset=0, limit=100
- SQL queries: sql="SELECT * FROM results WHERE field > value" (auto-converts to SQLite table)
- Nested objects stored as JSON strings

For plain text (MEMORY/DISK location):
- Line-based pagination: offset=0, limit=100
- Returns array of lines with line numbers
- Use for log files, large text documents, configuration files

For CSV data:
- SQL queries: sql="SELECT * FROM results WHERE column = 'value'" (auto-converts to SQLite table)
- First row treated as headers

Auto-conversion to SQLite:
- JSON arrays and CSV are automatically converted to queryable tables
- Conversion is temporary and cleaned up after use
- Use standard SQL syntax for filtering/aggregation

Examples:
- JSON object: query_tool_result(reference_id="ref_123")
- SQL on JSON: query_tool_result(reference_id="ref_123", sql="SELECT * FROM results WHERE score > 90")
- SQL on CSV: query_tool_result(reference_id="ref_123", sql="SELECT COUNT(*) FROM results GROUP BY category")
- JSON array pagination: query_tool_result(reference_id="ref_123", offset=0, limit=100)
- Text pagination: query_tool_result(reference_id="ref_123", offset=0, limit=50)
- Aggregate: query_tool_result(reference_id="ref_123", sql="SELECT AVG(score) FROM results")`
}

// InputSchema returns the JSON schema for the tool input.
func (t *QueryToolResultTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"reference_id": {
				Type:        "string",
				Description: "The reference ID from the large result",
			},
			"sql": {
				Type:        "string",
				Description: "SQL query to execute (table name is 'results'). For SQL results or queryable data.",
			},
			"offset": {
				Type:        "integer",
				Description: "Skip first N items (for pagination). Use with limit.",
			},
			"limit": {
				Type:        "integer",
				Description: "Return at most N items (for pagination). Use with offset.",
			},
		},
		Required: []string{"reference_id"},
	}
}

// Execute queries stored data with routing based on storage location.
func (t *QueryToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Validate reference_id
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

	// Parse DataRef format to determine location
	location := loomv1.StorageLocation_STORAGE_LOCATION_MEMORY
	if strings.HasPrefix(refID, "DataRef[") {
		parts := strings.SplitN(strings.TrimPrefix(refID, "DataRef["), ",", 3)
		if len(parts) >= 2 {
			refID = strings.TrimSpace(parts[0])
			locStr := strings.TrimSpace(parts[1])
			if strings.Contains(locStr, "DATABASE") {
				location = loomv1.StorageLocation_STORAGE_LOCATION_DATABASE
			}
		}
	}

	// Route based on location
	switch location {
	case loomv1.StorageLocation_STORAGE_LOCATION_DATABASE:
		return t.querySQLResult(ctx, refID, input)
	default: // MEMORY or DISK
		return t.queryMemoryData(ctx, refID, input)
	}
}

// querySQLResult handles SQL results (existing logic).
func (t *QueryToolResultTool) querySQLResult(ctx context.Context, refID string, input map[string]interface{}) (*shuttle.Result, error) {
	if t.sqlStore == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "store_not_available",
				Message: "SQL result store not configured",
			},
		}, nil
	}

	// Get metadata
	meta, err := t.sqlStore.GetMetadata(ctx, refID)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Result %s not found", refID),
			},
		}, nil
	}

	// Get SQL query or build pagination query
	var query string
	if sqlQuery, ok := input["sql"].(string); ok {
		// Replace "results" with actual table name
		query = strings.ReplaceAll(sqlQuery, "FROM results", fmt.Sprintf("FROM %s", meta.TableName))
		query = strings.ReplaceAll(query, "from results", fmt.Sprintf("FROM %s", meta.TableName))
	} else if offset, hasOffset := input["offset"]; hasOffset {
		// Simple pagination
		limit := 100 // default
		if l, ok := input["limit"].(float64); ok {
			limit = int(l)
		}
		offsetInt := 0
		if o, ok := offset.(float64); ok {
			offsetInt = int(o)
		}
		query = fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", meta.TableName, limit, offsetInt)
	} else {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "Must provide 'sql' query or 'offset'/'limit' for pagination",
			},
		}, nil
	}

	// Execute query
	result, err := t.sqlStore.Query(ctx, refID, query)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "query_failed",
				Message:    fmt.Sprintf("Query failed: %v", err),
				Suggestion: "Check your SQL syntax. Columns: " + strings.Join(meta.Columns, ", "),
			},
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
	}, nil
}

// queryMemoryData handles non-SQL data (JSON, CSV, text).
func (t *QueryToolResultTool) queryMemoryData(ctx context.Context, refID string, input map[string]interface{}) (*shuttle.Result, error) {
	if t.memoryStore == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "store_not_available",
				Message: "Shared memory store not configured",
			},
		}, nil
	}

	// Get metadata to determine data type
	ref := &loomv1.DataReference{
		Id:       refID,
		Location: loomv1.StorageLocation_STORAGE_LOCATION_MEMORY,
	}
	meta, err := t.memoryStore.GetMetadata(ref)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_found",
				Message: fmt.Sprintf("Reference %s not found", refID),
			},
		}, nil
	}

	// Check query type
	if sqlQuery, ok := input["sql"].(string); ok {
		// SQL query on non-SQL data - convert to temp table
		return t.convertAndQuery(ctx, ref, meta, sqlQuery)
	}

	if _, hasOffset := input["offset"]; hasOffset {
		// Simple pagination for JSON arrays and text
		return t.paginateData(ref, meta, input)
	}

	// No valid query method provided
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:       "invalid_input",
			Message:    fmt.Sprintf("Data type '%s' requires specific query method", meta.DataType),
			Suggestion: "Check the tool result metadata and retrieval hints for supported query methods (e.g., offset/limit for text, sql for SQL results)",
		},
	}, nil
}

// convertAndQuery converts JSON/CSV to temporary SQLite table and executes query.
func (t *QueryToolResultTool) convertAndQuery(ctx context.Context, ref *loomv1.DataReference, meta *storage.DataMetadata, sqlQuery string) (*shuttle.Result, error) {
	// Check if SQL store is available for conversion
	if t.sqlStore == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "store_not_available",
				Message:    "SQL store required for SQL queries on non-SQL data",
				Suggestion: "Use offset/limit pagination instead",
			},
		}, nil
	}

	// Get raw data
	data, err := t.memoryStore.Get(ref)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "retrieval_failed",
				Message: fmt.Sprintf("Failed to retrieve data: %v", err),
			},
		}, nil
	}

	// Convert based on data type
	var columns []string
	var rows [][]interface{}

	switch meta.DataType {
	case "json_array":
		columns, rows, err = t.convertJSONArrayToRows(data)
	case "csv":
		columns, rows, err = t.convertCSVToRows(data)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "unsupported_type",
				Message:    fmt.Sprintf("SQL queries not supported for data type: %s", meta.DataType),
				Suggestion: "Only json_array and csv support SQL queries",
			},
		}, nil
	}

	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "conversion_failed",
				Message: fmt.Sprintf("Failed to convert data: %v", err),
			},
		}, nil
	}

	// Store in SQL store (creates temporary table)
	// Generate unique ID for temporary conversion
	tempID := fmt.Sprintf("temp_%s_%d", ref.Id, time.Now().UnixNano())

	// Convert []string columns to []interface{} for Store
	columnsInterface := make([]interface{}, len(columns))
	for i, col := range columns {
		columnsInterface[i] = col
	}

	// Convert [][]interface{} rows to []interface{} for Store
	rowsInterface := make([]interface{}, len(rows))
	for i, row := range rows {
		rowsInterface[i] = row
	}

	dataMap := map[string]interface{}{
		"columns": columnsInterface,
		"rows":    rowsInterface,
	}
	dataRef, err := t.sqlStore.Store(ctx, tempID, dataMap)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "storage_failed",
				Message: fmt.Sprintf("Failed to create temporary table: %v", err),
			},
		}, nil
	}

	// Get metadata for table name
	tableMeta, err := t.sqlStore.GetMetadata(ctx, dataRef.Id)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "metadata_failed",
				Message: fmt.Sprintf("Failed to get table metadata: %v", err),
			},
		}, nil
	}

	// Replace "results" with actual table name
	actualQuery := strings.ReplaceAll(sqlQuery, "FROM results", fmt.Sprintf("FROM %s", tableMeta.TableName))
	actualQuery = strings.ReplaceAll(actualQuery, "from results", fmt.Sprintf("FROM %s", tableMeta.TableName))

	// Execute query
	result, err := t.sqlStore.Query(ctx, dataRef.Id, actualQuery)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "query_failed",
				Message:    fmt.Sprintf("Query failed: %v", err),
				Suggestion: "Check SQL syntax. Columns: " + strings.Join(columns, ", "),
			},
		}, nil
	}

	// Clean up temporary table after a short delay (via TTL)
	// The SQLResultStore will auto-cleanup based on TTL

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"converted_from": meta.DataType,
			"temp_table":     tableMeta.TableName,
		},
	}, nil
}

// convertJSONArrayToRows converts JSON array to SQL table format.
func (t *QueryToolResultTool) convertJSONArrayToRows(data []byte) ([]string, [][]interface{}, error) {
	var items []map[string]interface{}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, nil, fmt.Errorf("failed to parse JSON array: %w", err)
	}

	if len(items) == 0 {
		return []string{}, [][]interface{}{}, nil
	}

	// Infer columns from first item
	firstItem := items[0]
	columns := make([]string, 0, len(firstItem))
	for key := range firstItem {
		columns = append(columns, key)
	}

	// Sort columns for consistency
	sortStringSlice(columns)

	// Convert each item to a row
	rows := make([][]interface{}, 0, len(items))
	for _, item := range items {
		row := make([]interface{}, len(columns))
		for i, col := range columns {
			val := item[col]
			// Convert complex types to JSON strings
			if val != nil {
				switch v := val.(type) {
				case map[string]interface{}, []interface{}:
					jsonBytes, _ := json.Marshal(v)
					row[i] = string(jsonBytes)
				default:
					row[i] = v
				}
			}
		}
		rows = append(rows, row)
	}

	return columns, rows, nil
}

// convertCSVToRows converts CSV data to SQL table format.
func (t *QueryToolResultTool) convertCSVToRows(data []byte) ([]string, [][]interface{}, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return nil, nil, fmt.Errorf("CSV must have at least header and one data row")
	}

	// Parse header
	headerLine := lines[0]
	columns := strings.Split(headerLine, ",")
	for i := range columns {
		columns[i] = strings.TrimSpace(columns[i])
	}

	// Parse rows
	rows := make([][]interface{}, 0, len(lines)-1)
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		values := strings.Split(line, ",")
		row := make([]interface{}, len(columns))
		for j, val := range values {
			if j < len(row) {
				row[j] = strings.TrimSpace(val)
			}
		}
		rows = append(rows, row)
	}

	return columns, rows, nil
}

// sortStringSlice sorts a string slice in place.
func sortStringSlice(s []string) {
	for i := 0; i < len(s)-1; i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// paginateData implements simple pagination for JSON arrays and plain text.
func (t *QueryToolResultTool) paginateData(ref *loomv1.DataReference, meta *storage.DataMetadata, input map[string]interface{}) (*shuttle.Result, error) {
	// Get full data
	data, err := t.memoryStore.Get(ref)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "retrieval_failed",
				Message: fmt.Sprintf("Failed to retrieve data: %v", err),
			},
		}, nil
	}

	// Extract pagination parameters
	offset := 0
	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}
	limit := 100 // default
	if l, ok := input["limit"].(float64); ok {
		limit = int(l)
	}

	// Handle based on data type
	switch meta.DataType {
	case "json_array":
		return t.paginateJSONArray(data, offset, limit)
	case "text":
		return t.paginateText(data, offset, limit)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "invalid_data_type",
				Message:    fmt.Sprintf("Pagination only supports json_array and text, got %s", meta.DataType),
				Suggestion: "Check the tool result metadata and retrieval hints for supported query methods",
			},
		}, nil
	}
}

// paginateJSONArray paginates JSON array data by items.
func (t *QueryToolResultTool) paginateJSONArray(data []byte, offset, limit int) (*shuttle.Result, error) {
	var items []interface{}
	if err := json.Unmarshal(data, &items); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "parse_failed",
				Message: fmt.Sprintf("Failed to parse JSON: %v", err),
			},
		}, nil
	}

	// Apply pagination
	if offset < 0 || offset >= len(items) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_offset",
				Message: fmt.Sprintf("Offset %d out of range (0-%d)", offset, len(items)-1),
			},
		}, nil
	}

	end := offset + limit
	if end > len(items) {
		end = len(items)
	}

	paginatedItems := items[offset:end]

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"items":          paginatedItems,
			"offset":         offset,
			"limit":          limit,
			"returned_count": len(paginatedItems),
			"total_count":    len(items),
			"has_more":       end < len(items),
		},
	}, nil
}

// paginateText paginates plain text data by lines.
func (t *QueryToolResultTool) paginateText(data []byte, offset, limit int) (*shuttle.Result, error) {
	// Split into lines
	text := string(data)
	lines := strings.Split(text, "\n")

	// Apply pagination
	if offset < 0 || offset >= len(lines) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_offset",
				Message: fmt.Sprintf("Offset %d out of range (0-%d)", offset, len(lines)-1),
			},
		}, nil
	}

	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}

	paginatedLines := lines[offset:end]

	// Return as array of lines for easier processing
	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"lines":          paginatedLines,
			"offset":         offset,
			"limit":          limit,
			"returned_count": len(paginatedLines),
			"total_lines":    len(lines),
			"has_more":       end < len(lines),
		},
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *QueryToolResultTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure QueryToolResultTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*QueryToolResultTool)(nil)

// RecordFindingTool allows agents to record verified findings in working memory.
// This prevents hallucination by maintaining structured facts discovered during analysis.
//
// Findings are stored in the SegmentedMemory Kernel layer and automatically injected
// into LLM context as a "Verified Findings" summary, providing working memory across
// tool executions.
type RecordFindingTool struct {
	memory *Memory // Agent's memory manager to access sessions
}

// NewRecordFindingTool creates a new RecordFindingTool.
func NewRecordFindingTool(memory *Memory) *RecordFindingTool {
	return &RecordFindingTool{
		memory: memory,
	}
}

// Name returns the tool name.
func (t *RecordFindingTool) Name() string {
	return "record_finding"
}

// Description returns the tool description for the LLM.
func (t *RecordFindingTool) Description() string {
	return `Record verified findings in working memory to prevent hallucination.

Use this tool to store structured facts discovered during analysis. These findings are
automatically injected into your context as a "Verified Findings" summary, providing
working memory across tool executions.

When to record findings:
- After counting rows: record_finding(path="table_name.row_count", value=2195, category="statistic")
- After schema discovery: record_finding(path="table_name.columns", value=["col1", "col2"], category="schema")
- After null analysis: record_finding(path="table_name.column_name.null_rate", value=0.17, category="statistic", note="376 out of 2195 rows")
- After finding patterns: record_finding(path="table_name.observation", value="All sites have unique site_id", category="observation")
- After distribution analysis: record_finding(path="table_name.region.distribution", value={"Americas": 0.54, "EMEA": 0.27}, category="distribution")

Path naming conventions:
- Use hierarchical structure: "table.column.metric" or "table.metric"
- Use snake_case for consistency
- Examples: "customers.row_count", "orders.status.null_rate", "sales.region.distribution"

Categories:
- "statistic": Counts, percentages, aggregates (e.g., row counts, null rates, averages)
- "schema": Table/column structures, data types, relationships
- "observation": Patterns, anomalies, business insights
- "distribution": Value distributions, frequency analysis, grouping results

Input:
- path (required): Hierarchical key for the finding (e.g., "table.column.metric")
- value (required): The actual data (number, string, array, object)
- category (optional): Type of finding ("statistic", "schema", "observation", "distribution")
- note (optional): Additional context or explanation
- source (optional): Tool call ID that produced this finding

Examples:
- record_finding(path="vantage_sites.row_count", value=2195, category="statistic")
- record_finding(path="vantage_sites.columns", value=["site_id", "customer_id", "region"], category="schema")
- record_finding(path="vantage_sites.customer_id.null_rate", value=0.17, category="statistic", note="376 out of 2195 rows")
- record_finding(path="vantage_sites.site_id.uniqueness", value="100% unique - likely primary key", category="observation")
- record_finding(path="vantage_sites.business_region.distribution", value={"Americas": 1183, "EMEA": 589, "APJ": 423}, category="distribution")`
}

// InputSchema returns the JSON schema for the tool input.
func (t *RecordFindingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"path": {
				Type:        "string",
				Description: "Hierarchical key for the finding (e.g., 'table.column.metric')",
			},
			"value": {
				Type:        "string",
				Description: "The actual data as JSON string (number, string, array, or object)",
			},
			"category": {
				Type:        "string",
				Description: "Type of finding: 'statistic', 'schema', 'observation', or 'distribution'",
			},
			"note": {
				Type:        "string",
				Description: "Optional additional context or explanation",
			},
			"source": {
				Type:        "string",
				Description: "Optional tool call ID that produced this finding",
			},
		},
		Required: []string{"path", "value"},
	}
}

// Execute records the finding in working memory.
func (t *RecordFindingTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	// Extract session_id from context (typed key)
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "no_session",
				Message: "Session ID not found in context",
			},
		}, nil
	}

	// Get session from memory
	session, exists := t.memory.GetSession(sessionID)
	if !exists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "session_not_found",
				Message: fmt.Sprintf("Session %s not found", sessionID),
			},
		}, nil
	}

	// Get SegmentedMemory from session
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok || segMem == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "no_segmented_memory",
				Message: "Session does not have segmented memory enabled",
			},
		}, nil
	}

	// Validate path
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "path must be a non-empty string",
			},
		}, nil
	}

	// Validate value (required, can be any type)
	value, hasValue := input["value"]
	if !hasValue {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "value is required",
			},
		}, nil
	}

	// Extract optional parameters
	category := ""
	if cat, ok := input["category"].(string); ok {
		category = cat
	}

	note := ""
	if n, ok := input["note"].(string); ok {
		note = n
	}

	source := ""
	if src, ok := input["source"].(string); ok {
		source = src
	}

	// Validate category if provided
	validCategories := map[string]bool{
		"statistic":    true,
		"schema":       true,
		"observation":  true,
		"distribution": true,
	}
	if category != "" && !validCategories[category] {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "invalid_input",
				Message:    fmt.Sprintf("Invalid category '%s'. Must be one of: statistic, schema, observation, distribution", category),
				Suggestion: "Use 'statistic' for counts/percentages, 'schema' for structure, 'observation' for patterns, 'distribution' for value distributions",
			},
		}, nil
	}

	// Record finding in session's segmented memory
	segMem.RecordFinding(path, value, category, note, source)

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"recorded": true,
			"path":     path,
			"category": category,
		},
	}, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *RecordFindingTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure RecordFindingTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*RecordFindingTool)(nil)
