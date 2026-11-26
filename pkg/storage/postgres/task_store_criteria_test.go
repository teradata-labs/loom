// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/task"
)

// TestClassifyCriteriaProbe pins SetAcceptanceCriteria's disambiguation split
// without a live database — the reason the classification is a named function.
// Only a proven missing row (pgx.ErrNoRows) may read as "not found"; any other
// probe failure is a verification error, because the agent's task_board tool
// repeats this message to the model verbatim and an outage narrated as absence
// made the agent re-plan around a task that existed. This mirrors the sqlite
// store's classifier and test one for one.
func TestClassifyCriteriaProbe(t *testing.T) {
	t.Run("missing row is not found", func(t *testing.T) {
		err := classifyCriteriaProbe(fmt.Errorf("wrapped: %w", pgx.ErrNoRows), "t1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.False(t, errors.Is(err, task.ErrAcceptanceCriteriaLocked))
	})
	t.Run("probe failure is a verification error, not absence", func(t *testing.T) {
		err := classifyCriteriaProbe(fmt.Errorf("connection reset by peer"), "t1")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "not found",
			"an I/O failure must not be narrated as an absent task")
		assert.False(t, errors.Is(err, task.ErrAcceptanceCriteriaLocked))
	})
	t.Run("row exists means locked", func(t *testing.T) {
		err := classifyCriteriaProbe(nil, "t1")
		require.Error(t, err)
		assert.True(t, errors.Is(err, task.ErrAcceptanceCriteriaLocked))
	})
}
