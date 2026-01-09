// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"go.uber.org/zap"
)

// Helper function to create a test shared memory store
func createTestPresentationStore(t *testing.T) *communication.SharedMemoryStore {
	store, err := communication.NewSharedMemoryStore(nil, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, store)
	return store
}

// TestTopNQueryTool_BasicFunctionality tests the Top-N query tool.
func TestTopNQueryTool_BasicFunctionality(t *testing.T) {
	// Create shared memory store
	store := createTestPresentationStore(t)

	// Create test data
	testData := []map[string]interface{}{
		{"pattern": "A→B→C", "frequency": 100, "duration": 5.2},
		{"pattern": "A→C", "frequency": 80, "duration": 3.1},
		{"pattern": "B→D", "frequency": 120, "duration": 4.5},
		{"pattern": "A→B", "frequency": 90, "duration": 2.8},
	}

	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	// Store data in shared memory
	ctx := context.Background()
	putReq := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "test-patterns",
		Value:     jsonData,
		AgentId:   "test-agent",
	}
	_, err = store.Put(ctx, putReq)
	require.NoError(t, err)

	// Create tool
	tool := NewTopNQueryTool(store, "test-agent")

	// Test top 2 by frequency DESC
	params := map[string]interface{}{
		"source_key": "test-patterns",
		"n":          2.0, // JSON unmarshals numbers as float64
		"sort_by":    "frequency",
		"direction":  "desc",
		"namespace":  "workflow",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify results
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)

	items, ok := data["items"].([]map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 2, len(items))

	// Check sorted by frequency desc
	assert.Equal(t, 120.0, items[0]["frequency"])
	assert.Equal(t, "B→D", items[0]["pattern"])
	assert.Equal(t, 100.0, items[1]["frequency"])
	assert.Equal(t, "A→B→C", items[1]["pattern"])

	// Verify metadata
	assert.Equal(t, 4, data["total"])
	assert.Equal(t, 2, data["returned"])
}

// TestTopNQueryTool_AscendingSort tests ascending sort order.
func TestTopNQueryTool_AscendingSort(t *testing.T) {
	store := createTestPresentationStore(t)

	testData := []map[string]interface{}{
		{"id": 1, "score": 10.5},
		{"id": 2, "score": 5.2},
		{"id": 3, "score": 15.8},
	}

	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	ctx := context.Background()
	putReq := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "test-scores",
		Value:     jsonData,
		AgentId:   "test-agent",
	}
	_, err = store.Put(ctx, putReq)
	require.NoError(t, err)

	tool := NewTopNQueryTool(store, "test-agent")

	params := map[string]interface{}{
		"source_key": "test-scores",
		"n":          2.0,
		"sort_by":    "score",
		"direction":  "asc", // Ascending
		"namespace":  "workflow",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	items := data["items"].([]map[string]interface{})

	// Check sorted by score asc (lowest first)
	assert.Equal(t, 5.2, items[0]["score"])
	assert.Equal(t, 10.5, items[1]["score"])
}

// TestTopNQueryTool_InvalidParams tests error handling for invalid parameters.
func TestTopNQueryTool_InvalidParams(t *testing.T) {
	store := createTestPresentationStore(t)
	tool := NewTopNQueryTool(store, "test-agent")
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
		errMsg string
	}{
		{
			name:   "missing source_key",
			params: map[string]interface{}{"n": 10.0, "sort_by": "count"},
			errMsg: "source_key is required",
		},
		{
			name:   "missing sort_by",
			params: map[string]interface{}{"source_key": "test", "n": 10.0},
			errMsg: "sort_by is required",
		},
		{
			name:   "n too large",
			params: map[string]interface{}{"source_key": "test", "n": 2000.0, "sort_by": "count"},
			errMsg: "n must be between 1 and 1000",
		},
		{
			name:   "n too small",
			params: map[string]interface{}{"source_key": "test", "n": 0.0, "sort_by": "count"},
			errMsg: "n must be between 1 and 1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err) // Tool doesn't return error, only marks result as failed
			assert.False(t, result.Success)
			assert.Contains(t, result.Error.Message, tt.errMsg)
		})
	}
}

// TestGroupByQueryTool_BasicFunctionality tests the GROUP BY query tool.
func TestGroupByQueryTool_BasicFunctionality(t *testing.T) {
	store := createTestPresentationStore(t)

	testData := []map[string]interface{}{
		{"segment": "enterprise", "region": "us", "revenue": 1000.0},
		{"segment": "enterprise", "region": "us", "revenue": 1500.0},
		{"segment": "enterprise", "region": "eu", "revenue": 800.0},
		{"segment": "smb", "region": "us", "revenue": 500.0},
		{"segment": "smb", "region": "eu", "revenue": 600.0},
		{"segment": "smb", "region": "eu", "revenue": 700.0},
	}

	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	ctx := context.Background()
	putReq := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "test-revenue",
		Value:     jsonData,
		AgentId:   "test-agent",
	}
	_, err = store.Put(ctx, putReq)
	require.NoError(t, err)

	tool := NewGroupByQueryTool(store, "test-agent")

	// Group by segment and region
	params := map[string]interface{}{
		"source_key": "test-revenue",
		"group_by":   []interface{}{"segment", "region"},
		"namespace":  "workflow",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	groups := data["groups"].([]map[string]interface{})

	// Should have 3 groups: (enterprise, us), (enterprise, eu), (smb, us), (smb, eu)
	assert.Equal(t, 4, len(groups))
	assert.Equal(t, 6, data["total_rows"])
	assert.Equal(t, 4, data["num_groups"])

	// Verify groups are sorted by count descending
	// (enterprise, us) and (smb, eu) both have count=2
	// Find the (smb, eu) group and verify count
	foundSmbEu := false
	for _, group := range groups {
		if group["segment"] == "smb" && group["region"] == "eu" {
			assert.Equal(t, 2, group["count"])
			foundSmbEu = true
			break
		}
	}
	assert.True(t, foundSmbEu, "Should find (smb, eu) group")
}

