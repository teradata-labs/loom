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
// Package adapter provides adapters to integrate MCP with Loom's existing systems.
// This package bridges MCP tools to Loom's shuttle.Tool interface.
package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// debugBedrockTools is cached at package init to avoid repeated os.Getenv calls.
var debugBedrockTools = os.Getenv("LOOM_DEBUG_BEDROCK_TOOLS") == "1"

// Result truncation and caching configuration
const (
	// DefaultMaxResultBytes is the maximum size of tool results before truncation
	// 20KB matches MaxPreviewChars in storage package (20,000 chars ≈ 5K tokens)
	DefaultMaxResultBytes = 20000

	// DefaultMaxResultRows is the maximum number of rows to return from SQL results
	DefaultMaxResultRows = 500

	// SchemaCacheTTL is how long schema results are cached
	SchemaCacheTTL = 5 * time.Minute
)

// schemaCache provides session-level caching for table schema lookups
type schemaCache struct {
	mu      sync.RWMutex
	entries map[string]*schemaCacheEntry
}

type schemaCacheEntry struct {
	result    string
	timestamp time.Time
}

var globalSchemaCache = &schemaCache{
	entries: make(map[string]*schemaCacheEntry),
}

func (c *schemaCache) get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.entries[key]
	if !exists {
		return "", false
	}

	// Check TTL
	if time.Since(entry.timestamp) > SchemaCacheTTL {
		return "", false
	}

	return entry.result, true
}

func (c *schemaCache) set(key string, result string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &schemaCacheEntry{
		result:    result,
		timestamp: time.Now(),
	}
}

// TruncationConfig configures how tool results are truncated
type TruncationConfig struct {
	MaxResultBytes int  // Maximum result size in bytes (0 = use default)
	MaxResultRows  int  // Maximum rows for SQL results (0 = use default)
	Enabled        bool // Whether truncation is enabled
}

// MCPToolAdapter wraps an MCP tool as a shuttle.Tool
type MCPToolAdapter struct {
	client        *client.Client
	tool          protocol.Tool
	serverName    string                     // Used as backend identifier
	truncation    TruncationConfig           // Result truncation settings
	sqlStore      *storage.SQLResultStore    // For storing large SQL results
	sharedMemory  *storage.SharedMemoryStore // For storing other large data
	uiResourceURI string                     // From tool._meta.ui.resourceUri (MCP Apps)
	logger        *zap.Logger                // Structured logger (defaults to no-op)
}

// NewMCPToolAdapter creates a new adapter that wraps an MCP tool
func NewMCPToolAdapter(client *client.Client, tool protocol.Tool, serverName string) *MCPToolAdapter {
	adapter := &MCPToolAdapter{
		client:     client,
		tool:       tool,
		serverName: serverName,
		truncation: TruncationConfig{
			MaxResultBytes: DefaultMaxResultBytes,
			MaxResultRows:  DefaultMaxResultRows,
			Enabled:        true, // Enable by default
		},
		sqlStore:     nil,          // Will be set by SetSQLResultStore if needed
		sharedMemory: nil,          // Will be set by SetSharedMemory if needed
		logger:       zap.NewNop(), // No-op by default; use SetLogger to enable
	}

	// Extract UI metadata from tool._meta.ui if present (MCP Apps)
	if uiMeta := protocol.GetUIToolMeta(tool); uiMeta != nil {
		adapter.uiResourceURI = uiMeta.ResourceURI
	}

	return adapter
}

// SetSQLResultStore configures SQL result store for this adapter.
// Enables automatic storage of large SQL results.
func (a *MCPToolAdapter) SetSQLResultStore(store *storage.SQLResultStore) {
	a.sqlStore = store
}

// SetSharedMemory configures shared memory store for this adapter.
// Enables automatic storage of large non-SQL data.
func (a *MCPToolAdapter) SetSharedMemory(store *storage.SharedMemoryStore) {
	a.sharedMemory = store
}

// SetLogger configures the structured logger for this adapter.
// If not called, a no-op logger is used.
func (a *MCPToolAdapter) SetLogger(logger *zap.Logger) {
	if logger != nil {
		a.logger = logger
	}
}

