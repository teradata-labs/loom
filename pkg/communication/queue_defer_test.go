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

package communication

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// A deferred message goes back to pending without the delivery counting
// against its retries, however often its recipient is busy.
func TestADeferredMessageKeepsItsRetries(t *testing.T) {
	queue, err := NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	require.NoError(t, err)
	defer func() { _ = queue.Close() }()
	ctx := context.Background()

	require.NoError(t, queue.Enqueue(ctx, &QueueMessage{ToAgent: "agent1", FromAgent: "agent0", MessageType: "test", MaxRetries: 1}))
	for i := 0; i < 5; i++ {
		msg, err := queue.Dequeue(ctx, "agent1")
		require.NoError(t, err)
		require.NotNil(t, msg, "delivery %d: a deferred message is delivered again", i+1)
		require.NoError(t, queue.Defer(ctx, msg.ID))
		assert.Equal(t, int32(0), msg.DequeueCount)
	}
}

// Deferring does not extend a message's life: one deferred past its expiry is
// expired at the next delivery, not delivered.
func TestADeferredMessageDoesNotOutliveItsExpiry(t *testing.T) {
	queue, err := NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	require.NoError(t, err)
	defer func() { _ = queue.Close() }()
	ctx := context.Background()

	expires := time.Now().Add(150 * time.Millisecond)
	require.NoError(t, queue.Enqueue(ctx, &QueueMessage{ToAgent: "agent1", FromAgent: "agent0", MessageType: "test", ExpiresAt: expires}))

	msg, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.NoError(t, queue.Defer(ctx, msg.ID))
	assert.Equal(t, expires.UnixMilli(), msg.ExpiresAt.UnixMilli(), "deferring leaves the expiry as it was")

	time.Sleep(time.Until(expires) + 50*time.Millisecond)

	again, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	assert.Nil(t, again, "a message deferred past its expiry is not delivered")
	assert.Equal(t, QueueMessageStatusExpired, msg.Status)
}
