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
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
)

// MockReferenceStore implements ReferenceStore for testing
type MockReferenceStore struct {
	data map[string][]byte
}

func NewMockReferenceStore() *MockReferenceStore {
	return &MockReferenceStore{
		data: make(map[string][]byte),
	}
}

func (m *MockReferenceStore) Store(ctx context.Context, data []byte, opts communication.StoreOptions) (*loomv1.Reference, error) {
	refID := generateMessageID()
	m.data[refID] = data

	return &loomv1.Reference{
		Id:        refID,
		Type:      opts.Type,
		Store:     loomv1.ReferenceStore_REFERENCE_STORE_MEMORY,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: 0,
	}, nil
}

func (m *MockReferenceStore) Resolve(ctx context.Context, ref *loomv1.Reference) ([]byte, error) {
	data, ok := m.data[ref.Id]
	if !ok {
		return nil, assert.AnError
	}
	return data, nil
}

func (m *MockReferenceStore) Retain(ctx context.Context, refID string) error {
	return nil
}

func (m *MockReferenceStore) Release(ctx context.Context, refID string) error {
	return nil
}

func (m *MockReferenceStore) List(ctx context.Context) ([]*loomv1.Reference, error) {
	return nil, nil
}

func (m *MockReferenceStore) Stats(ctx context.Context) (*communication.StoreStats, error) {
	return &communication.StoreStats{}, nil
}

func (m *MockReferenceStore) Close() error {
	return nil
}

// Helper to create a PolicyManager for testing
func newTestPolicyManager(alwaysReference bool, thresholdSize int64) *communication.PolicyManager {
	pm := communication.NewPolicyManager()

	// Configure the default policy that applies to all message types
	var defaultPolicy *loomv1.CommunicationPolicy
	if alwaysReference {
		// Set to always use reference
		defaultPolicy = &loomv1.CommunicationPolicy{
			Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_ALWAYS_REFERENCE,
			MessageType: "default",
		}
	} else {
		// Set auto-promote threshold
		defaultPolicy = &loomv1.CommunicationPolicy{
			Tier:        loomv1.CommunicationTier_COMMUNICATION_TIER_AUTO_PROMOTE,
			MessageType: "default",
			AutoPromote: &loomv1.AutoPromoteConfig{
				Enabled:        true,
				ThresholdBytes: thresholdSize,
			},
		}
	}

	// Apply policy to all common message types used in tests
	messageTypes := []string{"default", "session_state", "workflow_context", "control", "tool_result", "pattern_ref"}
	for _, msgType := range messageTypes {
		policy := &loomv1.CommunicationPolicy{
			Tier:        defaultPolicy.Tier,
			MessageType: msgType,
			AutoPromote: defaultPolicy.AutoPromote,
		}
		pm.SetPolicy(msgType, policy)
	}

	return pm
}

func TestAgent_Send_ValueSemantics(t *testing.T) {
	ctx := context.Background()

	// Create agent with value-only policy (threshold = 1MB, always use value)
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(false, 1024*1024)

	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Send small message (should use value)
	data := map[string]interface{}{
		"key": "value",
		"num": 42,
	}

	msg, err := agent.Send(ctx, "agent2", "control", data)
	require.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, "agent1", msg.FromAgent)
	assert.Equal(t, "agent2", msg.ToAgent)

	// Verify payload uses value
	assert.NotNil(t, msg.Payload.GetValue())
	assert.Nil(t, msg.Payload.GetReference())

	// Verify data can be unmarshaled
	var result map[string]interface{}
	err = json.Unmarshal(msg.Payload.GetValue(), &result)
	require.NoError(t, err)
	assert.Equal(t, "value", result["key"])
	assert.Equal(t, float64(42), result["num"]) // JSON numbers are float64
}

