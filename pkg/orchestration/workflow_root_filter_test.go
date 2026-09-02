// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/task"
)

func rootTask(id string) *task.Task {
	return &task.Task{ID: id, Metadata: map[string]string{workflowRootMetadataKey: "true"}}
}

func stageTask(id string) *task.Task {
	return &task.Task{ID: id, Metadata: map[string]string{}}
}

// TestExcludeWorkflowRootTask pins the invariant recordResults depends on: the
// slice it walks holds stages ONLY, in their original order.
//
// The resume path loads the board with ListTasks, which returns the root
// alongside the stages, and recordResults maps AgentResults[i] onto
// stageTasks[i] by index. Reads order by priority then created_at, and the root
// shares MEDIUM priority with Parallel stages and Conditional branches while
// created_at is only second-resolution — so the root's position inside that tie
// is not defined by either store. A root anywhere but last takes a stage's
// output into its own notes, closes itself, and shifts every later stage by
// one, dropping the last stage's output entirely.
func TestExcludeWorkflowRootTask(t *testing.T) {
	t.Run("a root in the middle is dropped and the stages keep their order", func(t *testing.T) {
		// The failure mode. Under the MEDIUM tie this ordering is permitted, and
		// it is the one that silently corrupts the mapping.
		got := excludeWorkflowRootTask([]*task.Task{
			stageTask("stage-0"), rootTask("root"), stageTask("stage-1"), stageTask("stage-2"),
		})
		require.Len(t, got, 3, "the root must not occupy an index a stage result maps onto")
		assert.Equal(t, []string{"stage-0", "stage-1", "stage-2"},
			[]string{got[0].ID, got[1].ID, got[2].ID},
			"relative order is the mapping; reordering corrupts what dropping the root protects")
	})

	t.Run("a root last is still dropped", func(t *testing.T) {
		// The ordering that happened to work for HIGH-priority stages. It must
		// not be the reason the mapping is correct.
		got := excludeWorkflowRootTask([]*task.Task{
			stageTask("stage-0"), stageTask("stage-1"), rootTask("root"),
		})
		require.Len(t, got, 2)
		assert.Equal(t, []string{"stage-0", "stage-1"}, []string{got[0].ID, got[1].ID})
	})

	t.Run("a root first is dropped", func(t *testing.T) {
		got := excludeWorkflowRootTask([]*task.Task{
			rootTask("root"), stageTask("stage-0"), stageTask("stage-1"),
		})
		require.Len(t, got, 2)
		assert.Equal(t, []string{"stage-0", "stage-1"}, []string{got[0].ID, got[1].ID})
	})

	t.Run("a listing with no root is unchanged", func(t *testing.T) {
		// The fresh path's shape: createBoardFromPattern returns stages only, so
		// the filter must be a no-op there rather than a second source of drift.
		got := excludeWorkflowRootTask([]*task.Task{stageTask("a"), stageTask("b")})
		require.Len(t, got, 2)
		assert.Equal(t, []string{"a", "b"}, []string{got[0].ID, got[1].ID})
	})

	t.Run("an empty listing yields an empty slice, not nil-panic", func(t *testing.T) {
		assert.Empty(t, excludeWorkflowRootTask(nil))
		assert.Empty(t, excludeWorkflowRootTask([]*task.Task{}))
	})
}

// TestIsWorkflowRootTask covers the single definition the resumability scan, the
// root lookup and the resume filter all now share — a second copy that drifted
// would leave them disagreeing about what counts as a stage.
func TestIsWorkflowRootTask(t *testing.T) {
	assert.True(t, isWorkflowRootTask(rootTask("r")))
	assert.False(t, isWorkflowRootTask(stageTask("s")), "empty metadata is a stage")
	assert.False(t, isWorkflowRootTask(&task.Task{ID: "no-metadata"}),
		"a nil Metadata map must read as a stage, not panic")
	assert.False(t, isWorkflowRootTask(nil), "a nil task must not panic")
	assert.False(t, isWorkflowRootTask(&task.Task{
		Metadata: map[string]string{workflowRootMetadataKey: "TRUE"},
	}), "the marker is exactly \"true\"; anything else is a stage")
}
