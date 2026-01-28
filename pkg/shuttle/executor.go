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
package shuttle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/storage"
)

// ToolRegistry is an interface for dynamic tool discovery.
// This avoids import cycles with pkg/tools/registry.
type ToolRegistry interface {
	Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error)
}

// MCPManager is an interface for getting MCP clients.
// This avoids import cycles with pkg/mcp/manager.
type MCPManager interface {
	GetClient(serverName string) (interface{}, error)
}

// BuiltinToolProvider is an interface for getting builtin tools.
// This avoids import cycles with pkg/shuttle/builtin.
type BuiltinToolProvider interface {
	GetTool(name string) Tool
}

// Executor executes tools with tracking and error handling.
type Executor struct {
	registry            *Registry
	sharedMemory        *storage.SharedMemoryStore
	sqlResultStore      *storage.SQLResultStore // SQL result store for queryable large results
	threshold           int64                   // Threshold for using shared memory (bytes)
	permissionChecker   *PermissionChecker
	toolRegistry        ToolRegistry        // Tool registry for dynamic tool discovery
	mcpManager          MCPManager          // MCP manager for dynamic MCP tool registration
	builtinToolProvider BuiltinToolProvider // Builtin tool provider for dynamic builtin tool registration

	// Metrics for large parameter optimization
	largeParamStores      atomic.Int64 // Count of parameters stored
	largeParamDerefs      atomic.Int64 // Count of parameters dereferenced
	largeParamBytesStored atomic.Int64 // Total bytes stored
	largeParamDerefErrors atomic.Int64 // Count of dereference failures
}

// NewExecutor creates a new tool executor.
func NewExecutor(registry *Registry) *Executor {
	return &Executor{
		registry:  registry,
		threshold: storage.DefaultSharedMemoryThreshold,
	}
}

// SetSharedMemory configures shared memory for large result handling.
func (e *Executor) SetSharedMemory(sharedMemory *storage.SharedMemoryStore, threshold int64) {
	e.sharedMemory = sharedMemory
	if threshold >= 0 {
		e.threshold = threshold
	}
}

// SetSQLResultStore configures SQL result store for queryable large SQL results.
func (e *Executor) SetSQLResultStore(sqlStore *storage.SQLResultStore) {
	e.sqlResultStore = sqlStore
}

// SetPermissionChecker configures permission checking for tool execution.
func (e *Executor) SetPermissionChecker(checker *PermissionChecker) {
	e.permissionChecker = checker
}

// SetToolRegistry configures the tool registry for dynamic tool discovery.
// When a tool is not found in the local registry, the executor will check
// the tool registry and dynamically register MCP tools if found.
func (e *Executor) SetToolRegistry(registry ToolRegistry) {
	e.toolRegistry = registry
}

// SetMCPManager configures the MCP manager for dynamic MCP tool registration.
func (e *Executor) SetMCPManager(manager MCPManager) {
	e.mcpManager = manager
}

// SetBuiltinToolProvider configures the builtin tool provider for dynamic builtin tool registration.
func (e *Executor) SetBuiltinToolProvider(provider BuiltinToolProvider) {
	e.builtinToolProvider = provider
}

