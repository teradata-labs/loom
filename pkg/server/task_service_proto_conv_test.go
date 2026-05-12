// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestProtoToBoard_PreservesClientID guards the CreateBoard fix: the proto
// converter must thread the client-supplied id through to task.TaskBoard so
// the storage layer's "generate UUID only when empty" branch is reachable.
// Without this, every CreateBoard call silently received a fresh UUID, which
// in turn meant agent configs pinning skill_task_board_id had no way to
// reference a board they wanted to pre-create.
func TestProtoToBoard_PreservesClientID(t *testing.T) {
	got := protoToBoard(&loomv1.TaskBoard{
		Id:         "release-audit-board",
		Name:       "Release Audit",
		WorkflowId: "wf-1",
	})
	require.NotNil(t, got)
	assert.Equal(t, "release-audit-board", got.ID,
		"client-supplied board.id must be honored")
	assert.Equal(t, "Release Audit", got.Name)
	assert.Equal(t, "wf-1", got.WorkflowID)
}

func TestProtoToBoard_EmptyIDPropagatesEmpty(t *testing.T) {
	// Storage layer is responsible for UUID synthesis when ID is empty;
	// the converter just passes through.
	got := protoToBoard(&loomv1.TaskBoard{Name: "no id"})
	require.NotNil(t, got)
	assert.Empty(t, got.ID,
		"empty proto id must propagate so storage can synthesize a UUID")
}
