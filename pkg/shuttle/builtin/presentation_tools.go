// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/visualization"
)

// TopNQueryTool provides Top-N query capability for agents.
// Executes TOP N pattern on data from shared memory (e.g., Stage 9 results).
// Pattern: Get top N items sorted by a column (frequency, score, etc.)
type TopNQueryTool struct {
	store   *communication.SharedMemoryStore
	agentID string
}

// NewTopNQueryTool creates a new Top-N query tool.
func NewTopNQueryTool(store *communication.SharedMemoryStore, agentID string) *TopNQueryTool {
	return &TopNQueryTool{
		store:   store,
		agentID: agentID,
	}
}

func (t *TopNQueryTool) Name() string {
	return "top_n_query"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/presentation.yaml).
// This fallback is used only when prompts are not configured.
func (t *TopNQueryTool) Description() string {
	return `Queries shared memory data and returns the top N items sorted by a column.

This implements the TOP-N presentation strategy:
- Reduces large datasets to the most important items
- Sorts by frequency, score, importance, or any numeric column
- Returns structured results ready for analysis or visualization

Example: Get top 50 nPath patterns sorted by frequency
Example: Get top 20 customers by churn risk score

Use this when you need:
- The highest/lowest N items from a dataset
- Most frequent patterns or categories
- Top performers or outliers

This tool queries data stored in shared_memory by other agents (e.g., Stage 9).`
}

func (t *TopNQueryTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for Top-N query",
		map[string]*shuttle.JSONSchema{
			"source_key": shuttle.NewStringSchema("Shared memory key containing the dataset (required)"),
			"n":          shuttle.NewNumberSchema("Number of top items to return (required, default: 10, range: 1-1000)"),
			"sort_by":    shuttle.NewStringSchema("Column name to sort by (required, e.g., 'frequency', 'score')"),
			"direction": shuttle.NewStringSchema("Sort direction: 'asc' or 'desc' (default: desc)").
				WithEnum("asc", "desc").
				WithDefault("desc"),
			"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', or 'swarm' (default: workflow)").
				WithEnum("global", "workflow", "swarm").
				WithDefault("workflow"),
		},
		[]string{"source_key", "n", "sort_by"},
	)
}