// Execute executes a tool by name with the given parameters.
func (e *Executor) Execute(ctx context.Context, toolName string, params map[string]interface{}) (*Result, error) {
	tool, ok := e.registry.Get(toolName)
	if !ok {
		// Tool not found locally, try dynamic registration
		dynamicTool, err := e.tryDynamicRegistration(ctx, toolName)
		if err != nil {
			return nil, fmt.Errorf("tool not found: %s (dynamic registration failed: %w)", toolName, err)
		}
		if dynamicTool == nil {
			return nil, fmt.Errorf("tool not found: %s", toolName)
		}
		tool = dynamicTool
	}

	// Check permissions before execution
	if e.permissionChecker != nil {
		if err := e.permissionChecker.CheckPermission(ctx, toolName, params); err != nil {
			return &Result{
				Success: false,
				Error:   &Error{Code: "permission_denied", Message: err.Error(), Retryable: false},
			}, nil
		}
	}

	// Normalize parameters to match schema expectations
	// LLMs naturally use snake_case, but some tools expect camelCase
	normalizedParams := normalizeParametersToSchema(tool, params)

	// Handle large parameters: store in shared memory to prevent context bloat
	referencedParams, err := e.handleLargeParameters(normalizedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "LARGE_PARAM_ERROR",
				Message:   fmt.Sprintf("Failed to handle large parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	// Dereference parameters before tool execution (transparent to tools)
	finalParams, err := e.dereferenceLargeParameters(referencedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "DEREF_ERROR",
				Message:   fmt.Sprintf("Failed to dereference parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	start := time.Now()
	result, err := tool.Execute(ctx, finalParams)
	duration := time.Since(start)

	if err != nil {
		return &Result{
			Success:         false,
			Error:           &Error{Code: "execution_failed", Message: err.Error(), Retryable: false},
			ExecutionTimeMs: duration.Milliseconds(),
		}, nil
	}

	if result != nil {
		// Always set execution time, even if tool already set it
		// (executor timing is authoritative)
		result.ExecutionTimeMs = duration.Milliseconds()

		// Handle large results EXCEPT for progressive disclosure tools which retrieve already-stored large data
		// Wrapping these outputs creates infinite recursion: query_tool_result → DataRef A → query_tool_result(A) → DataRef B → ...
		// Excluded tools: get_tool_result (metadata), query_tool_result (actual data retrieval)
		if toolName != "get_tool_result" && toolName != "query_tool_result" {
			if err := e.handleLargeResult(result); err != nil {
				// Log error but don't fail execution
				// The result is still valid, just not optimized
				if result.Metadata == nil {
					result.Metadata = make(map[string]interface{})
				}
				result.Metadata["shared_memory_error"] = err.Error()
			}
		}
	} else {
		// Tool returned nil result, create one
		result = &Result{
			Success:         true,
			ExecutionTimeMs: duration.Milliseconds(),
		}
	}

	return result, nil
}

// ExecuteWithTool executes a specific tool instance (not from registry).
func (e *Executor) ExecuteWithTool(ctx context.Context, tool Tool, params map[string]interface{}) (*Result, error) {
	// Check permissions before execution
	if e.permissionChecker != nil {
		toolName := tool.Name()
		if err := e.permissionChecker.CheckPermission(ctx, toolName, params); err != nil {
			return &Result{
				Success: false,
				Error:   &Error{Code: "permission_denied", Message: err.Error(), Retryable: false},
			}, nil
		}
	}

	// Handle large parameters: store in shared memory to prevent context bloat
	referencedParams, err := e.handleLargeParameters(params)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "LARGE_PARAM_ERROR",
				Message:   fmt.Sprintf("Failed to handle large parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	// Dereference parameters before tool execution (transparent to tools)
	finalParams, err := e.dereferenceLargeParameters(referencedParams)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "DEREF_ERROR",
				Message:   fmt.Sprintf("Failed to dereference parameters: %v", err),
				Retryable: false,
			},
		}, nil
	}

	start := time.Now()
	result, err := tool.Execute(ctx, finalParams)
	duration := time.Since(start)

	if err != nil {
		return &Result{
			Success:         false,
			Error:           &Error{Code: "execution_failed", Message: err.Error(), Retryable: false},
			ExecutionTimeMs: duration.Milliseconds(),
		}, nil
	}

	if result != nil {
		// Always set execution time, even if tool already set it
		// (executor timing is authoritative)
		result.ExecutionTimeMs = duration.Milliseconds()

		// Handle large results EXCEPT for get_tool_result (deprecated) which retrieves large data
		// query_tool_result output SHOULD be wrapped to prevent context overflow
		if tool.Name() != "get_tool_result" {
			if err := e.handleLargeResult(result); err != nil {
				// Log error but don't fail execution
				if result.Metadata == nil {
					result.Metadata = make(map[string]interface{})
				}
				result.Metadata["shared_memory_error"] = err.Error()
			}
		}
	} else {
		// Tool returned nil result, create one
		result = &Result{
			Success:         true,
			ExecutionTimeMs: duration.Milliseconds(),
		}
	}

	return result, nil
}

// handleLargeResult checks if result data is large and stores it appropriately.
// SQL results go to SQLResultStore (queryable), other data goes to SharedMemoryStore (blob).
func (e *Executor) handleLargeResult(result *Result) error {
	if result.Data == nil {
		return nil
	}

	// Serialize data to check size
	data, err := json.Marshal(result.Data)
	if err != nil {
		return fmt.Errorf("failed to serialize result: %w", err)
	}

	// Check if result exceeds threshold
	if int64(len(data)) <= e.threshold {
		return nil // Small result, keep inline
	}

	// Check if this is a SQL result (has rows and columns)
	isSQLResult := storage.IsSQLResult(result.Data)
	if e.sqlResultStore != nil && isSQLResult {
		// Store in queryable SQL table
		id := storage.GenerateID()
		ref, err := e.sqlResultStore.Store(id, result.Data)
		if err != nil {
			return fmt.Errorf("failed to store SQL result: %w", err)
		}

		// Get metadata for summary
		meta, _ := e.sqlResultStore.GetMetadata(id)

		// Replace data with reference and summary
		result.DataReference = ref
		summary := fmt.Sprintf("✓ SQL result stored in queryable table: %d rows, %d columns\n\nColumns: %v\n\n💡 To analyze this data, use: query_tool_result(\"%s\", \"SELECT * FROM results LIMIT 20\")\nExamples:\n- Count: query_tool_result(\"%s\", \"SELECT COUNT(*) as count FROM results\")\n- Filter: query_tool_result(\"%s\", \"SELECT * FROM results WHERE column_name LIKE '%%pattern%%'\")\n- Sample: query_tool_result(\"%s\", \"SELECT * FROM results LIMIT 10\")",
			meta.RowCount, meta.ColumnCount, meta.Columns, id, id, id, id)
		result.Data = summary

		return nil
	}

	// Not SQL or no SQL store configured, use shared memory
	if e.sharedMemory == nil {
		return nil // No storage configured
	}

	// Store in shared memory
	id := storage.GenerateID()
	ref, err := e.sharedMemory.Store(id, data, "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to store in shared memory: %w", err)
	}

	// Get metadata to create rich inline summary (like we do for SQL results)
	meta, err := e.sharedMemory.GetMetadata(ref)
	if err != nil {
		// Fallback to simple message if metadata unavailable
		result.DataReference = ref
		result.Data = fmt.Sprintf("[Large data stored in shared memory: %s]", storage.RefToString(ref))
		return nil
	}

	// Replace data with reference and rich metadata summary
	result.DataReference = ref
	result.Data = formatSharedMemoryResultSummary(meta, id)

	return nil
}

// formatSharedMemoryResultSummary creates a rich inline summary with metadata.
// This eliminates the need for a separate get_tool_result call - agents get all context immediately.
func formatSharedMemoryResultSummary(meta *storage.DataMetadata, id string) string {
	var summary strings.Builder

	// Header with data type and size
	summary.WriteString(fmt.Sprintf("✓ Large %s stored in memory: %d bytes (~%d tokens)\n\n",
		meta.DataType, meta.SizeBytes, meta.EstimatedTokens))

	// Preview section
	if meta.Preview != nil && (len(meta.Preview.First5) > 0 || len(meta.Preview.Last5) > 0) {
		summary.WriteString("📋 Preview:\n")
		if len(meta.Preview.First5) > 0 {
			previewJSON, _ := json.MarshalIndent(meta.Preview.First5, "", "  ")
			summary.WriteString(fmt.Sprintf("First 5 items:\n%s\n", string(previewJSON)))
		}
		if len(meta.Preview.Last5) > 0 && meta.DataType == "json_array" {
			previewJSON, _ := json.MarshalIndent(meta.Preview.Last5, "", "  ")
			summary.WriteString(fmt.Sprintf("\nLast 5 items:\n%s\n", string(previewJSON)))
		}
		summary.WriteString("\n")
	}

	// Schema section (if available)
	if meta.Schema != nil {
		switch meta.DataType {
		case "json_object":
			if len(meta.Schema.Fields) > 0 {
				fieldNames := make([]string, 0, len(meta.Schema.Fields))
				for _, field := range meta.Schema.Fields {
					fieldNames = append(fieldNames, fmt.Sprintf("%s (%s)", field.Name, field.Type))
				}
				summary.WriteString(fmt.Sprintf("📊 Schema: %d fields\n%s\n\n",
					len(meta.Schema.Fields), strings.Join(fieldNames, ", ")))
			}
		case "json_array":
			summary.WriteString(fmt.Sprintf("📊 Array: %d items\n", meta.Schema.ItemCount))
			if len(meta.Schema.Fields) > 0 {
				fieldNames := make([]string, 0, len(meta.Schema.Fields))
				for _, field := range meta.Schema.Fields {
					fieldNames = append(fieldNames, fmt.Sprintf("%s (%s)", field.Name, field.Type))
				}
				summary.WriteString(fmt.Sprintf("Item schema: %s\n\n", strings.Join(fieldNames, ", ")))
			}
		case "text":
			summary.WriteString(fmt.Sprintf("📊 Text: %d lines\n\n", meta.Schema.ItemCount))
		}
	}

	// Retrieval hints - how to access this data
	summary.WriteString("💡 How to retrieve:\n")
	switch meta.DataType {
	case "json_object":
		summary.WriteString(fmt.Sprintf("⚠️ This json_object is too large (%d bytes) for direct retrieval\n", meta.SizeBytes))
		summary.WriteString("Use the preview and schema above to understand the structure\n")
		if meta.Schema != nil && len(meta.Schema.Fields) > 0 {
			summary.WriteString("Consider which specific fields you need from the object\n")
		}

	case "json_array":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', offset=0, limit=100)\n", id))
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...')\n", id))
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			summary.WriteString("⚠️ Large dataset - use filtering to avoid context overload\n")
		}

	case "text":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', offset=0, limit=100)\n", id))
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			summary.WriteString(fmt.Sprintf("⚠️ Large file (%d lines) - paginate to avoid loading all at once\n", meta.Schema.ItemCount))
		}

	case "csv":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...')\n", id))
		summary.WriteString("💡 CSV auto-converts to queryable SQLite table\n")
	}

	return summary.String()
}

