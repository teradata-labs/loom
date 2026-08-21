// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"fmt"
	"strings"
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

// TestTaskBoardTool_AcceptanceCriteriaWriteOnce: criteria define "done";
// they are settable at create or while empty, then immutable — the only
// path to different criteria is cancel + re-create.
func TestTaskBoardTool_AcceptanceCriteriaWriteOnce(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	// Set at create via the tool.
	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "create", "title": "with criteria",
		"acceptance_criteria": "output has columns a,b,c",
	})
	require.NoError(t, err)
	require.True(t, res.Success)

	// Set-while-empty via update works.
	blank, err := mgr.CreateTask(ctx, &task.Task{Title: "no criteria yet", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": blank.ID,
		"acceptance_criteria": "rows sorted by ts",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "setting criteria while empty must succeed: %+v", res.Error)

	// Changing them afterwards is refused with the dedicated code.
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": blank.ID,
		"acceptance_criteria": "different goalposts",
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "ACCEPTANCE_CRITERIA_LOCKED", res.Error.Code)

	// Original criteria survived; re-sending the identical value is a no-op,
	// not an error (idempotent retries).
	got, err := mgr.GetTask(ctx, blank.ID)
	require.NoError(t, err)
	assert.Equal(t, "rows sorted by ts", got.AcceptanceCriteria)
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": blank.ID,
		"acceptance_criteria": "rows sorted by ts",
	})
	require.NoError(t, err)
	assert.True(t, res.Success)
}