// NewMCPToolAdapterWithConfig creates a new adapter with custom truncation config
func NewMCPToolAdapterWithConfig(client *client.Client, tool protocol.Tool, serverName string, config TruncationConfig) *MCPToolAdapter {
	if config.MaxResultBytes == 0 {
		config.MaxResultBytes = DefaultMaxResultBytes
	}
	if config.MaxResultRows == 0 {
		config.MaxResultRows = DefaultMaxResultRows
	}
	adapter := &MCPToolAdapter{
		client:     client,
		tool:       tool,
		serverName: serverName,
		truncation: config,
		logger:     zap.NewNop(), // No-op by default; use SetLogger to enable
	}

	// Extract UI metadata from tool._meta.ui if present (MCP Apps)
	if uiMeta := protocol.GetUIToolMeta(tool); uiMeta != nil {
		adapter.uiResourceURI = uiMeta.ResourceURI
	}

	return adapter
}

// Name implements shuttle.Tool
func (a *MCPToolAdapter) Name() string {
	// Prefix with server name to avoid collisions
	return fmt.Sprintf("%s:%s", a.serverName, a.tool.Name)
}

// Description implements shuttle.Tool
func (a *MCPToolAdapter) Description() string {
	return a.tool.Description
}

// InputSchema implements shuttle.Tool
func (a *MCPToolAdapter) InputSchema() *shuttle.JSONSchema {
	// Convert MCP InputSchema (map[string]interface{}) to shuttle.JSONSchema
	if len(a.tool.InputSchema) == 0 {
		// No schema - accept any object
		return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{}, nil)
	}

	// Serialize and deserialize to convert types
	schemaBytes, err := json.Marshal(a.tool.InputSchema)
	if err != nil {
		// Fallback to empty schema
		return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{}, nil)
	}

	var shuttleSchema shuttle.JSONSchema
	if err := json.Unmarshal(schemaBytes, &shuttleSchema); err != nil {
		// Fallback to empty schema
		return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{}, nil)
	}

	// Convert property names from camelCase to snake_case for LLM-friendliness
	// LLMs naturally prefer snake_case but MCP schemas often use camelCase
	if shuttleSchema.Properties != nil {
		snakeCaseProps := make(map[string]*shuttle.JSONSchema)
		for key, prop := range shuttleSchema.Properties {
			snakeCaseProps[toSnakeCase(key)] = prop
		}
		shuttleSchema.Properties = snakeCaseProps

		// Also convert required field names
		if len(shuttleSchema.Required) > 0 {
			snakeCaseRequired := make([]string, len(shuttleSchema.Required))
			for i, req := range shuttleSchema.Required {
				snakeCaseRequired[i] = toSnakeCase(req)
			}
			shuttleSchema.Required = snakeCaseRequired
		}
	}

	// Normalize schema to ensure JSON Schema draft 2020-12 compliance
	// This is critical for Bedrock which strictly validates schemas
	normalized := shuttle.NormalizeSchema(&shuttleSchema)

	// Debug logging to see what MCP provides and what we convert to.
	// Uses package-level cached env var to avoid per-call os.Getenv overhead.
	// Logs to zap (stderr) instead of fmt.Printf (stdout) to avoid corrupting
	// the MCP stdio transport channel.
	if debugBedrockTools {
		mcpJSON, _ := json.MarshalIndent(a.tool.InputSchema, "", "  ")
		normalizedJSON, _ := json.MarshalIndent(normalized, "", "  ")
		a.logger.Debug("MCP tool schema normalization",
			zap.String("tool", a.tool.Name),
			zap.String("original_schema", string(mcpJSON)),
			zap.String("normalized_schema", string(normalizedJSON)),
		)
	}

	return normalized
}