// estimateValueSize calculates approximate byte size of a parameter value.
// Used to determine if a parameter should be stored in shared memory.
func estimateValueSize(value interface{}) int64 {
	switch v := value.(type) {
	case string:
		return int64(len(v))
	case []byte:
		return int64(len(v))
	case map[string]interface{}, []interface{}:
		// Serialize to estimate size
		data, err := json.Marshal(v)
		if err != nil {
			return 0
		}
		return int64(len(data))
	default:
		// Small primitive types (int, bool, float), don't store
		return 0
	}
}

// handleLargeParameters checks if any parameter values exceed threshold
// and stores them in shared memory, replacing with DataReference objects.
// This prevents massive parameters from bloating context windows.
func (e *Executor) handleLargeParameters(params map[string]interface{}) (map[string]interface{}, error) {
	if e.sharedMemory == nil {
		return params, nil // No storage configured, skip optimization
	}

	result := make(map[string]interface{})
	modified := false

	for key, value := range params {
		size := estimateValueSize(value)

		if size > e.threshold {
			// Store in shared memory
			data, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize large parameter %s: %w", key, err)
			}

			id := storage.GenerateID()
			ref, err := e.sharedMemory.Store(id, data, "application/json", map[string]string{
				"parameter_name": key,
				"original_size":  fmt.Sprintf("%d", size),
				"source":         "parameter_optimization",
			})
			if err != nil {
				return nil, fmt.Errorf("failed to store large parameter %s: %w", key, err)
			}

			// Replace value with DataReference (type-safe, zero collision risk)
			result[key] = ref
			modified = true

			// Metrics: track stored parameter
			e.largeParamStores.Add(1)
			e.largeParamBytesStored.Add(size)
		} else {
			result[key] = value
		}
	}

	if !modified {
		return params, nil // No large params, return original
	}

	return result, nil
}

