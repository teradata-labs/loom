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
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

// BuiltinIndexer indexes builtin tools from pkg/shuttle.
type BuiltinIndexer struct {
	tools  []shuttle.Tool
	tracer observability.Tracer
}

// NewBuiltinIndexer creates a new builtin tool indexer.
// If no tools provided, it will use builtin.All(nil) to get all builtin tools.
func NewBuiltinIndexer(tracer observability.Tracer, tools ...shuttle.Tool) *BuiltinIndexer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &BuiltinIndexer{
		tools:  tools,
		tracer: tracer,
	}
}

// Name returns the indexer name.
func (i *BuiltinIndexer) Name() string {
	return "builtin"
}

// Source returns the tool source type.
func (i *BuiltinIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource_TOOL_SOURCE_BUILTIN
}

// Index indexes all builtin tools.
func (i *BuiltinIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	_, span := i.tracer.StartSpan(ctx, "tools.indexer.builtin.index")
	defer i.tracer.EndSpan(span)

	var tools []*loomv1.IndexedTool

	// Use provided tools or get builtin tools from builtin.All()
	builtinTools := i.tools
	if len(builtinTools) == 0 {
		builtinTools = builtin.All(nil)
	}

	for _, t := range builtinTools {
		// Convert InputSchema to JSON string
		inputSchema := ""
		if schema := t.InputSchema(); schema != nil {
			schemaBytes, _ := json.Marshal(schema)
			inputSchema = string(schemaBytes)
		}

		tool := &loomv1.IndexedTool{
			Id:           fmt.Sprintf("builtin:%s", t.Name()),
			Name:         t.Name(),
			Description:  t.Description(),
			Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
			InputSchema:  inputSchema,
			IndexedAt:    time.Now().Format(time.RFC3339),
			Capabilities: extractCapabilities(t.Name(), t.Description()),
			Keywords:     extractKeywords(t.Name(), t.Description()),
		}

		// Check if tool requires approval
		name := strings.ToLower(t.Name())
		if strings.Contains(name, "bash") ||
			strings.Contains(name, "exec") ||
			strings.Contains(name, "write") {
			tool.RequiresApproval = true
		}

		tools = append(tools, tool)
	}

	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Indexed %d builtin tools", len(tools)),
	}

	return tools, nil
}

// MCPIndexer indexes tools from MCP servers.
type MCPIndexer struct {
	manager *manager.Manager
	tracer  observability.Tracer
}

// NewMCPIndexer creates a new MCP tool indexer.
func NewMCPIndexer(mgr *manager.Manager, tracer observability.Tracer) *MCPIndexer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &MCPIndexer{
		manager: mgr,
		tracer:  tracer,
	}
}

// Name returns the indexer name.
func (i *MCPIndexer) Name() string {
	return "mcp"
}

// Source returns the tool source type.
func (i *MCPIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource_TOOL_SOURCE_MCP
}

// Index indexes tools from all connected MCP servers.
func (i *MCPIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	ctx, span := i.tracer.StartSpan(ctx, "tools.indexer.mcp.index")
	defer i.tracer.EndSpan(span)

	if i.manager == nil {
		return nil, nil
	}

	var allTools []*loomv1.IndexedTool

	// Get all connected servers
	servers := i.manager.ListServers()

	for _, serverInfo := range servers {
		serverName := serverInfo.Name

		// Get client for this server
		mcpClient, err := i.manager.GetClient(serverName)
		if err != nil {
			// Server not connected, skip
			continue
		}

		// Get tools from this server
		mcpTools, err := mcpClient.ListTools(ctx)
		if err != nil {
			// Log but continue with other servers
			continue
		}

		for _, mcpTool := range mcpTools {
			// Convert input schema to JSON string
			inputSchema := ""
			if mcpTool.InputSchema != nil {
				schemaBytes, _ := json.Marshal(mcpTool.InputSchema)
				inputSchema = string(schemaBytes)
			}

			tool := &loomv1.IndexedTool{
				Id:           fmt.Sprintf("mcp:%s:%s", serverName, mcpTool.Name),
				Name:         mcpTool.Name,
				Description:  mcpTool.Description,
				Source:       loomv1.ToolSource_TOOL_SOURCE_MCP,
				McpServer:    serverName,
				InputSchema:  inputSchema,
				IndexedAt:    time.Now().Format(time.RFC3339),
				Capabilities: extractCapabilities(mcpTool.Name, mcpTool.Description),
				Keywords:     extractKeywords(mcpTool.Name, mcpTool.Description),
			}

			allTools = append(allTools, tool)
		}
	}

	span.Status = observability.Status{
		Code:    observability.StatusOK,
		Message: fmt.Sprintf("Indexed %d MCP tools from %d servers", len(allTools), len(servers)),
	}

	return allTools, nil
}