// Execute implements shuttle.Tool
func (a *MCPToolAdapter) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	startTime := time.Now()

	// Convert parameter names from snake_case back to camelCase for MCP tools
	// LLMs naturally use snake_case but MCP tools expect camelCase
	camelCaseParams := normalizeParametersToCamelCase(params)

	// Check schema cache for schema-related tools (#4: Schema Caching)
	if a.isSchemaLookupTool() {
		cacheKey := a.buildSchemaCacheKey(camelCaseParams)
		if cached, ok := globalSchemaCache.get(cacheKey); ok {
			return &shuttle.Result{
				Success:         true,
				Data:            fmt.Sprintf("(cached) %s", cached),
				ExecutionTimeMs: 0,
				Metadata: map[string]interface{}{
					"mcp_server": a.serverName,
					"tool_name":  a.tool.Name,
					"cache_hit":  true,
					"cache_key":  cacheKey,
				},
			}, nil
		}
	}

	// Call MCP tool with camelCase parameters
	mcpResultInterface, err := a.client.CallTool(ctx, a.tool.Name, camelCaseParams)
	executionTime := time.Since(startTime).Milliseconds()

	if err != nil {
		// Convert error to shuttle.Result with error
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MCP_CALL_FAILED",
				Message:    err.Error(),
				Retryable:  true,
				Suggestion: "Check MCP server logs for details",
			},
			ExecutionTimeMs: executionTime,
		}, nil // Return nil error since we wrapped it in Result.Error
	}

	// Type assert to *protocol.CallToolResult
	mcpResult, ok := mcpResultInterface.(*protocol.CallToolResult)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_RESULT_TYPE",
				Message: fmt.Sprintf("Expected *protocol.CallToolResult, got %T", mcpResultInterface),
			},
			ExecutionTimeMs: executionTime,
		}, nil
	}

	// Convert MCP content to shuttle result data
	data := convertMCPContent(mcpResult.Content)

	// CRITICAL: Detect SQL results and store to SQLResultStore
	// This prevents 510KB results from entering context
	if a.sqlStore != nil {
		if sqlData, isSQLResult := a.detectAndExtractSQLResult(data); isSQLResult {
			// Safely extract row and column counts using comma-ok pattern
			// to avoid panics if the type structure is unexpected.
			rows, rowsOK := sqlData["rows"].([]interface{})
			columns, colsOK := sqlData["columns"].([]interface{})
			if !rowsOK || !colsOK {
				// Type mismatch despite detection -- fall through to normal result handling
				goto normalResult
			}

			// Generate unique ID for this result
			resultID := fmt.Sprintf("mcp_%s_%d", a.serverName, time.Now().UnixNano())

			// Store SQL result directly to database
			ref, err := a.sqlStore.Store(resultID, sqlData)
			if err == nil {
				// Success - return DataRef instead of full data
				return &shuttle.Result{
					Success:         true,
					DataReference:   ref,
					ExecutionTimeMs: executionTime,
					Metadata: map[string]interface{}{
						"mcp_server":    a.serverName,
						"tool_name":     a.tool.Name,
						"sql_result":    true,
						"rows":          len(rows),
						"columns":       len(columns),
						"stored_in_sql": true,
					},
					Data: fmt.Sprintf("Query returned %d rows (%d columns). Use query_tool_result to filter/paginate.",
						len(rows), len(columns)),
				}, nil
			}
			// If storage failed, fall through to normal truncation
		}
	}
normalResult:

	// Apply result truncation (#1: Truncate Tool Results)
	var truncated bool
	var originalSize int
	if a.truncation.Enabled {
		data, truncated, originalSize = a.truncateResult(data)
	}

	// Cache schema results (#4: Schema Caching)
	if a.isSchemaLookupTool() {
		cacheKey := a.buildSchemaCacheKey(camelCaseParams)
		if str, ok := data.(string); ok {
			globalSchemaCache.set(cacheKey, str)
		}
	}

	metadata := map[string]interface{}{
		"mcp_server": a.serverName,
		"tool_name":  a.tool.Name,
	}
	if truncated {
		metadata["truncated"] = true
		metadata["original_size"] = originalSize
		metadata["truncated_to"] = a.truncation.MaxResultBytes
	}

	return &shuttle.Result{
		Success:         true,
		Data:            data,
		ExecutionTimeMs: executionTime,
		Metadata:        metadata,
	}, nil
}

// Backend implements shuttle.Tool
func (a *MCPToolAdapter) Backend() string {
	// Use server name as backend identifier
	// This allows backend-specific routing if needed
	return fmt.Sprintf("mcp:%s", a.serverName)
}

// HasUI returns true if this tool has an associated MCP Apps UI resource.
func (a *MCPToolAdapter) HasUI() bool {
	return a.uiResourceURI != ""
}

// UIResourceURI returns the URI of the associated UI resource, or empty string.
func (a *MCPToolAdapter) UIResourceURI() string {
	return a.uiResourceURI
}

// convertMCPContent converts MCP Content array to shuttle-compatible data
func convertMCPContent(content []protocol.Content) interface{} {
	if len(content) == 0 {
		return nil
	}

	// If single text content, return as string
	if len(content) == 1 && content[0].Type == "text" {
		return content[0].Text
	}

	// Multiple content items or non-text - return as structured data
	results := make([]map[string]interface{}, len(content))
	for i, c := range content {
		item := map[string]interface{}{
			"type": c.Type,
		}

		switch c.Type {
		case "text":
			item["text"] = c.Text
		case "image":
			item["data"] = c.Data
			item["mimeType"] = c.MimeType
		case "resource":
			if c.Resource != nil {
				item["uri"] = c.Resource.URI
				item["mimeType"] = c.Resource.MimeType
			}
		}

		results[i] = item
	}

	return results
}