func TestAgent_Send_ReferenceSemantics(t *testing.T) {
	ctx := context.Background()

	// Create agent with always-reference policy
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(true, 0)

	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Send message (should use reference)
	data := map[string]interface{}{
		"large_data": "some data that should be stored as reference",
	}

	msg, err := agent.Send(ctx, "agent2", "session_state", data)
	require.NoError(t, err)
	assert.NotNil(t, msg)

	// Verify payload uses reference
	assert.Nil(t, msg.Payload.GetValue())
	assert.NotNil(t, msg.Payload.GetReference())

	// Verify reference is valid
	ref := msg.Payload.GetReference()
	assert.NotEmpty(t, ref.Id)
	assert.Equal(t, loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE, ref.Type)
	assert.Equal(t, loomv1.ReferenceStore_REFERENCE_STORE_MEMORY, ref.Store)
}

func TestAgent_Receive_ValueSemantics(t *testing.T) {
	ctx := context.Background()

	// Create agent
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(false, 1024*1024)

	agent := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Create message with value payload
	data := map[string]interface{}{
		"key": "value",
		"num": 42,
	}
	dataBytes, err := json.Marshal(data)
	require.NoError(t, err)

	msg := &loomv1.CommunicationMessage{
		Id:        "test-msg-1",
		FromAgent: "agent1",
		ToAgent:   "agent2",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: dataBytes,
			},
		},
		Timestamp: time.Now().Unix(),
	}

	// Receive and verify
	result, err := agent.Receive(ctx, msg)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "value", resultMap["key"])
	assert.Equal(t, float64(42), resultMap["num"])
}

func TestAgent_Receive_ReferenceSemantics(t *testing.T) {
	ctx := context.Background()

	// Create agent
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(true, 0)

	agent := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Store data in reference store
	data := map[string]interface{}{
		"large_data": "some data stored as reference",
	}
	dataBytes, err := json.Marshal(data)
	require.NoError(t, err)

	ref, err := refStore.Store(ctx, dataBytes, communication.StoreOptions{
		Type: loomv1.ReferenceType_REFERENCE_TYPE_WORKFLOW_CONTEXT,
	})
	require.NoError(t, err)

	// Create message with reference payload
	msg := &loomv1.CommunicationMessage{
		Id:        "test-msg-2",
		FromAgent: "agent1",
		ToAgent:   "agent2",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Reference{
				Reference: ref,
			},
		},
		Timestamp: time.Now().Unix(),
	}

	// Receive and verify
	result, err := agent.Receive(ctx, msg)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "some data stored as reference", resultMap["large_data"])
}

func TestAgent_SendReceive_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// Create two agents sharing same reference store
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(true, 0)

	agent1 := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	agent2 := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Agent1 sends message to Agent2
	data := map[string]interface{}{
		"workflow_step": "processing",
		"progress":      75,
	}

	msg, err := agent1.Send(ctx, "agent2", "workflow_context", data)
	require.NoError(t, err)

	// Agent2 receives message from Agent1
	result, err := agent2.Receive(ctx, msg)
	require.NoError(t, err)

	// Verify data round-trip
	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "processing", resultMap["workflow_step"])
	assert.Equal(t, float64(75), resultMap["progress"])
}

func TestAgent_Send_MissingReferenceStore(t *testing.T) {
	ctx := context.Background()

	// Create agent without reference store
	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		commPolicy: newTestPolicyManager(false, 1024*1024),
	}

	data := map[string]interface{}{"key": "value"}
	msg, err := agent.Send(ctx, "agent2", "control", data)

	assert.Error(t, err)
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "reference store not configured")
}

func TestAgent_Send_MissingPolicy(t *testing.T) {
	ctx := context.Background()

	// Create agent without communication policy
	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore: NewMockReferenceStore(),
	}

	data := map[string]interface{}{"key": "value"}
	msg, err := agent.Send(ctx, "agent2", "control", data)

	assert.Error(t, err)
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "communication policy not configured")
}

