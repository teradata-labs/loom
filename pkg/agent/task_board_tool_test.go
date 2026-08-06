// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/task"
)

// newTaskBoardToolWithMgr stitches together a TaskBoardTool over a fresh
// migrated SQLite task store. Reuses the helper from registry_taskhelper_test.go.
func newTaskBoardToolWithMgr(t *testing.T, cfg *loomv1.TaskBoardConfig) (*TaskBoardTool, *task.Manager) {
	t.Helper()
	_, mgr, dec := newTaskSubsystem(t)
	tool := NewTaskBoardTool(mgr, dec, "agent-under-test", nil, cfg)
	return tool, mgr
}

// TestTaskBoardTool_ResolveBoardForWrite_ExistingBoardKept covers the
// happy path: LLM names a real board, tool returns it unchanged.
func TestTaskBoardTool_ResolveBoardForWrite_ExistingBoardKept(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, &loomv1.TaskBoardConfig{DefaultBoardId: "configured-default"})

	_, err := mgr.CreateBoard(ctx, &task.TaskBoard{ID: "real-board", Name: "Real"})
	require.NoError(t, err)
	_, err = mgr.CreateBoard(ctx, &task.TaskBoard{ID: "configured-default", Name: "Default"})
	require.NoError(t, err)

	id, err := tool.resolveBoardForWrite(ctx, map[string]interface{}{"board_id": "real-board"})
	require.NoError(t, err)
	assert.Equal(t, "real-board", id,
		"existing board_id must be returned as-is, default is irrelevant")
}

// TestTaskBoardTool_ResolveBoardForWrite_RebindsToDefault is the regression
// test for the agent confusion observed in E2E test #3: LLM grabbed a branch
// name and passed it as board_id; the FK constraint then killed every
// CreateTask. The tool must rebind to the configured default if it exists.
func TestTaskBoardTool_ResolveBoardForWrite_RebindsToDefault(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, &loomv1.TaskBoardConfig{DefaultBoardId: "configured-default"})

	_, err := mgr.CreateBoard(ctx, &task.TaskBoard{ID: "configured-default", Name: "Default"})
	require.NoError(t, err)

	id, err := tool.resolveBoardForWrite(ctx, map[string]interface{}{"board_id": "feat/some-branch"})
	require.NoError(t, err)
	assert.Equal(t, "configured-default", id,
		"non-existent LLM-supplied id must rebind to the configured default board")
}

// TestTaskBoardTool_ResolveBoardForWrite_AutoCreatesWhenNoDefault covers the
// fallback: agent supplies a board_id, neither it nor any default exists.
// Tool must auto-create the requested id rather than FK-failing downstream.
func TestTaskBoardTool_ResolveBoardForWrite_AutoCreatesWhenNoDefault(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	id, err := tool.resolveBoardForWrite(ctx, map[string]interface{}{"board_id": "fresh-board"})
	require.NoError(t, err)
	assert.Equal(t, "fresh-board", id)

	got, err := mgr.GetBoard(ctx, "fresh-board")
	require.NoError(t, err, "auto-created board must be persisted")
	assert.Equal(t, "fresh-board", got.ID)
	assert.Contains(t, got.Name, "agent-under-test",
		"auto-created board name must reference the originating agent for audit")
}

// TestTaskBoardTool_ResolveBoardForWrite_AutoCreatesDefaultWhenMissing covers
// the case where the configured default is named but doesn't exist yet.
// Mirrors the emitter.ensureBoard contract — operators who pin a board id
// in YAML don't have to also pre-create it.
func TestTaskBoardTool_ResolveBoardForWrite_AutoCreatesDefaultWhenMissing(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, &loomv1.TaskBoardConfig{DefaultBoardId: "pinned-default"})

	// LLM omits board_id entirely — tool should use the default.
	id, err := tool.resolveBoardForWrite(ctx, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "pinned-default", id)

	got, err := mgr.GetBoard(ctx, "pinned-default")
	require.NoError(t, err)
	assert.Equal(t, "pinned-default", got.ID)
}

// TestTaskBoardTool_ResolveBoardForWrite_NoBoardWhenUnconfigured: when the
// agent has no configured default and the LLM doesn't supply a board_id,
// the tool returns the empty string so CreateTask writes a board-less task
// rather than fabricating a meaningless one.
func TestTaskBoardTool_ResolveBoardForWrite_NoBoardWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	tool, _ := newTaskBoardToolWithMgr(t, nil)

	id, err := tool.resolveBoardForWrite(ctx, map[string]interface{}{})
	require.NoError(t, err)
	assert.Empty(t, id,
		"no board_id, no default config: return empty so the task is board-less")
}

// TestTaskBoardTool_ClaimUsesContextSessionID: claims made inside a real
// conversation (ctx carries the session id, as agent.Chat sets it) must record
// that id — not the legacy synthetic "<agentID>-session" — so session-scoped
// task filtering (ListTasks sessionId, board UIs) works.
func TestTaskBoardTool_ClaimUsesContextSessionID(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "claim me", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	sessionCtx := session.WithSessionID(ctx, "real-session-uuid")
	res, err := tool.Execute(sessionCtx, map[string]interface{}{"action": "claim", "task_id": created.ID})
	require.NoError(t, err)
	require.True(t, res.Success, "claim failed: %+v", res.Error)

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "real-session-uuid", got.ClaimedBySession)
}

// TestTaskBoardTool_ClaimFallsBackToSyntheticSessionID: headless usage (no
// session in ctx) keeps the pre-existing "<agentID>-session" behavior.
func TestTaskBoardTool_ClaimFallsBackToSyntheticSessionID(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "claim me", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{"action": "claim", "task_id": created.ID})
	require.NoError(t, err)
	require.True(t, res.Success, "claim failed: %+v", res.Error)

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "agent-under-test-session", got.ClaimedBySession)
}

// TestTaskBoardTool_CreateStampsCreatedBySession: tasks created inside a
// conversation carry created_by_session metadata (attribution, NOT a claim —
// the task must remain claimable afterwards).
func TestTaskBoardTool_CreateStampsCreatedBySession(t *testing.T) {
	ctx := session.WithSessionID(context.Background(), "real-session-uuid")
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	res, err := tool.Execute(ctx, map[string]interface{}{"action": "create", "title": "made in conversation"})
	require.NoError(t, err)
	require.True(t, res.Success, "create failed: %+v", res.Error)

	tasks, _, err := mgr.ListTasks(ctx, task.ListTasksOpts{Limit: 10})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "real-session-uuid", tasks[0].Metadata[task.CreatedBySessionMetadataKey])
	assert.Empty(t, tasks[0].ClaimedBySession, "creation must not pre-claim the task")

	// The created task must still be claimable (pre-claiming would break ready → claim).
	claimRes, err := tool.Execute(ctx, map[string]interface{}{"action": "claim", "task_id": tasks[0].ID})
	require.NoError(t, err)
	require.True(t, claimRes.Success, "task created with session metadata must remain claimable: %+v", claimRes.Error)
}