// dereferenceLargeParameters replaces DataReference objects with actual data.
// This happens transparently before tool execution - tools never see references.
func (e *Executor) dereferenceLargeParameters(params map[string]interface{}) (map[string]interface{}, error) {
	if e.sharedMemory == nil {
		return params, nil
	}

	result := make(map[string]interface{})
	hasRefs := false

	for key, value := range params {
		// Check if value is a DataReference
		if ref, ok := value.(*loomv1.DataReference); ok {
			hasRefs = true

			// Retrieve from shared memory
			data, err := e.sharedMemory.Get(ref)
			if err != nil {
				e.largeParamDerefErrors.Add(1) // Metrics: track error
				return nil, fmt.Errorf("failed to dereference parameter %s: %w", key, err)
			}

			// Deserialize back to original type
			var originalValue interface{}
			if err := json.Unmarshal(data, &originalValue); err != nil {
				e.largeParamDerefErrors.Add(1) // Metrics: track error
				return nil, fmt.Errorf("failed to deserialize parameter %s: %w", key, err)
			}

			result[key] = originalValue
			e.largeParamDerefs.Add(1) // Metrics: track successful dereference
		} else {
			result[key] = value
		}
	}

	if !hasRefs {
		return params, nil // No references, return original
	}

	return result, nil
}

