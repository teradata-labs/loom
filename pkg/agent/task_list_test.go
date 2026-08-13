// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Invariant: IDs are sequential from 1 across create batches.
func TestTaskList_CreateAssignsSequentialIDs(t *testing.T) {
	tl := types.NewTaskList()
	ids, err := tl.Create([]types.TaskItem{{Title: "a"}, {Title: "b"}})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, ids)

	ids, err = tl.Create([]types.TaskItem{{Title: "c"}})
	require.NoError(t, err)
	assert.Equal(t, []int{3}, ids)
}

// Invariant: an empty title rejects the whole batch — nothing is created.
func TestTaskList_CreateRejectsEmptyTitleWholesale(t *testing.T) {
	tl := types.NewTaskList()
	_, err := tl.Create([]types.TaskItem{{Title: "a"}, {Title: "  "}})
	require.Error(t, err)

	ids, err := tl.Create([]types.TaskItem{{Title: "first"}})
	require.NoError(t, err)
	assert.Equal(t, []int{1}, ids, "failed batch must not have consumed IDs or slots")
}

// Invariant: pending→in_progress→done; done and cancelled are terminal.
func TestTaskList_UpdateLifecycle(t *testing.T) {
	tl := types.NewTaskList()
	_, err := tl.Create([]types.TaskItem{{Title: "a"}, {Title: "b"}})
	require.NoError(t, err)

	require.NoError(t, tl.Update(1, "in_progress", ""))
	require.NoError(t, tl.Update(1, "done", ""))
	assert.Error(t, tl.Update(1, "in_progress", ""), "done is terminal")

	require.NoError(t, tl.Update(2, "cancelled", "task text says snowflake not graded"))
	assert.Error(t, tl.Update(2, "done", ""), "cancelled is terminal")

	assert.Error(t, tl.Update(99, "done", ""), "unknown id")
	assert.Error(t, tl.Update(1, "bogus", ""), "unknown status")
}

// Invariant: cancelling requires a reason — silent removal is impossible.
func TestTaskList_CancelRequiresReason(t *testing.T) {
	tl := types.NewTaskList()
	_, err := tl.Create([]types.TaskItem{{Title: "a"}})
	require.NoError(t, err)
	assert.Error(t, tl.Update(1, "cancelled", "  "))
	assert.NoError(t, tl.Update(1, "cancelled", "superseded by #2"))
}

// Invariant: render shows in_progress criteria verbatim, pending titles only,
// done/cancelled as counts.
func TestTaskList_RenderGolden(t *testing.T) {
	tl := types.NewTaskList()
	_, err := tl.Create([]types.TaskItem{
		{Title: "intermediates", Criteria: "share = source transaction count; rates as decimals"},
		{Title: "marts"},
		{Title: "validate"},
		{Title: "snowflake mirror"},
	})
	require.NoError(t, err)
	require.NoError(t, tl.Update(1, "in_progress", ""))
	require.NoError(t, tl.Update(4, "cancelled", "not graded"))

	got := tl.Render(2400)
	assert.Equal(t, strings.Join([]string{
		"TASKS (0 done, 1 cancelled)",
		"#1 IN PROGRESS: intermediates — criteria: share = source transaction count; rates as decimals",
		"#2 pending: marts",
		"#3 pending: validate",
	}, "\n"), got)
}

// Invariant: under budget pressure pending titles truncate (with a count);
// in_progress criteria never truncate. Empty list renders "".
func TestTaskList_RenderTruncatesPendingNotInProgress(t *testing.T) {
	tl := types.NewTaskList()
	items := []types.TaskItem{{Title: "worked", Criteria: strings.Repeat("c", 120)}}
	for i := 0; i < 20; i++ {
		items = append(items, types.TaskItem{Title: strings.Repeat("p", 40)})
	}
	_, err := tl.Create(items)
	require.NoError(t, err)
	require.NoError(t, tl.Update(1, "in_progress", ""))

	got := tl.Render(400)
	assert.Contains(t, got, strings.Repeat("c", 120), "criteria survive any budget")
	assert.Contains(t, got, "more pending)", "overflow is declared, never silent")
	assert.Less(t, len(got), 400+80, "block stays near budget")

	assert.Equal(t, "", types.NewTaskList().Render(2400))
}

