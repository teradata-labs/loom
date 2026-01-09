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
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestQueueEnqueueDequeue(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue message
	msg := &QueueMessage{
		ToAgent:     "agent1",
		FromAgent:   "agent0",
		MessageType: "test",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
		Metadata: map[string]string{"key": "value"},
		Priority: 0,
	}

	err = queue.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Dequeue message
	dequeued, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	require.NotNil(t, dequeued)
	assert.Equal(t, msg.ToAgent, dequeued.ToAgent)
	assert.Equal(t, msg.FromAgent, dequeued.FromAgent)
	assert.Equal(t, QueueMessageStatusInFlight, dequeued.Status)
	assert.Equal(t, int32(1), dequeued.DequeueCount)

	// Acknowledge message
	err = queue.Acknowledge(ctx, dequeued.ID)
	require.NoError(t, err)

	// Queue should be empty now
	assert.Equal(t, 0, queue.GetQueueDepth("agent1"))
}

func TestQueuePriority(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue messages with different priorities
	for i := 0; i < 5; i++ {
		msg := &QueueMessage{
			ToAgent:     "agent1",
			FromAgent:   "agent0",
			MessageType: "test",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(fmt.Sprintf("msg%d", i)),
				},
			},
			Priority: int32(i), // 0, 1, 2, 3, 4
		}
		err = queue.Enqueue(ctx, msg)
		require.NoError(t, err)
	}

	// Dequeue should return highest priority first (4, 3, 2, 1, 0)
	expectedPriorities := []int32{4, 3, 2, 1, 0}
	for _, expectedPriority := range expectedPriorities {
		dequeued, err := queue.Dequeue(ctx, "agent1")
		require.NoError(t, err)
		require.NotNil(t, dequeued)
		assert.Equal(t, expectedPriority, dequeued.Priority)
		_ = queue.Acknowledge(ctx, dequeued.ID)
	}
}

func TestQueueRequeue(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue message
	msg := &QueueMessage{
		ToAgent:     "agent1",
		FromAgent:   "agent0",
		MessageType: "test",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
		MaxRetries: 3,
	}

	err = queue.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Dequeue and requeue
	dequeued, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	require.NotNil(t, dequeued)
	assert.Equal(t, int32(1), dequeued.DequeueCount)

	err = queue.Requeue(ctx, dequeued.ID)
	require.NoError(t, err)

	// Dequeue again
	dequeued2, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	require.NotNil(t, dequeued2)
	assert.Equal(t, dequeued.ID, dequeued2.ID)
	assert.Equal(t, int32(2), dequeued2.DequeueCount)
}

func TestQueueMaxRetries(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue message with max 3 retries
	msg := &QueueMessage{
		ToAgent:     "agent1",
		FromAgent:   "agent0",
		MessageType: "test",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
		MaxRetries: 3,
	}

	err = queue.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Dequeue and requeue twice (within limit)
	for i := 0; i < 2; i++ {
		dequeued, err := queue.Dequeue(ctx, "agent1")
		require.NoError(t, err)
		require.NotNil(t, dequeued)
		err = queue.Requeue(ctx, dequeued.ID)
		require.NoError(t, err)
	}

	// Third dequeue should succeed (dequeueCount=3, which is at limit)
	dequeued, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	require.NotNil(t, dequeued)

	// Third requeue should fail (exceeded max retries)
	err = queue.Requeue(ctx, dequeued.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeded max retries")

	// Fourth dequeue should return nil (message marked as failed)
	dequeued, err = queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	assert.Nil(t, dequeued)
}

func TestQueueExpiration(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue message that expires in 1ms
	msg := &QueueMessage{
		ToAgent:     "agent1",
		FromAgent:   "agent0",
		MessageType: "test",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
		ExpiresAt: time.Now().Add(1 * time.Millisecond),
	}

	err = queue.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	// Dequeue should return nil (message expired)
	dequeued, err := queue.Dequeue(ctx, "agent1")
	require.NoError(t, err)
	assert.Nil(t, dequeued)
}

func TestQueueConcurrentEnqueue(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Use temp file for concurrent test (SQLite :memory: doesn't handle concurrent writes well)
	tmpFile, err := os.CreateTemp("", "queue-concurrent-test-*.db")
	require.NoError(t, err)
	dbPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(dbPath)

	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue 100 messages concurrently from 10 goroutines
	const numGoroutines = 10
	const msgsPerGoroutine = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*msgsPerGoroutine)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < msgsPerGoroutine; i++ {
				msg := &QueueMessage{
					ToAgent:     "agent1",
					FromAgent:   fmt.Sprintf("agent%d", goroutineID),
					MessageType: "test",
					Payload: &loomv1.MessagePayload{
						Data: &loomv1.MessagePayload_Value{
							Value: []byte(fmt.Sprintf("msg-%d-%d", goroutineID, i)),
						},
					},
				}
				if err := queue.Enqueue(ctx, msg); err != nil {
					errors <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Enqueue error: %v", err)
	}

	// Verify all messages were enqueued
	depth := queue.GetQueueDepth("agent1")
	assert.Equal(t, numGoroutines*msgsPerGoroutine, depth)
}

func TestQueueConcurrentDequeue(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Use temp file for concurrent test
	tmpFile, err := os.CreateTemp("", "queue-concurrent-dequeue-*.db")
	require.NoError(t, err)
	dbPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(dbPath)

	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue 100 messages
	const numMessages = 100
	for i := 0; i < numMessages; i++ {
		msg := &QueueMessage{
			ToAgent:     "agent1",
			FromAgent:   "agent0",
			MessageType: "test",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(fmt.Sprintf("msg%d", i)),
				},
			},
		}
		err = queue.Enqueue(ctx, msg)
		require.NoError(t, err)
	}

	// Dequeue concurrently from 10 goroutines
	const numGoroutines = 10
	dequeued := make(chan *QueueMessage, numMessages)
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msg, err := queue.Dequeue(ctx, "agent1")
				if err != nil || msg == nil {
					break
				}
				dequeued <- msg
				_ = queue.Acknowledge(ctx, msg.ID)
			}
		}()
	}

	wg.Wait()
	close(dequeued)

	// Count dequeued messages
	count := 0
	for range dequeued {
		count++
	}

	assert.Equal(t, numMessages, count)
}