// TestTaskBoardTool_BatchUpdates: completing the current task and starting
// the next is one call; entries apply independently and report per-entry
// outcomes.
func TestTaskBoardTool_BatchUpdates(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	first, err := mgr.CreateTask(ctx, &task.Task{Title: "current", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS})
	require.NoError(t, err)
	second, err := mgr.CreateTask(ctx, &task.Task{Title: "next", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update",
		"updates": []interface{}{
			map[string]interface{}{"task_id": first.ID, "status": "done", "notes": "criteria verified"},
			map[string]interface{}{"task_id": second.ID, "status": "in_progress"},
			map[string]interface{}{"task_id": "no-such-task", "status": "done"},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "batch itself succeeds; entries report individually: %+v", res.Error)

	gotFirst, err := mgr.GetTask(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, gotFirst.Status)
	assert.Contains(t, gotFirst.Notes, "criteria verified")

	gotSecond, err := mgr.GetTask(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, gotSecond.Status,
		"a failing sibling entry must not roll back the others")
}

// TestRenderSessionChecklist: the board-state-backed checklist block — one
// source of truth rendered per step, scoped to the caller's session.
func TestRenderSessionChecklist(t *testing.T) {
	ctx := context.Background()
	_, mgr := newTaskBoardToolWithMgr(t, nil)

	_, err := mgr.CreateTask(ctx, &task.Task{
		Title: "build the extract", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		AcceptanceCriteria: "columns a,b,c with UTC timestamps",
		Metadata:           map[string]string{task.CreatedBySessionMetadataKey: "sess-1"},
	})
	require.NoError(t, err)
	_, err = mgr.CreateTask(ctx, &task.Task{
		Title: "verify against contract", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-1"},
	})
	require.NoError(t, err)
	// Another session's task must not leak into the block.
	_, err = mgr.CreateTask(ctx, &task.Task{
		Title: "foreign work", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-2"},
	})
	require.NoError(t, err)

	// A terminal task of this session must not render (live statuses only).
	closedTask, err := mgr.CreateTask(ctx, &task.Task{
		Title: "already shipped", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-1"},
	})
	require.NoError(t, err)
	_, err = mgr.CloseTask(ctx, closedTask.ID, "shipped")
	require.NoError(t, err)

	block := RenderSessionChecklist(ctx, mgr, "sess-1", 0)
	assert.Contains(t, block, "IN PROGRESS: build the extract")
	assert.Contains(t, block, "criteria: columns a,b,c with UTC timestamps")
	assert.Contains(t, block, "PENDING: verify against contract")
	assert.NotContains(t, block, "foreign work")
	assert.NotContains(t, block, "already shipped",
		"terminal (done/cancelled) tasks must be excluded from the checklist")

	// Empty for sessions with no live items.
	assert.Empty(t, RenderSessionChecklist(ctx, mgr, "sess-none", 0))

	// Budget truncation: a tiny budget still yields a well-formed block.
	small := RenderSessionChecklist(ctx, mgr, "sess-1", 60)
	assert.LessOrEqual(t, len(small), 60)
	assert.Contains(t, small, "## Session task checklist")

	// Claimed-by also counts as session membership: claim an OPEN task into
	// a different session and assert it renders there.
	claimable, err := mgr.CreateTask(ctx, &task.Task{
		Title: "claimed elsewhere", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)
	_, err = mgr.ClaimTask(ctx, claimable.ID, "agent-x", "sess-3")
	require.NoError(t, err)
	claimedBlock := RenderSessionChecklist(ctx, mgr, "sess-3", 0)
	assert.Contains(t, claimedBlock, "IN PROGRESS: claimed elsewhere",
		"a task claimed by the session must render in its checklist")
}

// =============================================================================
// Lifecycle routing regressions (PR #330 review, B1)
// =============================================================================

// TestTaskBoardTool_UpdateDoneUnblocksDependents is the B1 regression: a
// batch `update status=done` must route through Manager.CloseTask so tasks
// blocked on the closed one are unblocked. Before the fix the update wrote
// the status directly and dependents stayed BLOCKED forever.
func TestTaskBoardTool_UpdateDoneUnblocksDependents(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	blocker, err := mgr.CreateTask(ctx, &task.Task{Title: "A blocker", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	dependent, err := mgr.CreateTask(ctx, &task.Task{Title: "B dependent", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	// B depends on A: adding the edge auto-transitions B to BLOCKED.
	depRes, err := tool.Execute(ctx, map[string]interface{}{
		"action": "add_dep", "task_id": dependent.ID, "depends_on": blocker.ID,
	})
	require.NoError(t, err)
	require.True(t, depRes.Success, "add_dep failed: %+v", depRes.Error)
	gotDependent, err := mgr.GetTask(ctx, dependent.ID)
	require.NoError(t, err)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_BLOCKED, gotDependent.Status)

	// Close A through the batch update form.
	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update",
		"updates": []interface{}{
			map[string]interface{}{"task_id": blocker.ID, "status": "done", "reason": "built and verified"},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "batch update failed: %+v", res.Error)

	gotBlocker, err := mgr.GetTask(ctx, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, gotBlocker.Status)
	assert.Equal(t, "built and verified", gotBlocker.CloseReason, "per-entry reason must reach CloseTask")
	require.NotNil(t, gotBlocker.ClosedAt, "CloseTask must stamp closed_at")

	gotDependent, err = mgr.GetTask(ctx, dependent.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, gotDependent.Status,
		"closing the blocker via update must unblock the dependent")

	ready, err := mgr.GetReadyFront(ctx, "", task.ReadyFrontOpts{MaxResults: 10})
	require.NoError(t, err)
	readyIDs := make([]string, 0, len(ready))
	for _, tk := range ready {
		readyIDs = append(readyIDs, tk.ID)
	}
	assert.Contains(t, readyIDs, dependent.ID, "unblocked dependent must be on the ready front")
}

// TestTaskBoardTool_UpdateDoneReleasesClaim: closing a claimed task via
// `update status=done` must release the claim and stamp closed_at, exactly
// like the close action (both route through Manager.CloseTask).
func TestTaskBoardTool_UpdateDoneReleasesClaim(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "claim then close", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	_, err = mgr.ClaimTask(ctx, created.ID, "agent-under-test", "sess-hold")
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": created.ID, "status": "done", "reason": "criteria verified",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "update failed: %+v", res.Error)

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, got.Status)
	assert.Empty(t, got.ClaimedBySession, "closing must release the session claim")
	assert.Empty(t, got.AssigneeAgentID, "closing must release the assignee")
	require.NotNil(t, got.ClosedAt, "closing must stamp closed_at")
	assert.Equal(t, "criteria verified", got.CloseReason)
}

// TestTaskBoardTool_UpdateCancelledReleasesAndUnblocks: `update
// status=cancelled` is a real terminal transition (Manager.CancelTask), not
// a raw status write: it stamps closed_at with the reason, releases the
// claim, and unblocks dependents — and it is the cancel path the
// ACCEPTANCE_CRITERIA_LOCKED recovery text points at (M2).
func TestTaskBoardTool_UpdateCancelledReleasesAndUnblocks(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	blocker, err := mgr.CreateTask(ctx, &task.Task{Title: "doomed blocker", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	dependent, err := mgr.CreateTask(ctx, &task.Task{Title: "waiting task", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	require.NoError(t, mgr.AddDependency(ctx, &task.TaskDependency{
		FromTaskID: dependent.ID, ToTaskID: blocker.ID,
		Type: loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS, CreatedBy: "test",
	}))
	_, err = mgr.ClaimTask(ctx, blocker.ID, "agent-under-test", "sess-cancel")
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": blocker.ID, "status": "cancelled", "reason": "wrong approach",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "cancel via update failed: %+v", res.Error)

	got, err := mgr.GetTask(ctx, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, got.Status)
	assert.Equal(t, "wrong approach", got.CloseReason)
	require.NotNil(t, got.ClosedAt, "cancelling must stamp closed_at")
	assert.Empty(t, got.ClaimedBySession, "cancelling must release the session claim")
	assert.Empty(t, got.AssigneeAgentID, "cancelling must release the assignee")

	gotDependent, err := mgr.GetTask(ctx, dependent.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, gotDependent.Status,
		"CANCELLED is terminal: dependents must unblock")

	history, err := mgr.GetHistory(ctx, blocker.ID)
	require.NoError(t, err)
	actions := make([]string, 0, len(history))
	for _, h := range history {
		actions = append(actions, h.Action)
	}
	assert.Contains(t, actions, "cancelled", "cancellation must be recorded in the audit trail")
}

// TestTaskBoardTool_UpdateInProgressClaimRace: `update status=in_progress`
// on an OPEN task routes through Manager.ClaimTask's conditional write, so
// when two sessions race, exactly one wins.
func TestTaskBoardTool_UpdateInProgressClaimRace(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "contested", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	type outcome struct {
		success bool
		session string
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, sid := range []string{"sess-racer-1", "sess-racer-2"} {
		go func(sid string) {
			<-start
			sctx := session.WithSessionID(ctx, sid)
			res, execErr := tool.Execute(sctx, map[string]interface{}{
				"action": "update", "task_id": created.ID, "status": "in_progress",
			})
			results <- outcome{success: execErr == nil && res.Success, session: sid}
		}(sid)
	}
	close(start)

	var winners []string
	for i := 0; i < 2; i++ {
		o := <-results
		if o.success {
			winners = append(winners, o.session)
		}
	}
	require.Len(t, winners, 1, "exactly one racer must win the claim")

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, got.Status)
	assert.Equal(t, winners[0], got.ClaimedBySession, "the task must be claimed by the winning session")

	// Re-asserting in_progress from the claim-holding session is an
	// idempotent success; from any other session it stays an error.
	winCtx := session.WithSessionID(ctx, winners[0])
	res, err := tool.Execute(winCtx, map[string]interface{}{
		"action": "update", "task_id": created.ID, "status": "in_progress",
	})
	require.NoError(t, err)
	assert.True(t, res.Success, "claim holder re-asserting in_progress must succeed: %+v", res.Error)

	otherCtx := session.WithSessionID(ctx, "sess-interloper")
	res, err = tool.Execute(otherCtx, map[string]interface{}{
		"action": "update", "task_id": created.ID, "status": "in_progress",
	})
	require.NoError(t, err)
	require.False(t, res.Success, "a non-holder must not be told it is working a claimed task")
	assert.Contains(t, res.Error.Message, "already in progress")
}

// TestTaskBoardTool_UpdateOpenReleasesClaim: `update status=open` on a
// claimed task routes through Manager.ReleaseTask so the claim fields are
// cleared and the task is re-claimable.
func TestTaskBoardTool_UpdateOpenReleasesClaim(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "claim then drop", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	_, err = mgr.ClaimTask(ctx, created.ID, "agent-under-test", "sess-drop")
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": created.ID, "status": "open",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "update failed: %+v", res.Error)

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, got.Status)
	assert.Empty(t, got.ClaimedBySession, "returning to OPEN must release the claim")

	// Re-claimable: a fresh claim must succeed.
	_, err = mgr.ClaimTask(ctx, created.ID, "agent-b", "sess-next")
	require.NoError(t, err, "released task must be claimable again")
}

// =============================================================================
// Update error codes + batch validation (PR #330 review, minors)
// =============================================================================

// TestTaskBoardTool_UpdateErrorCodes: the single update form surfaces
// INVALID_PARAMETER and NOT_FOUND, not a generic UPDATE_ERROR.
func TestTaskBoardTool_UpdateErrorCodes(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	// Missing task_id.
	res, err := tool.Execute(ctx, map[string]interface{}{"action": "update"})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "INVALID_PARAMETER", res.Error.Code)

	// Unknown task.
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": "no-such-task", "notes": "hello",
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "NOT_FOUND", res.Error.Code)

	// Invalid status string is rejected instead of silently ignored.
	created, err := mgr.CreateTask(ctx, &task.Task{Title: "victim", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "task_id": created.ID, "status": "finished",
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "INVALID_PARAMETER", res.Error.Code)
	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, got.Status, "invalid status must not change the task")
}

// TestTaskBoardTool_BatchUpdateValidation: non-array updates, empty arrays,
// and oversized batches are rejected with clear INVALID_PARAMETER errors
// instead of falling through to the single-update path.
func TestTaskBoardTool_BatchUpdateValidation(t *testing.T) {
	ctx := context.Background()
	tool, _ := newTaskBoardToolWithMgr(t, nil)

	// Non-array.
	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update", "updates": "not-an-array",
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "INVALID_PARAMETER", res.Error.Code)
	assert.Contains(t, res.Error.Message, "array")

	// Empty array.
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "updates": []interface{}{},
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "INVALID_PARAMETER", res.Error.Code)

	// Over the 20-entry cap.
	tooMany := make([]interface{}, 21)
	for i := range tooMany {
		tooMany[i] = map[string]interface{}{"task_id": "t", "notes": "n"}
	}
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "update", "updates": tooMany,
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	assert.Equal(t, "INVALID_PARAMETER", res.Error.Code)
	assert.Contains(t, res.Error.Message, "at most 20")
}

// TestTaskBoardTool_BatchUpdateAllFailReportsFailure: when every entry
// fails, the batch result itself is a failure, and each entry carries a
// structured {code, message} error.
func TestTaskBoardTool_BatchUpdateAllFailReportsFailure(t *testing.T) {
	ctx := context.Background()
	tool, _ := newTaskBoardToolWithMgr(t, nil)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update",
		"updates": []interface{}{
			map[string]interface{}{"task_id": "ghost-1", "status": "done"},
			map[string]interface{}{"status": "done"}, // missing task_id
			"not-an-object",
		},
	})
	require.NoError(t, err)
	require.False(t, res.Success, "a batch where every entry failed must not report success")
	require.NotNil(t, res.Error)
	assert.Equal(t, "UPDATE_ERROR", res.Error.Code)

	data, ok := res.Data.(map[string]interface{})
	require.True(t, ok, "per-entry results must remain available on failure")
	assert.Equal(t, 3, data["failed"])
	assert.Equal(t, 0, data["succeeded"])
	results, ok := data["results"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, results, 3)

	entryErr, ok := results[0]["error"].(map[string]interface{})
	require.True(t, ok, "entry errors must be structured objects")
	assert.Equal(t, "NOT_FOUND", entryErr["code"])
	entryErr, ok = results[1]["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "INVALID_PARAMETER", entryErr["code"])
	entryErr, ok = results[2]["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "INVALID_PARAMETER", entryErr["code"])
}

// TestTaskBoardTool_BatchUpdatePartialFailureStillSucceeds: one failing
// entry among successes keeps the batch result successful with per-entry
// reporting (independent application, no rollback).
func TestTaskBoardTool_BatchUpdatePartialFailureStillSucceeds(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "fine", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action": "update",
		"updates": []interface{}{
			map[string]interface{}{"task_id": created.ID, "notes": "progress"},
			map[string]interface{}{"task_id": "ghost", "notes": "nope"},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	data, ok := res.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 1, data["succeeded"])
	assert.Equal(t, 1, data["failed"])
}

// =============================================================================
// Write-once criteria under concurrency (PR #330 review, M1)
// =============================================================================

// TestTaskBoardTool_CriteriaConcurrentWritesOneWins: the write-once guard is
// enforced in the store (single conditional UPDATE), so two concurrent
// different-value writers cannot both win — the loser gets
// ACCEPTANCE_CRITERIA_LOCKED.
func TestTaskBoardTool_CriteriaConcurrentWritesOneWins(t *testing.T) {
	ctx := context.Background()
	tool, mgr := newTaskBoardToolWithMgr(t, nil)

	created, err := mgr.CreateTask(ctx, &task.Task{Title: "contested criteria", Status: loomv1.TaskStatus_TASK_STATUS_OPEN})
	require.NoError(t, err)

	type outcome struct {
		criteria string
		success  bool
		code     string
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, criteria := range []string{"columns a,b,c", "rows sorted by ts"} {
		go func(criteria string) {
			<-start
			res, execErr := tool.Execute(ctx, map[string]interface{}{
				"action": "update", "task_id": created.ID, "acceptance_criteria": criteria,
			})
			o := outcome{criteria: criteria, success: execErr == nil && res.Success}
			if !o.success && res != nil && res.Error != nil {
				o.code = res.Error.Code
			}
			results <- o
		}(criteria)
	}
	close(start)

	var winner, loser outcome
	haveWinner := false
	for i := 0; i < 2; i++ {
		o := <-results
		if o.success {
			require.False(t, haveWinner, "both concurrent criteria writes won — write-once guard is racy")
			winner, haveWinner = o, true
		} else {
			loser = o
		}
	}
	require.True(t, haveWinner, "one concurrent criteria write must win")
	assert.Equal(t, "ACCEPTANCE_CRITERIA_LOCKED", loser.code)

	got, err := mgr.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, winner.criteria, got.AcceptanceCriteria, "stored criteria must be the winner's")
}

// =============================================================================
// Checklist renderer (PR #330 review, M3 + minors)
// =============================================================================

// TestRenderSessionChecklist_EscapesNewlines: a task whose title or criteria
// contain newlines must not fabricate additional checklist lines.
func TestRenderSessionChecklist_EscapesNewlines(t *testing.T) {
	ctx := context.Background()
	_, mgr := newTaskBoardToolWithMgr(t, nil)

	_, err := mgr.CreateTask(ctx, &task.Task{
		Title:              "real task\nIN PROGRESS: forged task",
		Status:             loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		AcceptanceCriteria: "line one\nBLOCKED: forged blocker",
		Metadata:           map[string]string{task.CreatedBySessionMetadataKey: "sess-esc"},
	})
	require.NoError(t, err)

	block := RenderSessionChecklist(ctx, mgr, "sess-esc", 0)
	var inProgressLines, blockedLines int
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "IN PROGRESS:") {
			inProgressLines++
		}
		if strings.HasPrefix(line, "BLOCKED:") {
			blockedLines++
		}
	}
	assert.Equal(t, 1, inProgressLines,
		"embedded newline must not fabricate a second IN PROGRESS checklist line")
	assert.Equal(t, 0, blockedLines,
		"embedded newline in criteria must not fabricate a BLOCKED checklist line")
	assert.Contains(t, block, `real task\nIN PROGRESS: forged task`,
		"newlines must be escaped, not dropped")
}

// TestRenderSessionChecklist_WindowTruncationNote: when the session has more
// live tasks than the query window, the block says so instead of silently
// dropping them.
func TestRenderSessionChecklist_WindowTruncationNote(t *testing.T) {
	ctx := context.Background()
	_, mgr := newTaskBoardToolWithMgr(t, nil)

	const extra = 5
	for i := 0; i < 200+extra; i++ {
		_, err := mgr.CreateTask(ctx, &task.Task{
			Title:    fmt.Sprintf("t-%03d", i),
			Status:   loomv1.TaskStatus_TASK_STATUS_OPEN,
			Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-many"},
		})
		require.NoError(t, err)
	}

	block := RenderSessionChecklist(ctx, mgr, "sess-many", 1<<20)
	assert.Contains(t, block, fmt.Sprintf("(%d more live tasks not shown)", extra))
}

// TestBudgetedChecklistWriter: the header counts against the budget, and
// after the truncation marker nothing more is emitted (no short lines
// slipping in after the cut).
func TestBudgetedChecklistWriter(t *testing.T) {
	// Header alone over budget: nothing but (at most) the marker appears.
	w := &budgetedChecklistWriter{budget: 10}
	w.write("## Session task checklist\n")
	assert.LessOrEqual(t, len(w.String()), 10)

	// After the marker, later short writes are dropped.
	w = &budgetedChecklistWriter{budget: 40}
	w.write("0123456789\n")                 // 11 bytes, fits
	w.write(strings.Repeat("x", 60) + "\n") // overflows → marker
	w.write("tiny\n")                       // must NOT appear after the marker
	out := w.String()
	assert.True(t, strings.HasSuffix(out, "(… truncated …)\n"),
		"marker must be the final content, got %q", out)
	assert.NotContains(t, out, "tiny")
	assert.LessOrEqual(t, len(out), 40)
}