// Invariant: Open is true while any item is pending or in_progress.
func TestTaskList_OpenGate(t *testing.T) {
	tl := types.NewTaskList()
	_, err := tl.Create([]types.TaskItem{{Title: "a"}, {Title: "b"}})
	require.NoError(t, err)
	assert.True(t, tl.Open())

	require.NoError(t, tl.Update(1, "done", ""))
	assert.True(t, tl.Open())

	require.NoError(t, tl.Update(2, "cancelled", "dropped per task text"))
	assert.False(t, tl.Open())
}

// taskListMechLLM scripts three provider calls: create two tasks, update #1,
// then finish — capturing the exact message list each call receives.
type taskListMechLLM struct {
	call     int
	captured [][]types.Message
}

func (m *taskListMechLLM) Chat(_ context.Context, messages []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	cp := make([]types.Message, len(messages))
	copy(cp, messages)
	m.captured = append(m.captured, cp)
	m.call++
	switch m.call {
	case 1:
		return &types.LLMResponse{ToolCalls: []types.ToolCall{{
			ID: "t1", Name: "task_list",
			Input: map[string]interface{}{
				"action": "create",
				"items": []interface{}{
					map[string]interface{}{"title": "intermediates", "criteria": "share = source transaction count"},
					map[string]interface{}{"title": "marts"},
				},
			},
		}}}, nil
	case 2:
		return &types.LLMResponse{ToolCalls: []types.ToolCall{{
			ID: "t2", Name: "task_list",
			Input: map[string]interface{}{"action": "update", "id": float64(1), "status": "in_progress"},
		}}}, nil
	default:
		return &types.LLMResponse{Content: "done"}, nil
	}
}
func (m *taskListMechLLM) Name() string  { return "mock-task-mech" }
func (m *taskListMechLLM) Model() string { return "mock" }

// Mechanism: the task block rides ONLY the transient trailing system message —
// present on every call after state exists, reflecting current state, and the
// preceding (cacheable) messages are byte-stable across status churn.
func TestMech_TaskListRidesTransientTail(t *testing.T) {
	mock := &taskListMechLLM{}
	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, mock, WithConfig(&Config{
		MaxTurns:          6,
		MaxToolExecutions: 10,
		PatternConfig:     patternCfg,
	}))

	_, err := ag.Chat(context.Background(), "task-mech", "build the models")
	require.NoError(t, err)
	require.Len(t, mock.captured, 3)

	// Call 1: no task state yet — no TASKS block anywhere.
	for _, msg := range mock.captured[0] {
		assert.NotContains(t, msg.Content, "TASKS (")
	}

	// Call 2: created — trailing system message carries the render with
	// pending items and the criteria vault text.
	last2 := mock.captured[1][len(mock.captured[1])-1]
	assert.Equal(t, "system", last2.Role)
	assert.Contains(t, last2.Content, "TASKS (0 done)")
	assert.Contains(t, last2.Content, "#1 pending: intermediates")

	// Call 3: after update — tail reflects in_progress WITH criteria verbatim.
	last3 := mock.captured[2][len(mock.captured[2])-1]
	assert.Equal(t, "system", last3.Role)
	assert.Contains(t, last3.Content, "#1 IN PROGRESS: intermediates — criteria: share = source transaction count")

	// The render lives ONLY in the tail: no earlier message in call 3
	// contains the block.
	for _, msg := range mock.captured[2][:len(mock.captured[2])-1] {
		assert.NotContains(t, msg.Content, "TASKS (")
	}
}
