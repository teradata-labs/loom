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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestBusPublishSubscribe(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe
	sub, err := bus.Subscribe(ctx, "agent1", "test.topic", nil, 10)
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "agent1", sub.AgentID)
	assert.Equal(t, "test.topic", sub.Topic)

	// Publish message
	msg := &loomv1.BusMessage{
		Id:        "msg1",
		Topic:     "test.topic",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("test payload"),
			},
		},
		Timestamp: time.Now().UnixMilli(),
	}

	_, _, err = bus.Publish(ctx, "test.topic", msg)
	require.NoError(t, err)

	// Receive message
	select {
	case received := <-sub.Channel:
		assert.Equal(t, "msg1", received.Id)
		assert.Equal(t, "agent0", received.FromAgent)
		assert.Equal(t, []byte("test payload"), received.Payload.GetValue())
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestBusMultipleSubscribers(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Create 3 subscribers
	sub1, err := bus.Subscribe(ctx, "agent1", "broadcast", nil, 10)
	require.NoError(t, err)

	sub2, err := bus.Subscribe(ctx, "agent2", "broadcast", nil, 10)
	require.NoError(t, err)

	sub3, err := bus.Subscribe(ctx, "agent3", "broadcast", nil, 10)
	require.NoError(t, err)

	// Publish message
	msg := &loomv1.BusMessage{
		Id:        "broadcast-msg",
		Topic:     "broadcast",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte("broadcast payload"),
			},
		},
		Timestamp: time.Now().UnixMilli(),
	}

	_, _, err = bus.Publish(ctx, "broadcast", msg)
	require.NoError(t, err)

	// All 3 should receive the message
	for i, sub := range []*Subscription{sub1, sub2, sub3} {
		select {
		case received := <-sub.Channel:
			assert.Equal(t, "broadcast-msg", received.Id, "subscriber %d", i)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timeout", i)
		}
	}
}

func TestBusMessageFiltering(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe with filter: only from agent0
	filter := &loomv1.SubscriptionFilter{
		FromAgents: []string{"agent0"},
	}
	sub, err := bus.Subscribe(ctx, "agent1", "filtered", filter, 10)
	require.NoError(t, err)

	// Publish from agent0 (should be received)
	msg1 := &loomv1.BusMessage{
		Id:        "msg1",
		Topic:     "filtered",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("from agent0")},
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "filtered", msg1)
	require.NoError(t, err)

	// Publish from agent2 (should be filtered out)
	msg2 := &loomv1.BusMessage{
		Id:        "msg2",
		Topic:     "filtered",
		FromAgent: "agent2",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("from agent2")},
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "filtered", msg2)
	require.NoError(t, err)

	// Should only receive msg1
	select {
	case received := <-sub.Channel:
		assert.Equal(t, "msg1", received.Id)
		assert.Equal(t, "agent0", received.FromAgent)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}

	// Should NOT receive msg2
	select {
	case received := <-sub.Channel:
		t.Fatalf("unexpected message: %s", received.Id)
	case <-time.After(100 * time.Millisecond):
		// Expected: no message
	}
}

