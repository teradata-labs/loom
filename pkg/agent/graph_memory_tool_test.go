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

//go:build fts5

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

// mockTC is a simple token counter for tests.
type mockTC struct{}

func (m *mockTC) CountTokens(text string) int { return len(text) / 4 }

func newTestGraphMemoryTool(t *testing.T) (*GraphMemoryTool, memory.GraphMemoryStore) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))

	store := sqlite.NewGraphMemoryStore(db, &mockTC{}, observability.NewNoOpTracer())
	tool := NewGraphMemoryTool(store, "test-agent")
	return tool, store
}

func parseToolResult(t *testing.T, result *shuttle.Result) map[string]interface{} {
	t.Helper()
	require.True(t, result.Success, "expected success, got error: %v", result.Error)
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "expected map[string]interface{}, got %T", result.Data)
	return data
}

func TestGraphMemoryTool_Remember(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":      "remember",
		"content":     "User prefers dark mode for all applications",
		"summary":     "Prefers dark mode",
		"memory_type": "preference",
		"tags":        []interface{}{"ui", "preference"},
		"salience":    0.8,
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)

	assert.Equal(t, "remember", data["action"])
	assert.NotEmpty(t, data["memory_id"])
	assert.Equal(t, 0.8, data["salience"])
}

func TestGraphMemoryTool_Recall(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	// Remember first.
	_, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "remember",
		"content": "The database migration uses FTS5 for full-text search",
		"tags":    []interface{}{"technical"},
	})
	require.NoError(t, err)

	// Recall.
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "recall",
		"query":  "FTS5 search",
		"limit":  float64(10),
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)

	assert.Equal(t, "recall", data["action"])
	assert.Equal(t, 1, data["count"])
	results, ok := data["results"].([]map[string]interface{})
	require.True(t, ok, "results should be []map[string]interface{}")
	assert.Len(t, results, 1)
}

func TestGraphMemoryTool_Forget(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	// Remember.
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "remember",
		"content": "Temporary fact to forget",
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	memoryID := data["memory_id"].(string)

	// Forget.
	result, err = tool.Execute(ctx, map[string]interface{}{
		"action":    "forget",
		"memory_id": memoryID,
	})
	require.NoError(t, err)
	data = parseToolResult(t, result)
	assert.Equal(t, "forget", data["action"])
	assert.Equal(t, true, data["success"])

	// Recall should not find it.
	result, err = tool.Execute(ctx, map[string]interface{}{
		"action": "recall",
		"query":  "temporary forget",
	})
	require.NoError(t, err)
	data = parseToolResult(t, result)
	assert.Equal(t, 0, data["count"])
}

func TestGraphMemoryTool_Supersede(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	// Remember original.
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "remember",
		"content": "User lives in Seattle",
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	oldID := data["memory_id"].(string)

	// Supersede.
	result, err = tool.Execute(ctx, map[string]interface{}{
		"action":    "supersede",
		"memory_id": oldID,
		"content":   "User moved to Austin",
	})
	require.NoError(t, err)
	data = parseToolResult(t, result)
	assert.Equal(t, "supersede", data["action"])
	assert.NotEqual(t, oldID, data["new_memory_id"])
	assert.Equal(t, oldID, data["old_memory_id"])
}

func TestGraphMemoryTool_Consolidate(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	// Remember 3 related memories.
	ids := make([]interface{}, 3)
	for i, content := range []string{
		"Movie night is Fridays",
		"Prefers musicals",
		"No horror movies",
	} {
		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":  "remember",
			"content": content,
		})
		require.NoError(t, err)
		data := parseToolResult(t, result)
		ids[i] = data["memory_id"].(string)
	}

	// Consolidate.
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":     "consolidate",
		"memory_ids": ids,
		"content":    "Family movie rules: Fridays, musicals preferred, no horror",
		"summary":    "Movie rules",
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	assert.Equal(t, "consolidate", data["action"])
	assert.Equal(t, 3, data["source_count"])
}

func TestGraphMemoryTool_ContextFor(t *testing.T) {
	tool, store := newTestGraphMemoryTool(t)
	ctx := context.Background()

	// Create entity and memory.
	entity, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "test-agent", Name: "alice", EntityType: "person",
	})
	require.NoError(t, err)

	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "test-agent", Content: "Alice likes Go programming",
		MemoryType: "preference", Salience: 0.8,
		EntityIDs: []string{entity.ID},
	})
	require.NoError(t, err)

	// ContextFor.
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":      "context_for",
		"entity_name": "alice",
		"topic":       "programming",
		"max_tokens":  float64(5000),
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	assert.Equal(t, "context_for", data["action"])
	assert.Equal(t, "alice", data["entity_name"])
	assert.Contains(t, data["context"], "alice")
}

func TestGraphMemoryTool_Entities(t *testing.T) {
	tool, store := newTestGraphMemoryTool(t)
	ctx := context.Background()

	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "test-agent", Name: "kubernetes", EntityType: "tool",
	})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "entities",
		"query":  "kubernetes",
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	assert.Equal(t, 1, data["count"])
}

func TestGraphMemoryTool_Relate(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":      "relate",
		"source_name": "alice",
		"target_name": "loom-project",
		"relation":    "WORKS_ON",
	})
	require.NoError(t, err)
	data := parseToolResult(t, result)
	assert.Equal(t, "relate", data["action"])
	assert.Equal(t, "alice", data["source_name"])
	assert.Equal(t, "WORKS_ON", data["relation"])
	assert.NotEmpty(t, data["edge_id"])
}

func TestGraphMemoryTool_InvalidAction(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "invalid",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_ACTION", result.Error.Code)
}

func TestGraphMemoryTool_MissingAction(t *testing.T) {
	tool, _ := newTestGraphMemoryTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMETER", result.Error.Code)
}