// ListAvailableTools returns all tools available in the executor's registry.
func (e *Executor) ListAvailableTools() []Tool {
	return e.registry.ListTools()
}

// ListToolsByBackend returns all tools for a specific backend.
func (e *Executor) ListToolsByBackend(backend string) []Tool {
	return e.registry.ListByBackend(backend)
}

// ExecutorStats holds metrics about executor operations.
type ExecutorStats struct {
	LargeParamStores      int64 // Count of parameters stored in shared memory
	LargeParamDerefs      int64 // Count of parameters dereferenced
	LargeParamBytesStored int64 // Total bytes stored for parameters
	LargeParamDerefErrors int64 // Count of dereference failures
}

// Stats returns metrics about executor operations.
// Includes large parameter optimization statistics.
func (e *Executor) Stats() ExecutorStats {
	return ExecutorStats{
		LargeParamStores:      e.largeParamStores.Load(),
		LargeParamDerefs:      e.largeParamDerefs.Load(),
		LargeParamBytesStored: e.largeParamBytesStored.Load(),
		LargeParamDerefErrors: e.largeParamDerefErrors.Load(),
	}
}

// normalizeParametersToSchema attempts to normalize parameter names to match the tool's schema.
// This handles the common issue where LLMs use snake_case but tools expect camelCase (or vice versa).
func normalizeParametersToSchema(tool Tool, params map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return params
	}

	schema := tool.InputSchema()
	if schema == nil || schema.Properties == nil {
		return params // No schema to normalize against
	}

	// Build a mapping of lowercase parameter names to actual schema names
	schemaKeys := make(map[string]string)
	for key := range schema.Properties {
		schemaKeys[toLowerUnderscore(key)] = key
	}

	// Normalize incoming parameters
	normalized := make(map[string]interface{}, len(params))
	for key, value := range params {
		// Try to find matching schema key (case-insensitive with underscore normalization)
		normalizedKey := toLowerUnderscore(key)
		if schemaKey, exists := schemaKeys[normalizedKey]; exists {
			// Use the schema's preferred key name
			normalized[schemaKey] = value
		} else {
			// No match found, use original key
			normalized[key] = value
		}
	}

	return normalized
}

