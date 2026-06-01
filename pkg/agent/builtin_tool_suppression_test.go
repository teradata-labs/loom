// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

// newTestGraphStore spins up an in-memory SQLite-backed graph store. Shared
// across the suppression tests below; mirrors graph_memory_tool_test.go.
func newTestGraphStore(t *testing.T) memory.GraphMemoryStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))

	return sqlite.NewGraphMemoryStore(db, &mockTC{}, observability.NewNoOpTracer())
}

// TestWithoutBuiltinTool_GraphMemory verifies the core invariant of the
// tool-surface refactor: WithGraphMemoryStore wires the subsystem (extractor
// state, store reference) regardless of WithoutBuiltinTool. WithoutBuiltinTool
// only hides the tool definition from the LLM's tool list.
func TestWithoutBuiltinTool_GraphMemory(t *testing.T) {
	store := newTestGraphStore(t)
	gmCfg := DefaultGraphMemoryConfig()
	gmCfg.Enabled = true
	gmCfg.EnableExtraction = true

	t.Run("without suppression: subsystem + tool both wired", func(t *testing.T) {
		ag := NewAgent(nil, nil,
			WithGraphMemoryStore(store, gmCfg),
		)
		assert.NotNil(t, ag.graphMemoryStore, "subsystem store should be wired")
		assert.True(t, ag.graphMemoryConfig.Enabled, "config should pass through")
		assert.True(t, ag.enableGraphMemoryExtraction, "extractor should be enabled")
		assert.True(t, ag.tools.IsRegistered("graph_memory"), "tool should surface to the LLM")
	})

	t.Run("with suppression: subsystem wired, tool hidden", func(t *testing.T) {
		ag := NewAgent(nil, nil,
			WithGraphMemoryStore(store, gmCfg),
			WithoutBuiltinTool("graph_memory"),
		)
		assert.NotNil(t, ag.graphMemoryStore, "subsystem store should still be wired")
		assert.True(t, ag.graphMemoryConfig.Enabled, "config should still pass through")
		assert.True(t, ag.enableGraphMemoryExtraction, "extractor should still be enabled")
		assert.False(t, ag.tools.IsRegistered("graph_memory"), "tool should NOT surface")
		assert.True(t, ag.isBuiltinToolSuppressed("graph_memory"), "suppression flag should be set")
	})
}

// TestWithoutBuiltinTool_TaskBoard verifies the same invariant for the task
// board: WithTaskBoard wires the manager (for skill task emission), and
// WithoutBuiltinTool only hides the task_board tool from the LLM.
func TestWithoutBuiltinTool_TaskBoard(t *testing.T) {
	// A nil task.Manager is sufficient: checkAndRegisterTaskBoardTool early-
	// exits on nil manager regardless, and we're not exercising emission here.
	// The suppression check should fire before any subsystem checks.

	t.Run("with suppression: tool suppression bit is set", func(t *testing.T) {
		ag := NewAgent(nil, nil,
			WithoutBuiltinTool("task_board"),
		)
		assert.True(t, ag.isBuiltinToolSuppressed("task_board"))
		assert.False(t, ag.tools.IsRegistered("task_board"))
	})
}

// TestWithoutBuiltinTool_NoOpForUnsuppressedTools verifies WithoutBuiltinTool
// only affects the named tool; other tool surfaces are unchanged.
func TestWithoutBuiltinTool_NoOpForUnsuppressedTools(t *testing.T) {
	ag := NewAgent(nil, nil,
		WithoutBuiltinTool("graph_memory"),
	)
	assert.True(t, ag.isBuiltinToolSuppressed("graph_memory"))
	assert.False(t, ag.isBuiltinToolSuppressed("task_board"))
	assert.False(t, ag.isBuiltinToolSuppressed("conversation_memory"))
}

// TestWithoutBuiltinTool_MultipleCalls verifies repeated calls accumulate
// into the suppression set.
func TestWithoutBuiltinTool_MultipleCalls(t *testing.T) {
	ag := NewAgent(nil, nil,
		WithoutBuiltinTool("graph_memory"),
		WithoutBuiltinTool("task_board"),
		WithoutBuiltinTool("conversation_memory"),
		WithoutBuiltinTool("session_memory"),
		WithoutBuiltinTool("get_error_details"),
		WithoutBuiltinTool("query_tool_result"),
	)
	for _, name := range []string{
		"graph_memory", "task_board", "conversation_memory",
		"session_memory", "get_error_details", "query_tool_result",
	} {
		assert.True(t, ag.isBuiltinToolSuppressed(name), "expected %q suppressed", name)
	}
}

// TestWithoutBuiltinTool_EmptyName is a defensive check: the option should
// ignore empty strings rather than poisoning the map with a "" key.
func TestWithoutBuiltinTool_EmptyName(t *testing.T) {
	ag := NewAgent(nil, nil,
		WithoutBuiltinTool(""),
	)
	assert.False(t, ag.isBuiltinToolSuppressed(""))
	assert.Empty(t, ag.suppressedBuiltinTools)
}

// TestWithoutBuiltinTool_GraphMemoryExtractorStillRoutesToCompressor is a
// behavioural assertion: when the tool is suppressed but a compressor LLM is
// configured, the graph memory extractor's LLM resolution (compressorLLM
// fallback in extractGraphMemoryAsync) still applies. We don't run the
// extractor here (that requires session state); we just verify the agent
// retains the compressor wiring alongside the suppression.
func TestWithoutBuiltinTool_GraphMemoryExtractorStillRoutesToCompressor(t *testing.T) {
	store := newTestGraphStore(t)
	gmCfg := DefaultGraphMemoryConfig()
	gmCfg.Enabled = true
	gmCfg.EnableExtraction = true

	compressor := &stubLLM{name: "compressor", model: "compressor-model"}

	ag := NewAgent(nil, nil,
		WithGraphMemoryStore(store, gmCfg),
		WithCompressorLLM(compressor),
		WithoutBuiltinTool("graph_memory"),
	)
	assert.NotNil(t, ag.graphMemoryStore, "subsystem still wired")
	assert.Same(t, compressor, ag.compressorLLM, "compressor wiring preserved")
	assert.False(t, ag.tools.IsRegistered("graph_memory"), "tool suppressed")
	assert.True(t, ag.enableGraphMemoryExtraction, "extractor enabled")
	// The actual LLM resolution lives in extractGraphMemoryAsync (uses
	// a.compressorLLM ?? a.llm). Documented here so a future refactor that
	// moves that logic breaks this test instead of silently regressing.
	assert.Equal(t, ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR).Name(), "compressor")
}

// stubLLM is a minimal LLMProvider used to assert per-role wiring. The
// Chat/ChatStream paths are not exercised in this file.
type stubLLM struct {
	name  string
	model string
}

func (s *stubLLM) Name() string  { return s.name }
func (s *stubLLM) Model() string { return s.model }
func (s *stubLLM) Chat(ctx context.Context, msgs []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	return &LLMResponse{Content: ""}, nil
}
