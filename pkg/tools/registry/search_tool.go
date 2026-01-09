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
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// SearchTool is a builtin tool that enables agents to search for tools.
// It provides LLM-assisted tool discovery with high accuracy.
type SearchTool struct {
	registry *Registry
}

// NewSearchTool creates a new tool search tool.
func NewSearchTool(registry *Registry) *SearchTool {
	return &SearchTool{
		registry: registry,
	}
}

// Name returns the tool name.
func (t *SearchTool) Name() string {
	return "tool_search"
}

// Description returns the tool description.
func (t *SearchTool) Description() string {
	return `Search for available tools based on natural language queries.
Use this tool when you need to find tools to accomplish a task but don't know what tools are available.
The search uses LLM-assisted ranking for high accuracy.

Examples:
- "send a notification to Slack"
- "query a PostgreSQL database"
- "read and parse JSON files"
- "make HTTP API requests"

Returns a list of matching tools with confidence scores, descriptions, and input schemas.`
}

// InputSchema returns the JSON schema for tool parameters.
func (t *SearchTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"query": {
				Type:        "string",
				Description: "Natural language description of what you want to do. Be specific about the task.",
			},
			"mode": {
				Type:        "string",
				Description: "Search accuracy mode: 'fast' (keyword only), 'balanced' (default, FTS + LLM re-ranking), 'accurate' (full LLM pipeline)",
				Enum:        []interface{}{"fast", "balanced", "accurate"},
			},
			"capabilities": {
				Type:        "array",
				Description: "Optional capability filters to narrow results (e.g., ['notification', 'database'])",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"max_results": {
				Type:        "integer",
				Description: "Maximum number of results to return (default: 5, max: 10)",
			},
			"task_context": {
				Type:        "string",
				Description: "Optional context about your current task to improve ranking accuracy",
			},
		},
		Required: []string{"query"},
	}
}

// Execute searches for tools matching the query.
func (t *SearchTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Parse parameters
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_QUERY",
				Message: "query parameter is required and must be a non-empty string",
			},
		}, nil
	}

	// Parse mode
	mode := loomv1.SearchMode_SEARCH_MODE_BALANCED
	if modeStr, ok := params["mode"].(string); ok {
		switch modeStr {
		case "fast":
			mode = loomv1.SearchMode_SEARCH_MODE_FAST
		case "balanced":
			mode = loomv1.SearchMode_SEARCH_MODE_BALANCED
		case "accurate":
			mode = loomv1.SearchMode_SEARCH_MODE_ACCURATE
		}
	}

	// Parse capabilities filter
	var capabilities []string
	if caps, ok := params["capabilities"].([]interface{}); ok {
		for _, c := range caps {
			if s, ok := c.(string); ok {
				capabilities = append(capabilities, s)
			}
		}
	}

	// Parse max results
	maxResults := int32(5)
	if mr, ok := params["max_results"].(float64); ok {
		maxResults = int32(mr)
		if maxResults > 10 {
			maxResults = 10
		}
		if maxResults < 1 {
			maxResults = 1
		}
	}

	// Parse task context
	taskContext := ""
	if tc, ok := params["task_context"].(string); ok {
		taskContext = tc
	}

	// Perform search
	resp, err := t.registry.Search(ctx, &loomv1.SearchToolsRequest{
		Query:             query,
		Mode:              mode,
		CapabilityFilters: capabilities,
		MaxResults:        maxResults,
		IncludeSchema:     true,
		TaskContext:       taskContext,
	})
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:      "SEARCH_FAILED",
				Message:   fmt.Sprintf("Tool search failed: %v", err),
				Retryable: true,
			},
		}, nil
	}

	// Format results for LLM consumption
	results := make([]map[string]interface{}, 0, len(resp.Results))
	for _, r := range resp.Results {
		result := map[string]interface{}{
			"name":        r.Tool.Name,
			"description": r.Tool.Description,
			"confidence":  fmt.Sprintf("%.0f%%", r.Confidence*100),
			"source":      r.Tool.Source.String(),
		}

		if r.MatchReason != "" {
			result["match_reason"] = r.MatchReason
		}

		if r.Tool.McpServer != "" {
			result["mcp_server"] = r.Tool.McpServer
		}

		if len(r.Tool.Capabilities) > 0 {
			result["capabilities"] = r.Tool.Capabilities
		}

		// Parse and include simplified input schema
		if r.Tool.InputSchema != "" {
			var schema map[string]interface{}
			if json.Unmarshal([]byte(r.Tool.InputSchema), &schema) == nil {
				// Simplify schema for LLM readability
				if props, ok := schema["properties"].(map[string]interface{}); ok {
					simplified := make(map[string]string)
					for name, prop := range props {
						if propMap, ok := prop.(map[string]interface{}); ok {
							desc := ""
							if d, ok := propMap["description"].(string); ok {
								desc = d
							}
							typeStr := "any"
							if t, ok := propMap["type"].(string); ok {
								typeStr = t
							}
							simplified[name] = fmt.Sprintf("%s (%s)", desc, typeStr)
						}
					}
					result["parameters"] = simplified
				}
			}
		}

		if r.Tool.RequiresApproval {
			result["requires_approval"] = true
		}

		results = append(results, result)
	}

	// Build response data
	data := map[string]interface{}{
		"query":         query,
		"results_count": len(results),
		"results":       results,
		"search_mode":   mode.String(),
	}

	if resp.Metadata != nil {
		data["search_time_ms"] = resp.Metadata.TotalMs
		data["total_tools_indexed"] = resp.Metadata.TotalIndexed
	}

	return &shuttle.Result{
		Success:         true,
		Data:            data,
		ExecutionTimeMs: time.Since(start).Milliseconds(),
		Metadata: map[string]interface{}{
			"candidates_retrieved": resp.Metadata.CandidatesRetrieved,
			"mode_used":            resp.Metadata.ModeUsed.String(),
		},
	}, nil
}

// Backend returns empty string as this tool is backend-agnostic.
func (t *SearchTool) Backend() string {
	return ""
}

// Ensure SearchTool implements shuttle.Tool
var _ shuttle.Tool = (*SearchTool)(nil)