// toLowerUnderscore converts any naming convention to lowercase with underscores.
// This allows matching camelCase, snake_case, PascalCase, etc.
func toLowerUnderscore(s string) string {
	if s == "" {
		return ""
	}

	var result []rune
	for i, r := range s {
		// Convert to lowercase
		lower := unicode.ToLower(r)

		// Add underscore before uppercase letters (except first char)
		if i > 0 && unicode.IsUpper(r) {
			result = append(result, '_')
		}

		result = append(result, lower)
	}

	return string(result)
}

// tryDynamicRegistration attempts to dynamically register a tool from the tool registry.
// This enables agents to use tools they discover via tool_search without explicit registration.
// Returns the registered tool, or nil if registration fails or tool not found.
func (e *Executor) tryDynamicRegistration(ctx context.Context, toolName string) (Tool, error) {
	// Check if tool registry is configured
	if e.toolRegistry == nil {
		return nil, fmt.Errorf("tool registry not configured")
	}

	// Search for the tool in the registry
	// Use the exact tool name as the query for a precise match
	resp, err := e.toolRegistry.Search(ctx, &loomv1.SearchToolsRequest{
		Query:         toolName,
		Mode:          loomv1.SearchMode_SEARCH_MODE_FAST, // Use fast mode for keyword match
		MaxResults:    1,
		IncludeSchema: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search tool registry: %w", err)
	}

	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("tool not found in registry")
	}

	toolInfo := resp.Results[0].Tool

	// Handle based on tool source
	switch toolInfo.Source {
	case loomv1.ToolSource_TOOL_SOURCE_MCP:
		return e.registerMCPTool(ctx, toolInfo)
	case loomv1.ToolSource_TOOL_SOURCE_BUILTIN:
		return e.registerBuiltinTool(ctx, toolInfo)
	case loomv1.ToolSource_TOOL_SOURCE_CUSTOM:
		return nil, fmt.Errorf("custom tools not yet supported for dynamic registration")
	default:
		return nil, fmt.Errorf("unknown tool source: %v", toolInfo.Source)
	}
}

// registerMCPTool dynamically registers an MCP tool from the tool registry.
func (e *Executor) registerMCPTool(ctx context.Context, toolInfo *loomv1.IndexedTool) (Tool, error) {
	if e.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager not configured")
	}

	if toolInfo.McpServer == "" {
		return nil, fmt.Errorf("MCP tool missing server name")
	}

	// Get MCP client for the server
	client, err := e.mcpManager.GetClient(toolInfo.McpServer)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP client for server %s: %w", toolInfo.McpServer, err)
	}

	// Parse input schema from JSON string
	var inputSchema *JSONSchema
	if toolInfo.InputSchema != "" {
		if err := json.Unmarshal([]byte(toolInfo.InputSchema), &inputSchema); err != nil {
			// If schema parsing fails, create a basic schema
			inputSchema = &JSONSchema{
				Type:       "object",
				Properties: map[string]*JSONSchema{},
			}
		}
	}

	// Create MCP tool wrapper
	mcpTool := &mcpToolWrapper{
		name:        toolInfo.Name,
		description: toolInfo.Description,
		inputSchema: inputSchema,
		client:      client,
		serverName:  toolInfo.McpServer,
	}

	// Register the tool so subsequent calls don't require dynamic lookup
	e.registry.Register(mcpTool)

	return mcpTool, nil
}

// registerBuiltinTool dynamically registers a builtin tool from the builtin tool provider.
func (e *Executor) registerBuiltinTool(ctx context.Context, toolInfo *loomv1.IndexedTool) (Tool, error) {
	if e.builtinToolProvider == nil {
		return nil, fmt.Errorf("builtin tool provider not configured")
	}

	// Get tool from builtin provider
	tool := e.builtinToolProvider.GetTool(toolInfo.Name)
	if tool == nil {
		return nil, fmt.Errorf("builtin tool not found: %s", toolInfo.Name)
	}

	// Register the tool so subsequent calls don't require dynamic lookup
	e.registry.Register(tool)

	return tool, nil
}

// mcpToolWrapper wraps an MCP client to implement the Tool interface.
type mcpToolWrapper struct {
	name        string
	description string
	inputSchema *JSONSchema
	client      interface{} // MCP client interface
	serverName  string
}

