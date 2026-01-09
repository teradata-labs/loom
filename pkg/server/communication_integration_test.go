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
package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/metadata"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestBusPublishSubscribe_Integration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create minimal server with agents
	agents := map[string]*agent.Agent{
		"agent1": createTestAgentForComm(t, "agent1"),
		"agent2": createTestAgentForComm(t, "agent2"),
	}
	// Use no-op tracer for tests
	tracer := observability.NewNoOpTracer()
	sessionStore, err := agent.NewSessionStore(":memory:", tracer)
	require.NoError(t, err)
	defer sessionStore.Close()

	srv := NewMultiAgentServer(agents, sessionStore)

	// Configure communication
	bus := communication.NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := communication.NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Create in-memory reference store using factory
	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	require.NoError(t, err)
	policy := communication.NewPolicyManager()

	err = srv.ConfigureCommunication(bus, queue, sharedMem, refStore, policy, logger)
	require.NoError(t, err)

	// Use cancellable context to control Subscribe goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a mock stream for Subscribe
	mockStream := &mockSubscribeStream{
		ctx:      ctx,
		messages: make(chan *loomv1.BusMessage, 10),
		done:     make(chan struct{}),
	}

	// Start subscriber in background with sync
	subscribeDone := make(chan struct{})
	go func() {
		defer close(subscribeDone)
		err := srv.Subscribe(&loomv1.SubscribeRequest{
			AgentId:      "agent1",
			TopicPattern: "test.topic",
			BufferSize:   10,
		}, mockStream)
		if err != nil && err != context.Canceled {
			t.Logf("Subscribe error: %v", err)
		}
	}()

	// Give subscriber time to set up
	time.Sleep(100 * time.Millisecond)

	// Publish message
	publishResp, err := srv.Publish(ctx, &loomv1.PublishRequest{
		Topic: "test.topic",
		Message: &loomv1.BusMessage{
			Id:        "msg1",
			Topic:     "test.topic",
			FromAgent: "agent2",
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte("test payload"),
				},
			},
			Timestamp: time.Now().UnixMilli(),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "msg1", publishResp.MessageId)
	assert.Equal(t, int32(1), publishResp.SubscriberCount)

	// Receive message
	select {
	case msg := <-mockStream.messages:
		assert.Equal(t, "msg1", msg.Id)
		assert.Equal(t, "agent2", msg.FromAgent)
		assert.Equal(t, []byte("test payload"), msg.Payload.GetValue())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	// Cancel context and wait for Subscribe goroutine to finish
	cancel()
	select {
	case <-subscribeDone:
		// Subscribe goroutine finished cleanly
	case <-time.After(1 * time.Second):
		t.Fatal("Subscribe goroutine did not finish in time")
	}
}

func TestSharedMemoryPutGet_Integration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create minimal server
	agents := map[string]*agent.Agent{
		"agent1": createTestAgentForComm(t, "agent1"),
	}
	// Use no-op tracer for tests
	tracer := observability.NewNoOpTracer()
	sessionStore, err := agent.NewSessionStore(":memory:", tracer)
	require.NoError(t, err)
	defer sessionStore.Close()

	srv := NewMultiAgentServer(agents, sessionStore)

	// Configure communication
	bus := communication.NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := communication.NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Create in-memory reference store using factory
	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	require.NoError(t, err)
	policy := communication.NewPolicyManager()

	err = srv.ConfigureCommunication(bus, queue, sharedMem, refStore, policy, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Put value
	putResp, err := srv.PutSharedMemory(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test.key",
		Value:     []byte("test value"),
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), putResp.Version)
	assert.True(t, putResp.Created)

	// Get value
	getResp, err := srv.GetSharedMemory(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "test.key",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.True(t, getResp.Found)
	assert.Equal(t, []byte("test value"), getResp.Value.Value)
	assert.Equal(t, int64(1), getResp.Value.Version)
}

func TestSharedMemoryList_Integration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create minimal server
	agents := map[string]*agent.Agent{
		"agent1": createTestAgentForComm(t, "agent1"),
	}
	// Use no-op tracer for tests
	tracer := observability.NewNoOpTracer()
	sessionStore, err := agent.NewSessionStore(":memory:", tracer)
	require.NoError(t, err)
	defer sessionStore.Close()

	srv := NewMultiAgentServer(agents, sessionStore)

	// Configure communication
	bus := communication.NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := communication.NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Create in-memory reference store using factory
	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	require.NoError(t, err)
	policy := communication.NewPolicyManager()

	err = srv.ConfigureCommunication(bus, queue, sharedMem, refStore, policy, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Put multiple keys
	for i := 0; i < 5; i++ {
		_, err := srv.PutSharedMemory(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("key%d", i),
			Value:     []byte(fmt.Sprintf("value %d", i)),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// List all keys
	listResp, err := srv.ListSharedMemoryKeys(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5), listResp.TotalCount)
	assert.Len(t, listResp.Keys, 5)
}

func TestSharedMemoryStats_Integration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create minimal server
	agents := map[string]*agent.Agent{
		"agent1": createTestAgentForComm(t, "agent1"),
	}
	// Use no-op tracer for tests
	tracer := observability.NewNoOpTracer()
	sessionStore, err := agent.NewSessionStore(":memory:", tracer)
	require.NoError(t, err)
	defer sessionStore.Close()

	srv := NewMultiAgentServer(agents, sessionStore)

	// Configure communication
	bus := communication.NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := communication.NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Create in-memory reference store using factory
	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	require.NoError(t, err)
	policy := communication.NewPolicyManager()

	err = srv.ConfigureCommunication(bus, queue, sharedMem, refStore, policy, logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Put 3 values
	for i := 0; i < 3; i++ {
		_, err := srv.PutSharedMemory(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
			Key:       fmt.Sprintf("key%d", i),
			Value:     []byte(fmt.Sprintf("value %d", i)),
			AgentId:   "agent1",
		})
		require.NoError(t, err)
	}

	// Get stats
	stats, err := srv.GetSharedMemoryStats(ctx, &loomv1.GetSharedMemoryStatsRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.KeyCount)
	assert.Equal(t, int64(3), stats.WriteCount)
	assert.Greater(t, stats.TotalBytes, int64(0))
}

// mockSubscribeStream implements loomv1.LoomService_SubscribeServer for testing
type mockSubscribeStream struct {
	ctx      context.Context
	messages chan *loomv1.BusMessage
	done     chan struct{}
}

func (m *mockSubscribeStream) Send(msg *loomv1.BusMessage) error {
	select {
	case m.messages <- msg:
		return nil
	case <-m.done:
		return fmt.Errorf("stream closed")
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func (m *mockSubscribeStream) Context() context.Context {
	return m.ctx
}

func (m *mockSubscribeStream) SendMsg(msg interface{}) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSubscribeStream) RecvMsg(msg interface{}) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSubscribeStream) SetHeader(md metadata.MD) error {
	return nil
}

func (m *mockSubscribeStream) SendHeader(md metadata.MD) error {
	return nil
}

func (m *mockSubscribeStream) SetTrailer(md metadata.MD) {
}

// createTestAgentForComm creates a minimal agent for communication testing
func createTestAgentForComm(t *testing.T, name string) *agent.Agent {
	t.Helper()

	mockBackend := &mockBackend{}
	mockLLM := &mockLLMProvider{
		responses: []string{"Mock response from " + name},
	}

	ag := agent.NewAgent(mockBackend, mockLLM,
		agent.WithName(name))
	return ag
}
