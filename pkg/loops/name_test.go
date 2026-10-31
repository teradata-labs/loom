// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package loops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run named from its task carries its name on every read, and naming it is
// not progress: when it last moved stays as it was.
func TestARunIsNamedWithoutMoving(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	moved := time.Unix(1_700_000_000, 0)
	require.NoError(t, s.Create(ctx, &Run{ID: "r1", WorkspaceID: "ws-1", SessionID: "c1", SpecName: "bench",
		Brief: "Run the ADE experiment", CreatedAt: moved}))
	_, err := s.db.ExecContext(ctx, `UPDATE loop_runs SET updated_at = ? WHERE id = 'r1'`, moved.Unix())
	require.NoError(t, err)

	require.NoError(t, s.Name(ctx, "r1", "ADE bench, Tera arm"))
	got, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "ADE bench, Tera arm", got.Title)
	assert.Equal(t, moved.Unix(), got.UpdatedAt.Unix(), "naming moved the run")

	listed, err := s.List(ctx, "ws-1")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "ADE bench, Tera arm", listed[0].Title)

	assert.ErrorIs(t, s.Name(ctx, "no-such-run", "x"), ErrNotFound)
}