func (t *mcpToolWrapper) Name() string {
	return t.name
}

func (t *mcpToolWrapper) Description() string {
	return t.description
}

func (t *mcpToolWrapper) InputSchema() *JSONSchema {
	return t.inputSchema
}

func (t *mcpToolWrapper) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	// Use the MCP client's CallTool method
	// We use reflection-safe interface matching to avoid import cycles with mcp/protocol package

	// Define interface that matches actual MCP client CallTool signature
	// The actual return type is *protocol.CallToolResult, but we use interface{} to avoid import cycles
	type mcpClient interface {
		CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error)
	}

	client, ok := t.client.(mcpClient)
	if !ok {
		// Debug: Log the actual type we received
		actualType := fmt.Sprintf("%T", t.client)
		return &Result{
			Success: false,
			Error: &Error{
				Code:    "MCP_CLIENT_ERROR",
				Message: fmt.Sprintf("MCP client does not support CallTool method (actual type: %s, server: %s)", actualType, t.serverName),
			},
		}, nil
	}

	result, err := client.CallTool(ctx, t.name, params)
	if err != nil {
		return &Result{
			Success: false,
			Error: &Error{
				Code:      "MCP_EXECUTION_FAILED",
				Message:   fmt.Sprintf("MCP tool execution failed: %v", err),
				Retryable: true,
			},
		}, nil
	}

	// Extract and parse MCP Content if present
	// This handles CallToolResult structures with Content arrays
	cleanData := extractMCPContentData(result)

	return &Result{
		Success: true,
		Data:    cleanData,
	}, nil
}

func (t *mcpToolWrapper) Backend() string {
	return "" // MCP tools don't have a specific backend
}

// extractMCPContentData extracts clean data from MCP CallToolResult structures.
// This handles the Content array format and attempts to parse SQL results directly.
func extractMCPContentData(result interface{}) interface{} {
	if result == nil {
		return nil
	}

	// Try to extract Content field using reflection-safe type assertions
	// MCP CallToolResult has structure like: {Content: [{Type: "text", Text: "..."}], IsError: false}
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		// Not a map, return as-is (might already be clean data)
		return result
	}

	// Check for Content field
	contentRaw, hasContent := resultMap["content"]
	if !hasContent {
		// No Content field, might be clean data already
		return result
	}

	// Content should be an array
	contentArray, ok := contentRaw.([]interface{})
	if !ok || len(contentArray) == 0 {
		return result
	}

	// Handle single text content (most common case)
	if len(contentArray) == 1 {
		contentItem, ok := contentArray[0].(map[string]interface{})
		if !ok {
			return result
		}

		contentType, _ := contentItem["type"].(string)
		if contentType == "text" {
			text, _ := contentItem["text"].(string)

			// Try to parse as JSON - many MCP tools return JSON in text field
			text = strings.TrimSpace(text)
			// Skip any message prefix like "✓ Success\n\n{...}"
			jsonStart := strings.Index(text, "{")
			if jsonStart >= 0 {
				jsonText := text[jsonStart:]
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(jsonText), &parsed); err == nil {
					// Successfully parsed JSON - check if it's SQL result
					if hasColumns, hasRows := parsed["columns"], parsed["rows"]; hasColumns != nil && hasRows != nil {
						// This is a SQL result! Return the clean parsed structure
						return parsed
					}
					if hasColumns, hasRows := parsed["Columns"], parsed["Rows"]; hasColumns != nil && hasRows != nil {
						// Capitalized version
						return parsed
					}
					// Not SQL, but valid JSON - return parsed
					return parsed
				}
			}

			// Not JSON or parsing failed, return text as-is
			return text
		}
	}

	// Multiple content items or non-text - return structured Content array
	results := make([]map[string]interface{}, len(contentArray))
	for i, c := range contentArray {
		if contentItem, ok := c.(map[string]interface{}); ok {
			results[i] = contentItem
		}
	}
	return results
}