func TestAgent_Receive_NilMessage(t *testing.T) {
	ctx := context.Background()

	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   NewMockReferenceStore(),
		commPolicy: newTestPolicyManager(false, 1024*1024),
	}

	result, err := agent.Receive(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "nil message")
}

func TestAgent_Send_AutoPromote(t *testing.T) {
	ctx := context.Background()

	// Create agent with auto-promote policy (threshold = 100 bytes)
	refStore := NewMockReferenceStore()
	policy := newTestPolicyManager(false, 100)

	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   refStore,
		commPolicy: policy,
	}

	// Send small message (< 100 bytes, should use value)
	smallData := map[string]interface{}{
		"small": "data",
	}

	msg1, err := agent.Send(ctx, "agent2", "control", smallData)
	require.NoError(t, err)
	assert.NotNil(t, msg1.Payload.GetValue())
	assert.Nil(t, msg1.Payload.GetReference())

	// Send large message (> 100 bytes, should use reference)
	largeData := map[string]interface{}{
		"large": "this is a much larger payload that exceeds the 100 byte threshold and should trigger auto-promotion to reference-based communication",
	}

	msg2, err := agent.Send(ctx, "agent2", "tool_result", largeData)
	require.NoError(t, err)
	assert.Nil(t, msg2.Payload.GetValue())
	assert.NotNil(t, msg2.Payload.GetReference())
}

func TestAgent_InferReferenceType(t *testing.T) {
	tests := []struct {
		messageType  string
		expectedType loomv1.ReferenceType
	}{
		{"session_state", loomv1.ReferenceType_REFERENCE_TYPE_SESSION_STATE},
		{"workflow_context", loomv1.ReferenceType_REFERENCE_TYPE_WORKFLOW_CONTEXT},
		{"collaboration_state", loomv1.ReferenceType_REFERENCE_TYPE_COLLABORATION_STATE},
		{"tool_result", loomv1.ReferenceType_REFERENCE_TYPE_TOOL_RESULT},
		{"pattern_data", loomv1.ReferenceType_REFERENCE_TYPE_PATTERN_DATA},
		{"trace", loomv1.ReferenceType_REFERENCE_TYPE_OBSERVABILITY_TRACE},
		{"unknown", loomv1.ReferenceType_REFERENCE_TYPE_LARGE_PAYLOAD},
	}

	for _, tt := range tests {
		t.Run(tt.messageType, func(t *testing.T) {
			result := inferReferenceType(tt.messageType)
			assert.Equal(t, tt.expectedType, result)
		})
	}
}

// Tests for Message Queue Integration

func TestAgent_SendAsync_WithQueue(t *testing.T) {
	ctx := context.Background()

	// Create message queue
	queue, err := communication.NewMessageQueue(":memory:", nil, nil)
	require.NoError(t, err)
	defer queue.Close()

	// Create agent with message queue
	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:     NewMockReferenceStore(),
		commPolicy:   newTestPolicyManager(false, 1024*1024),
		messageQueue: queue,
	}

	// Send async message
	data := map[string]interface{}{
		"task": "process_data",
		"id":   123,
	}

	messageID, err := agent.SendAsync(ctx, "agent2", "control", data)
	require.NoError(t, err)
	assert.NotEmpty(t, messageID)

	// Verify message was enqueued
	depth := queue.GetQueueDepth("agent2")
	assert.Equal(t, 1, depth)
}

func TestAgent_SendAsync_WithoutQueue(t *testing.T) {
	ctx := context.Background()

	// Create agent without message queue
	agent := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:   NewMockReferenceStore(),
		commPolicy: newTestPolicyManager(false, 1024*1024),
	}

	// Send async message should fail
	data := map[string]interface{}{"key": "value"}
	messageID, err := agent.SendAsync(ctx, "agent2", "control", data)

	assert.Error(t, err)
	assert.Empty(t, messageID)
	assert.Contains(t, err.Error(), "message queue not configured")
}