func (t *TopNQueryTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate store availability
	if t.store == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "STORE_NOT_AVAILABLE",
				Message:    "Shared memory store not configured",
				Suggestion: "Presentation tools require MultiAgentServer with shared memory configured",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract parameters
	sourceKey, ok := params["source_key"].(string)
	if !ok || sourceKey == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "source_key is required",
				Suggestion: "Provide the shared memory key containing the dataset (e.g., 'stage-9-npath-full-results')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	n := 10
	if nVal, ok := params["n"].(float64); ok {
		n = int(nVal)
	} else if nVal, ok := params["n"].(int); ok {
		n = nVal
	}
	if n < 1 || n > 1000 {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "n must be between 1 and 1000",
				Suggestion: "Provide a valid number of top items to return",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	sortBy, ok := params["sort_by"].(string)
	if !ok || sortBy == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "sort_by is required",
				Suggestion: "Specify the column to sort by (e.g., 'frequency', 'score', 'count')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	direction := "desc"
	if dir, ok := params["direction"].(string); ok && dir != "" {
		direction = dir
	}

	namespaceStr := "workflow"
	if ns, ok := params["namespace"].(string); ok && ns != "" {
		namespaceStr = ns
	}

	// Parse namespace
	var namespace loomv1.SharedMemoryNamespace
	switch namespaceStr {
	case "global":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL
	case "workflow":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	case "swarm":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM
	default:
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	}

	// Read from shared memory
	req := &loomv1.GetSharedMemoryRequest{
		Namespace: namespace,
		Key:       sourceKey,
		AgentId:   t.agentID,
	}

	resp, err := t.store.Get(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "READ_FAILED",
				Message:    fmt.Sprintf("Failed to read from shared memory: %v", err),
				Retryable:  true,
				Suggestion: "Check if source_key exists and shared memory store is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if !resp.Found {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "KEY_NOT_FOUND",
				Message:    fmt.Sprintf("Key not found in shared memory: %s", sourceKey),
				Suggestion: "Check if the source agent has written data to shared memory yet",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Parse JSON data
	var data interface{}
	if err := json.Unmarshal(resp.Value.Value, &data); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_DATA_FORMAT",
				Message:    fmt.Sprintf("Failed to parse data as JSON: %v", err),
				Suggestion: "Ensure source data is valid JSON",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract array of items
	var items []map[string]interface{}
	switch d := data.(type) {
	case []interface{}:
		for _, item := range d {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}
	case map[string]interface{}:
		// If data is a map with array values, try to extract arrays
		for _, v := range d {
			if arr, ok := v.([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						items = append(items, m)
					}
				}
			}
		}
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSUPPORTED_DATA_STRUCTURE",
				Message:    "Data must be an array of objects or a map containing arrays",
				Suggestion: "Ensure source data is structured as JSON array or map with array values",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if len(items) == 0 {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"items":      []interface{}{},
				"total":      0,
				"returned":   0,
				"sort_by":    sortBy,
				"direction":  direction,
				"source_key": sourceKey,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Sort items by sort_by column
	sort.Slice(items, func(i, j int) bool {
		iVal, iOk := getNumericValue(items[i][sortBy])
		jVal, jOk := getNumericValue(items[j][sortBy])

		if !iOk || !jOk {
			return false
		}

		if direction == "asc" {
			return iVal < jVal
		}
		return iVal > jVal
	})

	// Take top N
	topN := n
	if len(items) < topN {
		topN = len(items)
	}
	topItems := items[:topN]

	// Build result
	result := map[string]interface{}{
		"items":      topItems,
		"total":      len(items),
		"returned":   topN,
		"sort_by":    sortBy,
		"direction":  direction,
		"source_key": sourceKey,
		"namespace":  namespaceStr,
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"strategy":   "top_n",
			"total":      len(items),
			"returned":   topN,
			"source_key": sourceKey,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *TopNQueryTool) Backend() string {
	return "" // Backend-agnostic
}

// GroupByQueryTool provides GROUP BY aggregation capability for agents.
// Executes GROUP BY pattern on data from shared memory.
// Pattern: Aggregate data by dimensions with sum/count/avg functions.
type GroupByQueryTool struct {
	store   *communication.SharedMemoryStore
	agentID string
}

// NewGroupByQueryTool creates a new GROUP BY query tool.
func NewGroupByQueryTool(store *communication.SharedMemoryStore, agentID string) *GroupByQueryTool {
	return &GroupByQueryTool{
		store:   store,
		agentID: agentID,
	}
}

func (t *GroupByQueryTool) Name() string {
	return "group_by_query"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/presentation.yaml).
// This fallback is used only when prompts are not configured.
func (t *GroupByQueryTool) Description() string {
	return `Queries shared memory data and aggregates by one or more dimensions.

This implements the GROUP BY presentation strategy:
- Groups data by categorical columns (e.g., customer_segment, region, product)
- Computes aggregates: COUNT(*), SUM(measure), AVG(measure)
- Returns summary statistics per group

Example: Group nPath patterns by path length, count frequency
Example: Group customers by segment, average churn score

Use this when you need:
- Aggregate statistics by category
- Distribution across dimensions
- Summary reports by grouping

This tool queries data stored in shared_memory by other agents (e.g., Stage 9).`
}

func (t *GroupByQueryTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for GROUP BY query",
		map[string]*shuttle.JSONSchema{
			"source_key": shuttle.NewStringSchema("Shared memory key containing the dataset (required)"),
			"group_by": shuttle.NewArraySchema(
				"Column names to group by (required, e.g., ['segment', 'region'])",
				shuttle.NewStringSchema("Column name"),
			),
			"aggregates": shuttle.NewArraySchema(
				"Aggregations to compute (optional, e.g., [{'function': 'sum', 'column': 'revenue'}])",
				shuttle.NewObjectSchema(
					"Aggregate function",
					map[string]*shuttle.JSONSchema{
						"function": shuttle.NewStringSchema("Aggregate function: 'count', 'sum', 'avg', 'min', 'max'").
							WithEnum("count", "sum", "avg", "min", "max"),
						"column": shuttle.NewStringSchema("Column to aggregate (required for sum/avg/min/max)"),
					},
					[]string{"function"},
				),
			),
			"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', or 'swarm' (default: workflow)").
				WithEnum("global", "workflow", "swarm").
				WithDefault("workflow"),
		},
		[]string{"source_key", "group_by"},
	)
}

func (t *GroupByQueryTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate store availability
	if t.store == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "STORE_NOT_AVAILABLE",
				Message:    "Shared memory store not configured",
				Suggestion: "Presentation tools require MultiAgentServer with shared memory configured",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract parameters
	sourceKey, ok := params["source_key"].(string)
	if !ok || sourceKey == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "source_key is required",
				Suggestion: "Provide the shared memory key containing the dataset",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	groupByRaw, ok := params["group_by"].([]interface{})
	if !ok || len(groupByRaw) == 0 {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "group_by is required and must be a non-empty array",
				Suggestion: "Provide columns to group by (e.g., ['customer_segment', 'region'])",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert group_by to []string
	var groupBy []string
	for _, g := range groupByRaw {
		if gStr, ok := g.(string); ok {
			groupBy = append(groupBy, gStr)
		}
	}

	namespaceStr := "workflow"
	if ns, ok := params["namespace"].(string); ok && ns != "" {
		namespaceStr = ns
	}

	// Parse namespace
	var namespace loomv1.SharedMemoryNamespace
	switch namespaceStr {
	case "global":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL
	case "workflow":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	case "swarm":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM
	default:
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	}

	// Read from shared memory
	req := &loomv1.GetSharedMemoryRequest{
		Namespace: namespace,
		Key:       sourceKey,
		AgentId:   t.agentID,
	}

	resp, err := t.store.Get(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "READ_FAILED",
				Message:    fmt.Sprintf("Failed to read from shared memory: %v", err),
				Retryable:  true,
				Suggestion: "Check if source_key exists and shared memory store is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if !resp.Found {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "KEY_NOT_FOUND",
				Message:    fmt.Sprintf("Key not found in shared memory: %s", sourceKey),
				Suggestion: "Check if the source agent has written data to shared memory yet",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Parse JSON data
	var data interface{}
	if err := json.Unmarshal(resp.Value.Value, &data); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_DATA_FORMAT",
				Message:    fmt.Sprintf("Failed to parse data as JSON: %v", err),
				Suggestion: "Ensure source data is valid JSON",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract array of items
	var items []map[string]interface{}
	switch d := data.(type) {
	case []interface{}:
		for _, item := range d {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}
	case map[string]interface{}:
		// If data is a map with array values, try to extract arrays
		for _, v := range d {
			if arr, ok := v.([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						items = append(items, m)
					}
				}
			}
		}
	}

	if len(items) == 0 {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"groups":     []interface{}{},
				"group_by":   groupBy,
				"total_rows": 0,
				"source_key": sourceKey,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Group items
	groups := make(map[string][]map[string]interface{})
	for _, item := range items {
		// Build group key from group_by columns
		var keyParts []string
		for _, col := range groupBy {
			if val, ok := item[col]; ok {
				keyParts = append(keyParts, fmt.Sprintf("%v", val))
			} else {
				keyParts = append(keyParts, "NULL")
			}
		}
		// Create composite key (e.g., "[segment:enterprise region:us]")
		groupKey := fmt.Sprintf("%v", keyParts)
		groups[groupKey] = append(groups[groupKey], item)
	}

	// Build result with counts
	var results []map[string]interface{}
	for _, groupItems := range groups {
		result := make(map[string]interface{})

		// Add group dimensions
		for i, col := range groupBy {
			if i < len(groupItems) && len(groupItems) > 0 {
				result[col] = groupItems[0][col]
			}
		}

		// Add count
		result["count"] = len(groupItems)

		// Calculate aggregates for numeric columns
		// Find all numeric columns (not in group_by)
		if len(groupItems) > 0 {
			firstItem := groupItems[0]
			for col, val := range firstItem {
				// Skip group_by columns
				isGroupCol := false
				for _, gb := range groupBy {
					if col == gb {
						isGroupCol = true
						break
					}
				}
				if isGroupCol {
					continue
				}

				// Check if column is numeric
				switch val.(type) {
				case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
					// Calculate aggregates
					var sum, min, max float64
					count := 0

					for _, item := range groupItems {
						if v, ok := item[col]; ok {
							var numVal float64
							switch typed := v.(type) {
							case int:
								numVal = float64(typed)
							case int64:
								numVal = float64(typed)
							case float64:
								numVal = typed
							case float32:
								numVal = float64(typed)
							default:
								continue
							}

							if count == 0 {
								min = numVal
								max = numVal
							} else {
								if numVal < min {
									min = numVal
								}
								if numVal > max {
									max = numVal
								}
							}
							sum += numVal
							count++
						}
					}

					if count > 0 {
						result[col+"_sum"] = sum
						result[col+"_avg"] = sum / float64(count)
						result[col+"_min"] = min
						result[col+"_max"] = max
					}
				}
			}
		}

		results = append(results, result)
	}

	// Sort by count descending
	sort.Slice(results, func(i, j int) bool {
		iCount, _ := results[i]["count"].(int)
		jCount, _ := results[j]["count"].(int)
		return iCount > jCount
	})

	// Build final result
	finalResult := map[string]interface{}{
		"groups":     results,
		"group_by":   groupBy,
		"total_rows": len(items),
		"num_groups": len(groups),
		"source_key": sourceKey,
		"namespace":  namespaceStr,
	}

	return &shuttle.Result{
		Success: true,
		Data:    finalResult,
		Metadata: map[string]interface{}{
			"strategy":   "group_by",
			"total_rows": len(items),
			"num_groups": len(groups),
			"source_key": sourceKey,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *GroupByQueryTool) Backend() string {
	return "" // Backend-agnostic
}

// Helper function to extract numeric value from interface{}
func getNumericValue(val interface{}) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	default:
		return 0, false
	}
}

// PresentationTools creates presentation strategy tools for an agent.
// These tools allow agents to query shared memory data using SQL-inspired patterns
// (Top-N, GROUP BY, etc.) without writing raw SQL.
//
// Note: Visualization tools (generate_workflow_visualization, generate_visualization)
// are NOT included here by default. They should be explicitly assigned by the metaagent
// when needed. Use VisualizationTools() to get them.
func PresentationTools(store *communication.SharedMemoryStore, agentID string) []shuttle.Tool {
	tools := make([]shuttle.Tool, 0, 2)

	if store != nil {
		tools = append(tools,
			NewTopNQueryTool(store, agentID),
			NewGroupByQueryTool(store, agentID),
		)
	}

	return tools
}

// VisualizationTools returns visualization tools that can be assigned by the metaagent.
// These are NOT included in default tool sets - they must be explicitly assigned.
func VisualizationTools() []shuttle.Tool {
	return []shuttle.Tool{
		NewWorkflowVisualizationTool(),
		visualization.NewVisualizationTool(), // HTML report generation with ECharts
	}
}

// PresentationToolNames returns the names of presentation strategy tools.
// Note: Visualization tools are not included - use VisualizationToolNames() for those.
func PresentationToolNames() []string {
	return []string{
		"top_n_query",
		"group_by_query",
	}
}

// VisualizationToolNames returns the names of visualization tools.
// These are available for metaagent assignment but not included in default tool sets.
func VisualizationToolNames() []string {
	return []string{
		"generate_workflow_visualization",
		"generate_visualization",
	}
}