// AdaptMCPTools converts all tools from an MCP client to shuttle.Tool instances
func AdaptMCPTools(ctx context.Context, mcpClient *client.Client, serverName string) ([]shuttle.Tool, error) {
	// List all tools from MCP server
	mcpTools, err := mcpClient.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCP tools: %w", err)
	}

	// Convert each MCP tool to shuttle.Tool
	tools := make([]shuttle.Tool, len(mcpTools))
	for i, mcpTool := range mcpTools {
		tools[i] = NewMCPToolAdapter(mcpClient, mcpTool, serverName)
	}

	return tools, nil
}

// =============================================================================
// Helper methods for result truncation and schema caching
// =============================================================================

// isSchemaLookupTool returns true if this tool fetches table/column schema
func (a *MCPToolAdapter) isSchemaLookupTool() bool {
	toolName := strings.ToLower(a.tool.Name)
	schemaPatterns := []string{
		"get_table_schema",
		"get_schema",
		"describe_table",
		"table_schema",
		"get_columns",
		"list_columns",
		"column_info",
	}
	for _, pattern := range schemaPatterns {
		if strings.Contains(toolName, pattern) {
			return true
		}
	}
	return false
}

// buildSchemaCacheKey creates a unique cache key for schema lookups
func (a *MCPToolAdapter) buildSchemaCacheKey(params map[string]interface{}) string {
	// Hash the params to create a stable key
	paramsJSON, _ := json.Marshal(params)
	hash := sha256.Sum256(paramsJSON)
	return fmt.Sprintf("%s:%s:%x", a.serverName, a.tool.Name, hash[:8])
}

// truncateResult applies truncation to tool results to reduce token consumption
// Returns: (truncatedData, wasTruncated, originalSize)
func (a *MCPToolAdapter) truncateResult(data interface{}) (interface{}, bool, int) {
	switch v := data.(type) {
	case string:
		return a.truncateString(v)
	case []map[string]interface{}:
		return a.truncateArrayResult(v)
	default:
		// For other types, convert to string and truncate
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			return data, false, 0
		}
		truncated, wasTruncated, originalSize := a.truncateString(string(jsonBytes))
		return truncated, wasTruncated, originalSize
	}
}

// truncateString truncates a string result to maxResultBytes
func (a *MCPToolAdapter) truncateString(s string) (interface{}, bool, int) {
	originalSize := len(s)
	if originalSize <= a.truncation.MaxResultBytes {
		return s, false, originalSize
	}

	// Try to truncate at a row boundary for SQL results
	truncated := s[:a.truncation.MaxResultBytes]

	// Look for last complete row (newline)
	lastNewline := strings.LastIndex(truncated, "\n")
	if lastNewline > a.truncation.MaxResultBytes/2 {
		truncated = truncated[:lastNewline]
	}

	// Add truncation notice
	rowCount := strings.Count(s, "\n")
	truncatedRows := strings.Count(truncated, "\n")

	notice := fmt.Sprintf("\n\n[TRUNCATED: Showing %d of ~%d rows (%d of %d bytes). Use LIMIT in queries or create volatile tables for full results.]",
		truncatedRows, rowCount, len(truncated), originalSize)

	return truncated + notice, true, originalSize
}

// truncateArrayResult truncates array results (multiple content items)
func (a *MCPToolAdapter) truncateArrayResult(items []map[string]interface{}) (interface{}, bool, int) {
	jsonBytes, _ := json.Marshal(items)
	originalSize := len(jsonBytes)

	if originalSize <= a.truncation.MaxResultBytes {
		return items, false, originalSize
	}

	// Keep only first N items
	maxItems := a.truncation.MaxResultRows
	if len(items) <= maxItems {
		// Items are fine, but individual items might be large
		// Truncate text content within items
		for i := range items {
			if text, ok := items[i]["text"].(string); ok {
				if len(text) > a.truncation.MaxResultBytes/len(items) {
					items[i]["text"] = text[:a.truncation.MaxResultBytes/len(items)] + "... [truncated]"
				}
			}
		}
		return items, true, originalSize
	}

	truncatedItems := items[:maxItems]
	truncatedItems = append(truncatedItems, map[string]interface{}{
		"type": "text",
		"text": fmt.Sprintf("[TRUNCATED: Showing %d of %d items]", maxItems, len(items)),
	})

	return truncatedItems, true, originalSize
}