// CustomIndexer indexes custom tool definitions from YAML files.
type CustomIndexer struct {
	configPath string
	tracer     observability.Tracer
}

// NewCustomIndexer creates a new custom tool indexer.
func NewCustomIndexer(configPath string, tracer observability.Tracer) *CustomIndexer {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &CustomIndexer{
		configPath: configPath,
		tracer:     tracer,
	}
}

// Name returns the indexer name.
func (i *CustomIndexer) Name() string {
	return "custom"
}

// Source returns the tool source type.
func (i *CustomIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource_TOOL_SOURCE_CUSTOM
}

// Index indexes custom tool definitions.
func (i *CustomIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	// TODO: Implement custom tool indexing from YAML config files
	// For now, return empty list
	return nil, nil
}

// extractCapabilities extracts capability tags from tool name and description.
func extractCapabilities(name, description string) []string {
	caps := make(map[string]bool)
	combined := strings.ToLower(name + " " + description)

	// Capability detection rules
	capRules := map[string][]string{
		"file_io":       {"file", "read", "write", "path", "directory"},
		"http":          {"http", "request", "api", "fetch", "url", "endpoint"},
		"database":      {"database", "sql", "query", "postgres", "mysql", "sqlite"},
		"notification":  {"notify", "alert", "slack", "email", "message", "send"},
		"shell":         {"bash", "shell", "command", "exec", "terminal"},
		"search":        {"search", "find", "lookup", "query", "filter"},
		"transform":     {"transform", "convert", "parse", "format", "encode", "decode"},
		"validate":      {"validate", "check", "verify", "lint", "test"},
		"generate":      {"generate", "create", "build", "make", "produce"},
		"analyze":       {"analyze", "inspect", "examine", "review", "audit"},
		"web_search":    {"web", "search", "google", "tavily", "browse"},
		"code":          {"code", "function", "class", "method", "syntax"},
		"git":           {"git", "commit", "branch", "merge", "pull", "push"},
		"kubernetes":    {"kubernetes", "k8s", "pod", "deployment", "container"},
		"aws":           {"aws", "s3", "lambda", "ec2", "dynamodb"},
		"visualization": {"chart", "graph", "plot", "visualize", "diagram"},
	}

	for cap, keywords := range capRules {
		for _, kw := range keywords {
			if strings.Contains(combined, kw) {
				caps[cap] = true
				break
			}
		}
	}

	result := make([]string, 0, len(caps))
	for cap := range caps {
		result = append(result, cap)
	}

	return result
}

// extractKeywords extracts search keywords from tool name and description.
func extractKeywords(name, description string) []string {
	// Combine and lowercase
	combined := strings.ToLower(name + " " + description)

	// Remove common stop words
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "this": true, "that": true, "it": true, "as": true,
		"by": true, "from": true, "at": true, "can": true, "will": true,
	}

	// Split into words
	words := strings.FieldsFunc(combined, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
	})

	// Filter and deduplicate
	seen := make(map[string]bool)
	var keywords []string

	for _, word := range words {
		if len(word) < 3 {
			continue
		}
		if stopWords[word] {
			continue
		}
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)
	}

	// Limit to 20 keywords
	if len(keywords) > 20 {
		keywords = keywords[:20]
	}

	return keywords
}
