// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNotifyStreamActivity_NoHookIsNoOp(t *testing.T) {
	assert.False(t, NotifyStreamActivity(context.Background()))
}

func TestNotifyStreamActivity_InvokesHook(t *testing.T) {
	calls := 0
	ctx := WithStreamActivity(context.Background(), func() { calls++ })

	assert.True(t, NotifyStreamActivity(ctx))
	assert.True(t, NotifyStreamActivity(ctx))
	assert.Equal(t, 2, calls)
}

func TestWithStreamActivity_NilHookLeavesContextUnchanged(t *testing.T) {
	parent := context.Background()
	assert.Equal(t, parent, WithStreamActivity(parent, nil))
	assert.False(t, NotifyStreamActivity(WithStreamActivity(parent, nil)))
}