func TestQueuePersistence(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create temp file for database
	tmpFile, err := os.CreateTemp("", "queue-test-*.db")
	require.NoError(t, err)
	dbPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(dbPath)

	ctx := context.Background()

	// Create queue and enqueue messages
	queue1, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		msg := &QueueMessage{
			ToAgent:     "agent1",
			FromAgent:   "agent0",
			MessageType: "test",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(fmt.Sprintf("msg%d", i)),
				},
			},
		}
		err = queue1.Enqueue(ctx, msg)
		require.NoError(t, err)
	}

	queue1.Close()

	// Reopen queue and verify messages are recovered
	queue2, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue2.Close()

	depth := queue2.GetQueueDepth("agent1")
	assert.Equal(t, 5, depth)

	// Dequeue all messages
	for i := 0; i < 5; i++ {
		dequeued, err := queue2.Dequeue(ctx, "agent1")
		require.NoError(t, err)
		require.NotNil(t, dequeued)
		_ = queue2.Acknowledge(ctx, dequeued.ID)
	}

	assert.Equal(t, 0, queue2.GetQueueDepth("agent1"))
}

func TestQueueMultipleAgents(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Enqueue messages for 3 different agents
	for agentNum := 1; agentNum <= 3; agentNum++ {
		for i := 0; i < 10; i++ {
			msg := &QueueMessage{
				ToAgent:     fmt.Sprintf("agent%d", agentNum),
				FromAgent:   "agent0",
				MessageType: "test",
				Payload: &loomv1.MessagePayload{
					Data: &loomv1.MessagePayload_Value{
						Value: []byte(fmt.Sprintf("msg%d", i)),
					},
				},
			}
			err = queue.Enqueue(ctx, msg)
			require.NoError(t, err)
		}
	}

	// Verify each agent has 10 messages
	for agentNum := 1; agentNum <= 3; agentNum++ {
		depth := queue.GetQueueDepth(fmt.Sprintf("agent%d", agentNum))
		assert.Equal(t, 10, depth)
	}

	// Dequeue from agent2 only
	for i := 0; i < 10; i++ {
		dequeued, err := queue.Dequeue(ctx, "agent2")
		require.NoError(t, err)
		require.NotNil(t, dequeued)
		assert.Equal(t, "agent2", dequeued.ToAgent)
		_ = queue.Acknowledge(ctx, dequeued.ID)
	}

	// agent1 and agent3 should still have their messages
	assert.Equal(t, 10, queue.GetQueueDepth("agent1"))
	assert.Equal(t, 0, queue.GetQueueDepth("agent2"))
	assert.Equal(t, 10, queue.GetQueueDepth("agent3"))
}

func TestQueueClose(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Enqueue message
	msg := &QueueMessage{
		ToAgent:     "agent1",
		FromAgent:   "agent0",
		MessageType: "test",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
	}
	err = queue.Enqueue(ctx, msg)
	require.NoError(t, err)

	// Close queue
	err = queue.Close()
	require.NoError(t, err)

	// Operations after close should error
	err = queue.Enqueue(ctx, msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")

	_, err = queue.Dequeue(ctx, "agent1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestQueueValidation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	dbPath := ":memory:"
	queue, err := NewMessageQueue(dbPath, nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	ctx := context.Background()

	// Empty to_agent
	err = queue.Enqueue(ctx, &QueueMessage{
		ToAgent: "",
	})
	assert.Error(t, err)

	// Empty agent ID for dequeue
	_, err = queue.Dequeue(ctx, "")
	assert.Error(t, err)
}