func TestBusMetadataFiltering(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe with metadata filter
	filter := &loomv1.SubscriptionFilter{
		Metadata: map[string]string{
			"priority": "high",
		},
	}
	sub, err := bus.Subscribe(ctx, "agent1", "events", filter, 10)
	require.NoError(t, err)

	// Publish with matching metadata (should be received)
	msg1 := &loomv1.BusMessage{
		Id:        "msg1",
		Topic:     "events",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("high priority")},
		},
		Metadata: map[string]string{
			"priority": "high",
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "events", msg1)
	require.NoError(t, err)

	// Publish with non-matching metadata (should be filtered)
	msg2 := &loomv1.BusMessage{
		Id:        "msg2",
		Topic:     "events",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("low priority")},
		},
		Metadata: map[string]string{
			"priority": "low",
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "events", msg2)
	require.NoError(t, err)

	// Should only receive msg1
	select {
	case received := <-sub.Channel:
		assert.Equal(t, "msg1", received.Id)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Should NOT receive msg2
	select {
	case <-sub.Channel:
		t.Fatal("unexpected message")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestBusBufferOverflow(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe with tiny buffer
	sub, err := bus.Subscribe(ctx, "agent1", "flood", nil, 2)
	require.NoError(t, err)

	// Publish 10 messages rapidly (buffer can only hold 2)
	for i := 0; i < 10; i++ {
		msg := &loomv1.BusMessage{
			Id:        fmt.Sprintf("msg%d", i),
			Topic:     "flood",
			FromAgent: "agent0",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(fmt.Sprintf("payload %d", i)),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		}
		_, _, err = bus.Publish(ctx, "flood", msg)
		require.NoError(t, err)
	}

	// Should receive first 2 messages (buffer size)
	received := 0
	for {
		select {
		case <-sub.Channel:
			received++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	// Only 2 should have been received, rest dropped
	assert.Equal(t, 2, received, "should only receive buffer size messages")

	// Check stats show dropped messages
	stats, err := bus.GetTopicStats(ctx, "flood")
	require.NoError(t, err)
	assert.Equal(t, int64(10), stats.TotalPublished)
	assert.Equal(t, int64(2), stats.TotalDelivered)
	assert.Equal(t, int64(8), stats.TotalDropped)
}

func TestBusConcurrentPublish(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Create subscriber
	sub, err := bus.Subscribe(ctx, "agent1", "concurrent", nil, 1000)
	require.NoError(t, err)

	// Publish 100 messages concurrently from 10 goroutines
	const numGoroutines = 10
	const msgsPerGoroutine = 10
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < msgsPerGoroutine; i++ {
				msg := &loomv1.BusMessage{
					Id:        fmt.Sprintf("g%d-msg%d", goroutineID, i),
					Topic:     "concurrent",
					FromAgent: fmt.Sprintf("agent%d", goroutineID),
					Payload: &loomv1.MessagePayload{
						Data: &loomv1.MessagePayload_Value{
							Value: []byte(fmt.Sprintf("payload %d-%d", goroutineID, i)),
						},
					},
					Timestamp: time.Now().UnixMilli(),
				}
				_, _, _ = bus.Publish(ctx, "concurrent", msg)
			}
		}(g)
	}

	wg.Wait()

	// Count received messages
	received := 0
	timeout := time.After(2 * time.Second)
	for received < numGoroutines*msgsPerGoroutine {
		select {
		case <-sub.Channel:
			received++
		case <-timeout:
			t.Fatalf("timeout after receiving %d/%d messages", received, numGoroutines*msgsPerGoroutine)
		}
	}

	assert.Equal(t, numGoroutines*msgsPerGoroutine, received, "should receive all messages")
}

func TestBusUnsubscribe(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe
	sub, err := bus.Subscribe(ctx, "agent1", "test", nil, 10)
	require.NoError(t, err)

	// Publish message (should be received)
	msg1 := &loomv1.BusMessage{
		Id:        "msg1",
		Topic:     "test",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("before unsubscribe")},
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "test", msg1)
	require.NoError(t, err)

	select {
	case <-sub.Channel:
		// Expected
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Unsubscribe
	err = bus.Unsubscribe(ctx, sub.ID)
	require.NoError(t, err)

	// Publish message (should NOT be received, channel should be closed)
	msg2 := &loomv1.BusMessage{
		Id:        "msg2",
		Topic:     "test",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("after unsubscribe")},
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "test", msg2)
	require.NoError(t, err)

	// Channel should be closed
	select {
	case _, ok := <-sub.Channel:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}

func TestBusListTopics(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe to multiple topics
	_, err := bus.Subscribe(ctx, "agent1", "topic1", nil, 10)
	require.NoError(t, err)

	_, err = bus.Subscribe(ctx, "agent1", "topic2", nil, 10)
	require.NoError(t, err)

	_, err = bus.Subscribe(ctx, "agent2", "topic3", nil, 10)
	require.NoError(t, err)

	// List topics
	topics, err := bus.ListTopics(ctx)
	require.NoError(t, err)
	assert.Len(t, topics, 3)
	assert.Contains(t, topics, "topic1")
	assert.Contains(t, topics, "topic2")
	assert.Contains(t, topics, "topic3")
}

func TestBusTopicStats(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Subscribe
	_, err := bus.Subscribe(ctx, "agent1", "stats-test", nil, 10)
	require.NoError(t, err)

	// Publish 5 messages
	for i := 0; i < 5; i++ {
		msg := &loomv1.BusMessage{
			Id:        fmt.Sprintf("msg%d", i),
			Topic:     "stats-test",
			FromAgent: "agent0",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{Value: []byte("test")},
			},
			Timestamp: time.Now().UnixMilli(),
		}
		_, _, err = bus.Publish(ctx, "stats-test", msg)
		require.NoError(t, err)
	}

	// Get stats
	stats, err := bus.GetTopicStats(ctx, "stats-test")
	require.NoError(t, err)
	assert.Equal(t, "stats-test", stats.Topic)
	assert.Equal(t, int64(5), stats.TotalPublished)
	assert.Equal(t, int64(5), stats.TotalDelivered)
	assert.Equal(t, int64(0), stats.TotalDropped)
	assert.Equal(t, int32(1), stats.ActiveSubscribers)
	assert.Greater(t, stats.CreatedAt, int64(0))
	assert.Greater(t, stats.LastPublishAt, int64(0))
}

func TestBusClose(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)

	ctx := context.Background()

	// Subscribe
	sub, err := bus.Subscribe(ctx, "agent1", "test", nil, 10)
	require.NoError(t, err)

	// Close bus
	err = bus.Close()
	require.NoError(t, err)

	// Channel should be closed
	select {
	case _, ok := <-sub.Channel:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for channel close")
	}

	// Publishing after close should error
	msg := &loomv1.BusMessage{
		Id:        "msg1",
		Topic:     "test",
		FromAgent: "agent0",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: []byte("test")},
		},
		Timestamp: time.Now().UnixMilli(),
	}
	_, _, err = bus.Publish(ctx, "test", msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestBusValidation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	ctx := context.Background()

	// Empty topic
	_, err := bus.Subscribe(ctx, "agent1", "", nil, 10)
	assert.Error(t, err)

	// Empty agent ID
	_, err = bus.Subscribe(ctx, "", "topic", nil, 10)
	assert.Error(t, err)

	// Empty topic for publish
	msg := &loomv1.BusMessage{Id: "msg1"}
	_, _, err = bus.Publish(ctx, "", msg)
	assert.Error(t, err)

	// Nil message
	_, _, err = bus.Publish(ctx, "topic", nil)
	assert.Error(t, err)
}