// TestGroupByQueryTool_SingleDimension tests grouping by a single column.
func TestGroupByQueryTool_SingleDimension(t *testing.T) {
	store := createTestPresentationStore(t)

	testData := []map[string]interface{}{
		{"category": "A", "value": 100},
		{"category": "B", "value": 200},
		{"category": "A", "value": 150},
		{"category": "C", "value": 50},
		{"category": "A", "value": 120},
	}

	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	ctx := context.Background()
	putReq := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "test-categories",
		Value:     jsonData,
		AgentId:   "test-agent",
	}
	_, err = store.Put(ctx, putReq)
	require.NoError(t, err)

	tool := NewGroupByQueryTool(store, "test-agent")

	params := map[string]interface{}{
		"source_key": "test-categories",
		"group_by":   []interface{}{"category"},
		"namespace":  "workflow",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	groups := data["groups"].([]map[string]interface{})

	// Should have 3 groups: A (count=3), B (count=1), C (count=1)
	assert.Equal(t, 3, len(groups))

	// First group (sorted by count desc) should be category A with count 3
	assert.Equal(t, "A", groups[0]["category"])
	assert.Equal(t, 3, groups[0]["count"])
}

// TestGroupByQueryTool_KeyNotFound tests error handling when key doesn't exist.
func TestGroupByQueryTool_KeyNotFound(t *testing.T) {
	store := createTestPresentationStore(t)
	tool := NewGroupByQueryTool(store, "test-agent")
	ctx := context.Background()

	params := map[string]interface{}{
		"source_key": "nonexistent-key",
		"group_by":   []interface{}{"column"},
		"namespace":  "workflow",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Message, "Key not found")
}

// TestPresentationTools_ConcurrentAccess tests concurrent tool usage (race detection).
func TestPresentationTools_ConcurrentAccess(t *testing.T) {
	store := createTestPresentationStore(t)

	// Store test data
	testData := []map[string]interface{}{
		{"id": 1, "value": 10},
		{"id": 2, "value": 20},
		{"id": 3, "value": 30},
		{"id": 4, "value": 40},
		{"id": 5, "value": 50},
	}

	jsonData, err := json.Marshal(testData)
	require.NoError(t, err)

	ctx := context.Background()
	putReq := &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "concurrent-test",
		Value:     jsonData,
		AgentId:   "test-agent",
	}
	_, err = store.Put(ctx, putReq)
	require.NoError(t, err)

	// Create tools
	topNTool := NewTopNQueryTool(store, "test-agent-1")
	groupByTool := NewGroupByQueryTool(store, "test-agent-2")

	// Run concurrent queries
	done := make(chan bool)

	// Agent 1: Top-N queries
	go func() {
		for i := 0; i < 10; i++ {
			params := map[string]interface{}{
				"source_key": "concurrent-test",
				"n":          3.0,
				"sort_by":    "value",
				"direction":  "desc",
				"namespace":  "workflow",
			}
			result, err := topNTool.Execute(ctx, params)
			require.NoError(t, err)
			assert.True(t, result.Success)
		}
		done <- true
	}()

	// Agent 2: GROUP BY queries
	go func() {
		for i := 0; i < 10; i++ {
			params := map[string]interface{}{
				"source_key": "concurrent-test",
				"group_by":   []interface{}{"id"},
				"namespace":  "workflow",
			}
			result, err := groupByTool.Execute(ctx, params)
			require.NoError(t, err)
			assert.True(t, result.Success)
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done
}

// TestPresentationToolNames tests the tool name registry function.
func TestPresentationToolNames(t *testing.T) {
	names := PresentationToolNames()
	// Visualization tools are NOT included - use VisualizationToolNames() for those
	assert.Equal(t, 2, len(names))
	assert.Contains(t, names, "top_n_query")
	assert.Contains(t, names, "group_by_query")
}

// TestVisualizationToolNames tests the visualization tool name registry function.
func TestVisualizationToolNames(t *testing.T) {
	names := VisualizationToolNames()
	assert.Equal(t, 2, len(names))
	assert.Contains(t, names, "generate_workflow_visualization")
	assert.Contains(t, names, "generate_visualization")
}

// TestPresentationTools_Factory tests the factory function.
func TestPresentationTools_Factory(t *testing.T) {
	store := createTestPresentationStore(t)
	tools := PresentationTools(store, "test-agent")

	// Only query tools, NOT visualization tools (metaagent assigns those)
	assert.Equal(t, 2, len(tools))

	// Verify tool names
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name()] = true
	}

	assert.True(t, toolNames["top_n_query"])
	assert.True(t, toolNames["group_by_query"])
}

// TestVisualizationTools_Factory tests the visualization tools factory function.
func TestVisualizationTools_Factory(t *testing.T) {
	tools := VisualizationTools()
	assert.Equal(t, 2, len(tools))

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name()] = true
	}
	assert.True(t, toolNames["generate_workflow_visualization"])
	assert.True(t, toolNames["generate_visualization"])
}

// TestPresentationTools_NilStore tests behavior with nil store.
func TestPresentationTools_NilStore(t *testing.T) {
	tools := PresentationTools(nil, "test-agent")
	// No tools without store (viz tools are NOT included by default)
	assert.Equal(t, 0, len(tools), "Should have no tools without store")
}