func TestAgent_ReceiveWithTimeout_WithQueue(t *testing.T) {
	ctx := context.Background()

	// Create message queue
	queue, err := communication.NewMessageQueue(":memory:", nil, nil)
	require.NoError(t, err)
	defer queue.Close()

	// Create agent with message queue
	agent := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:     NewMockReferenceStore(),
		commPolicy:   newTestPolicyManager(false, 1024*1024),
		messageQueue: queue,
	}

	// Enqueue a message for the agent
	data := map[string]interface{}{"task": "test"}
	dataBytes, err := json.Marshal(data)
	require.NoError(t, err)

	payload := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Value{
			Value: dataBytes,
		},
		Metadata: &loomv1.PayloadMetadata{
			SizeBytes:   int64(len(dataBytes)),
			ContentType: "application/json",
		},
	}

	queueMsg := &communication.QueueMessage{
		ID:          "test-msg-1",
		ToAgent:     "agent2",
		FromAgent:   "agent1",
		MessageType: "control",
		Payload:     payload,
		Metadata:    make(map[string]string),
		EnqueuedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		MaxRetries:  3,
	}

	err = queue.Enqueue(ctx, queueMsg)
	require.NoError(t, err)

	// Receive message with timeout
	msg, err := agent.ReceiveWithTimeout(ctx, 2*time.Second)
	require.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, "test-msg-1", msg.Id)
	assert.Equal(t, "agent1", msg.FromAgent)
	assert.Equal(t, "agent2", msg.ToAgent)
}

func TestAgent_ReceiveWithTimeout_Timeout(t *testing.T) {
	ctx := context.Background()

	// Create message queue (empty)
	queue, err := communication.NewMessageQueue(":memory:", nil, nil)
	require.NoError(t, err)
	defer queue.Close()

	// Create agent with message queue
	agent := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:     NewMockReferenceStore(),
		commPolicy:   newTestPolicyManager(false, 1024*1024),
		messageQueue: queue,
	}

	// Receive should timeout
	msg, err := agent.ReceiveWithTimeout(ctx, 200*time.Millisecond)
	assert.NoError(t, err) // Timeout is not an error
	assert.Nil(t, msg)     // No message received
}

func TestAgent_SendAsync_ReceiveWithTimeout_Integration(t *testing.T) {
	ctx := context.Background()

	// Create shared message queue
	queue, err := communication.NewMessageQueue(":memory:", nil, nil)
	require.NoError(t, err)
	defer queue.Close()

	// Create two agents sharing the queue
	agent1 := &Agent{
		config: &Config{
			Name: "agent1",
		},
		refStore:     NewMockReferenceStore(),
		commPolicy:   newTestPolicyManager(false, 1024*1024),
		messageQueue: queue,
	}

	agent2 := &Agent{
		config: &Config{
			Name: "agent2",
		},
		refStore:     NewMockReferenceStore(),
		commPolicy:   newTestPolicyManager(false, 1024*1024),
		messageQueue: queue,
	}

	// Agent1 sends async message to Agent2
	data := map[string]interface{}{
		"task":     "analyze_data",
		"priority": "high",
	}

	messageID, err := agent1.SendAsync(ctx, "agent2", "control", data)
	require.NoError(t, err)
	assert.NotEmpty(t, messageID)

	// Agent2 receives the message
	msg, err := agent2.ReceiveWithTimeout(ctx, 2*time.Second)
	require.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, "agent1", msg.FromAgent)
	assert.Equal(t, "agent2", msg.ToAgent)

	// Verify payload
	assert.NotNil(t, msg.Payload)
	receivedData := msg.Payload.GetValue()
	var resultMap map[string]interface{}
	err = json.Unmarshal(receivedData, &resultMap)
	require.NoError(t, err)
	assert.Equal(t, "analyze_data", resultMap["task"])
	assert.Equal(t, "high", resultMap["priority"])
}