// ClearSchemaCache clears the global schema cache (useful for testing or cache invalidation)
func ClearSchemaCache() {
	globalSchemaCache.mu.Lock()
	defer globalSchemaCache.mu.Unlock()
	globalSchemaCache.entries = make(map[string]*schemaCacheEntry)
}

// GetSchemaCacheStats returns cache statistics
func GetSchemaCacheStats() (entries int, oldestAge time.Duration) {
	globalSchemaCache.mu.RLock()
	defer globalSchemaCache.mu.RUnlock()

	entries = len(globalSchemaCache.entries)
	var oldest time.Time
	for _, entry := range globalSchemaCache.entries {
		if oldest.IsZero() || entry.timestamp.Before(oldest) {
			oldest = entry.timestamp
		}
	}
	if !oldest.IsZero() {
		oldestAge = time.Since(oldest)
	}
	return
}

// toSnakeCase converts a camelCase string to snake_case.
// This helps LLMs work with MCP tools that use camelCase parameters.
// Example: "databaseName" -> "database_name"
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				result.WriteRune('_')
			}
			result.WriteRune(unicode.ToLower(r))
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// toCamelCase converts a snake_case string to camelCase.
// Example: "database_name" -> "databaseName"
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 1 {
		return s // Already camelCase or single word
	}

	var result strings.Builder
	result.WriteString(parts[0]) // First part stays lowercase
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			result.WriteRune(unicode.ToUpper(rune(parts[i][0])))
			result.WriteString(parts[i][1:])
		}
	}
	return result.String()
}

// normalizeParametersToCamelCase converts all parameter keys in a map from snake_case to camelCase.
// This restores the original parameter names expected by MCP tools.
func normalizeParametersToCamelCase(params map[string]interface{}) map[string]interface{} {
	if params == nil {
		return nil
	}

	normalized := make(map[string]interface{}, len(params))
	for key, value := range params {
		normalized[toCamelCase(key)] = value
	}
	return normalized
}

// detectAndExtractSQLResult detects if MCP result contains SQL data and extracts it.
// Returns (sqlData map[string]interface{}, isSQLResult bool)
// sqlData contains "columns" and "rows" keys if SQL result detected.
func (a *MCPToolAdapter) detectAndExtractSQLResult(data interface{}) (map[string]interface{}, bool) {
	// SQL results from MCP tools typically come as text with embedded JSON
	// Format: "✓ SQL executed successfully\n\nOutput:\n{\"columns\":[...],\"rows\":[[...]]}"

	str, ok := data.(string)
	if !ok {
		return nil, false
	}

	// Look for JSON pattern with columns and rows
	// Use regex to find JSON object containing both columns and rows arrays
	jsonPattern := regexp.MustCompile(`\{[^{}]*"columns"\s*:\s*\[[^\]]*\][^{}]*"rows"\s*:\s*\[`)
	if !jsonPattern.MatchString(str) {
		return nil, false
	}

	// Find the start of the JSON object
	jsonStart := strings.Index(str, `{"columns"`)
	if jsonStart == -1 {
		// Try alternative: columns might come second
		jsonStart = strings.Index(str, `{"rows"`)
		if jsonStart == -1 {
			return nil, false
		}
	}

	// Extract JSON substring (from { to end)
	jsonStr := str[jsonStart:]

	// Find the matching closing brace
	braceCount := 0
	jsonEnd := -1
	for i, ch := range jsonStr {
		if ch == '{' {
			braceCount++
		} else if ch == '}' {
			braceCount--
			if braceCount == 0 {
				jsonEnd = i + 1
				break
			}
		}
	}

	if jsonEnd == -1 {
		return nil, false
	}

	jsonStr = jsonStr[:jsonEnd]

	// Parse JSON
	var sqlData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &sqlData); err != nil {
		return nil, false
	}

	// Validate that it has both columns and rows
	columns, hasColumns := sqlData["columns"]
	rows, hasRows := sqlData["rows"]

	if !hasColumns || !hasRows {
		return nil, false
	}

	// Validate types
	if _, ok := columns.([]interface{}); !ok {
		return nil, false
	}
	if _, ok := rows.([]interface{}); !ok {
		return nil, false
	}

	return sqlData, true
}
