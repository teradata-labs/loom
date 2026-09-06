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
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// debugBedrockTools is cached at package init to avoid repeated os.Getenv calls.
var debugBedrockTools = os.Getenv("LOOM_DEBUG_BEDROCK_TOOLS") == "1"

// Schema caching configuration
const (
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

// MCPToolAdapter wraps an MCP tool as a shuttle.Tool
type MCPToolAdapter struct {
	client        *client.Client
	tool          protocol.Tool
	serverName    string      // Used as backend identifier
	uiResourceURI string      // From tool._meta.ui.resourceUri (MCP Apps)
	logger        *zap.Logger // Structured logger (defaults to no-op)

	// paramNameMap maps the LLM-visible property name (snake_case, as
	// presented by InputSchema) back to the server's ORIGINAL property name
	// from the tool's own schema. Execute renames via this map only — a key
	// with no entry passes through unchanged, so the adapter never invents a
	// casing the server's schema doesn't contain (issue #339).
	paramNameMap map[string]string
}

// NewMCPToolAdapter creates a new adapter that wraps an MCP tool
func NewMCPToolAdapter(client *client.Client, tool protocol.Tool, serverName string) *MCPToolAdapter {
	adapter := &MCPToolAdapter{
		client:       client,
		tool:         tool,
		serverName:   serverName,
		logger:       zap.NewNop(), // No-op by default; use SetLogger to enable
		paramNameMap: buildParamNameMap(tool.InputSchema),
	}

	// Extract UI metadata from tool._meta.ui if present (MCP Apps)
	if uiMeta := protocol.GetUIToolMeta(tool); uiMeta != nil {
		adapter.uiResourceURI = uiMeta.ResourceURI
	}

	return adapter
}

// buildParamNameMap derives the snake_case → original-name mapping from the
// tool's input schema, recording only entries where the LLM-visible name
// differs from the server's. For a snake_case schema the map is empty
// (identity); for a camelCase schema it restores the original names exactly.
func buildParamNameMap(inputSchema map[string]interface{}) map[string]string {
	props, ok := inputSchema["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	m := make(map[string]string)
	ambiguous := map[string]bool{}
	for original := range props {
		visible := toSnakeCase(original)
		if visible == original {
			// An identity property claims its own name: a differently-cased
			// sibling that collapses to it must not shadow it.
			ambiguous[visible] = true
			delete(m, visible)
			continue
		}
		if _, taken := m[visible]; taken || ambiguous[visible] {
			// Two original properties collapse to one visible name
			// (e.g. tableName + table_name). Renaming would be a
			// nondeterministic guess; ambiguous names pass through as-is.
			ambiguous[visible] = true
			delete(m, visible)
			continue
		}
		m[visible] = original
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// restoreParameterNames renames each parameter from its LLM-visible name back
// to the server's original schema property name via paramNameMap. Keys without
// a mapping pass through unchanged.
func (a *MCPToolAdapter) restoreParameterNames(params map[string]interface{}) map[string]interface{} {
	if params == nil || len(a.paramNameMap) == 0 {
		return params
	}
	restored := make(map[string]interface{}, len(params))
	for key, value := range params {
		if original, ok := a.paramNameMap[key]; ok {
			restored[original] = value
		} else {
			restored[key] = value
		}
	}
	return restored
}

// SetLogger configures the structured logger for this adapter.
// If not called, a no-op logger is used.
func (a *MCPToolAdapter) SetLogger(logger *zap.Logger) {
	if logger != nil {
		a.logger = logger
	}
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

	// Restore the server's original parameter names. InputSchema presents
	// properties to the LLM in snake_case; this maps each name back to the
	// exact property name in the tool's schema. A blind snake→camel
	// conversion here corrupted calls to snake_case servers (issue #339):
	// "table_name" became "tableName", which strict servers reject and
	// lenient servers silently drop.
	restoredParams := a.restoreParameterNames(params)

	// Check schema cache for schema-related tools (#4: Schema Caching)
	if a.isSchemaLookupTool() {
		cacheKey := a.buildSchemaCacheKey(restoredParams)
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
	mcpResultInterface, err := a.client.CallTool(ctx, a.tool.Name, restoredParams)
	if err != nil {
		// The mechanism is chosen once, on the first error: a resource-wait
		// retry that then fails with a backpressure hint (or a freeze
		// re-invoke that fails with a resource link) surfaces that second
		// error to the model rather than chaining waits — deliberate
		// recursion bounding, one wait mechanism per tool call.
		if isBackpressure(err) {
			// Backpressure freeze (issue #354): capacity flow control never
			// reaches the model — the call re-invokes, parked server-side
			// via the error's wait_param when named, until capacity frees
			// or the conversation's deadline expires.
			mcpResultInterface, err = a.awaitBackpressure(ctx, restoredParams, err)
		} else {
			// Park-and-wake (issue #343): a failure that links a resource
			// parks here and retries when the resource updates; otherwise
			// unchanged.
			mcpResultInterface, err = a.awaitLinkedResource(ctx, restoredParams, err)
		}
	}
	executionTime := time.Since(startTime).Milliseconds()

	if err != nil {
		// Convert error to shuttle.Result with error
		shuttleErr := &shuttle.Error{
			Code:       "MCP_CALL_FAILED",
			Message:    err.Error(),
			Retryable:  true,
			Suggestion: "Check MCP server logs for details",
		}
		// A capacity condition that outlived the freeze (budget exhausted,
		// or a hint that arrived with no wait mechanism) carries its hint
		// onto the generic contract, so consumers outside pkg/mcp — the
		// agent loop, observability — read flow control without knowing MCP.
		var terr *client.ToolResultError
		if errors.As(err, &terr) {
			if hint := terr.Backpressure(); hint != nil {
				shuttleErr.SetBackpressure(shuttle.BackpressureHint{
					Code:        hint.Code,
					RetryAfterS: hint.RetryAfterS,
					WaitParam:   hint.WaitParam,
					MaxWaitS:    hint.MaxWaitS,
				})
			}
		}
		return &shuttle.Result{
			Success:         false,
			Error:           shuttleErr,
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

	// Convert MCP content to shuttle result data. The result rides WHOLE —
	// arrival appends whole (ContextCompilation HLD §4): the compile-time
	// offload, the persist-time row bound and the retrieval page bound are the
	// only size logic, so no second bound may cut the payload upstream here.
	data := convertMCPContent(mcpResult.Content)

	// Session-handle lifecycle (issue #345): collect minted handles for
	// end-of-conversation auto-release; drop ones the agent released itself.
	// The events ride out on the Result so the agent's lease ledger and the
	// LLM slot scheduler learn about the lease generically.
	leaseEvents := trackSessionHandles(ctx, a, params, data)

	// Cache schema results (#4: Schema Caching)
	if a.isSchemaLookupTool() {
		cacheKey := a.buildSchemaCacheKey(restoredParams)
		if str, ok := data.(string); ok {
			globalSchemaCache.set(cacheKey, str)
		}
	}

	metadata := map[string]interface{}{
		"mcp_server": a.serverName,
		"tool_name":  a.tool.Name,
	}

	result := &shuttle.Result{
		Success:         true,
		Data:            data,
		ExecutionTimeMs: executionTime,
		Metadata:        metadata,
	}
	for _, ev := range leaseEvents {
		shuttle.AppendLeaseEvent(result, ev)
	}
	return result, nil
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
		case "resource_link":
			// A reference to a server resource without its contents
			// (2025-06-18+): preserve uri/name so the agent can see and act
			// on the link instead of receiving an empty content item.
			item["uri"] = c.URI
			if c.Name != "" {
				item["name"] = c.Name
			}
			if c.MimeType != "" {
				item["mimeType"] = c.MimeType
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
// Helper methods for schema caching
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
