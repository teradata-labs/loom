// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"go.uber.org/zap"
)

// Helper function to create a test message queue
func createTestQueue(t *testing.T) *communication.MessageQueue {
	tmpFile := t.TempDir() + "/test-queue.db"
	queue, err := communication.NewMessageQueue(tmpFile, nil, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, queue)
	return queue
}

// Helper function to create a test shared memory store
func createTestStore(t *testing.T) *communication.SharedMemoryStore {
	store, err := communication.NewSharedMemoryStore(nil, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, store)
	return store
}

// TestSendMessageTool tests the send_message tool
func TestSendMessageTool(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		params         map[string]interface{}
		setupQueue     func(*communication.MessageQueue)
		expectSuccess  bool
		expectError    string
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name:    "successful message send",
			agentID: "agent-sender",
			params: map[string]interface{}{
				"to_agent":     "agent-receiver",
				"message":      "Hello, agent-receiver!",
				"message_type": "greeting",
				"metadata": map[string]interface{}{
					"priority": "high",
				},
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "agent-sender", data["from_agent"])
				assert.Equal(t, "agent-receiver", data["to_agent"])
				assert.Equal(t, "greeting", data["message_type"])
				assert.Equal(t, 22, data["message_size"]) // "Hello, agent-receiver!" is 22 chars
				assert.NotEmpty(t, data["sent_at"])
			},
		},
		{
			name:    "message send with default type",
			agentID: "agent-sender",
			params: map[string]interface{}{
				"to_agent": "agent-receiver",
				"message":  "Test message",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "message", data["message_type"])
			},
		},
		{
			name:    "missing to_agent parameter",
			agentID: "agent-sender",
			params: map[string]interface{}{
				"message": "Hello!",
			},
			expectSuccess: false,
			expectError:   "to_agent is required",
		},
		{
			name:    "missing message parameter",
			agentID: "agent-sender",
			params: map[string]interface{}{
				"to_agent": "agent-receiver",
			},
			expectSuccess: false,
			expectError:   "message is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create message queue
			queue := createTestQueue(t)

			// Create tool
			tool := NewSendMessageTool(queue, tt.agentID)

			// Validate tool interface
			assert.Equal(t, "send_message", tool.Name())
			assert.NotEmpty(t, tool.Description())
			assert.NotNil(t, tool.InputSchema())
			assert.Empty(t, tool.Backend())

			// Execute tool
			ctx := context.Background()
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate result
			assert.Equal(t, tt.expectSuccess, result.Success)

			if tt.expectSuccess {
				assert.Nil(t, result.Error)
				require.NotNil(t, result.Data)
				if tt.validateResult != nil {
					tt.validateResult(t, result.Data.(map[string]interface{}))
				}
			} else {
				require.NotNil(t, result.Error)
				assert.Contains(t, result.Error.Message, tt.expectError)
			}
		})
	}
}

// TestReceiveMessageTool tests the receive_message tool
func TestReceiveMessageTool(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		params         map[string]interface{}
		setupQueue     func(*communication.MessageQueue)
		expectSuccess  bool
		expectError    string
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name:    "receive message successfully",
			agentID: "agent-receiver",
			params:  map[string]interface{}{},
			setupQueue: func(queue *communication.MessageQueue) {
				// Send a message first
				payload := &loomv1.MessagePayload{
					Data: &loomv1.MessagePayload_Value{
						Value: []byte("Test message"),
					},
				}
				_, _ = queue.Send(context.Background(), "agent-sender", "agent-receiver", "test", payload, nil)
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.True(t, data["has_message"].(bool))
				assert.Equal(t, "agent-sender", data["from_agent"])
				assert.Equal(t, "test", data["message_type"])
				assert.Equal(t, "Test message", data["message"])
				assert.NotEmpty(t, data["message_id"])
			},
		},
		{
			name:          "no messages available",
			agentID:       "agent-receiver",
			params:        map[string]interface{}{},
			setupQueue:    func(queue *communication.MessageQueue) {},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.False(t, data["has_message"].(bool))
				assert.Equal(t, "no pending messages", data["reason"])
			},
		},
		{
			name:    "timeout waiting for message",
			agentID: "agent-receiver",
			params: map[string]interface{}{
				"timeout_seconds": 0.1, // 100ms timeout
			},
			setupQueue:    func(queue *communication.MessageQueue) {},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.False(t, data["has_message"].(bool))
				reason, ok := data["reason"].(string)
				require.True(t, ok)
				// Could be either timeout or no messages depending on timing
				assert.Contains(t, []string{"timeout - no messages received within timeout period", "no pending messages"}, reason)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create message queue
			queue := createTestQueue(t)

			// Setup queue if needed
			if tt.setupQueue != nil {
				tt.setupQueue(queue)
			}

			// Create tool
			tool := NewReceiveMessageTool(queue, tt.agentID)

			// Validate tool interface
			assert.Equal(t, "receive_message", tool.Name())
			assert.NotEmpty(t, tool.Description())
			assert.NotNil(t, tool.InputSchema())
			assert.Empty(t, tool.Backend())

			// Execute tool
			ctx := context.Background()
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate result
			assert.Equal(t, tt.expectSuccess, result.Success)

			if tt.expectSuccess {
				assert.Nil(t, result.Error)
				require.NotNil(t, result.Data)
				if tt.validateResult != nil {
					tt.validateResult(t, result.Data.(map[string]interface{}))
				}
			} else {
				require.NotNil(t, result.Error)
				assert.Contains(t, result.Error.Message, tt.expectError)
			}
		})
	}
}

// TestSharedMemoryWriteTool tests the shared_memory_write tool
func TestSharedMemoryWriteTool(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		params         map[string]interface{}
		expectSuccess  bool
		expectError    string
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name:    "write JSON data to global namespace",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"key":       "test-key-1",
				"value":     `{"name": "John", "age": 30}`,
				"namespace": "global",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "test-key-1", data["key"])
				assert.Equal(t, "global", data["namespace"])
				assert.Equal(t, int64(1), data["version"])
				assert.Greater(t, data["size_bytes"], 0)
			},
		},
		{
			name:    "write to workflow namespace",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"key":       "workflow-data",
				"value":     "Some workflow data",
				"namespace": "workflow",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "workflow", data["namespace"])
			},
		},
		{
			name:    "write to swarm namespace",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"key":       "swarm-config",
				"value":     "Swarm configuration",
				"namespace": "swarm",
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.Equal(t, "swarm", data["namespace"])
			},
		},
		{
			name:    "missing key parameter",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"value": "Test data",
			},
			expectSuccess: false,
			expectError:   "key is required",
		},
		{
			name:    "missing value parameter",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"key": "test-key",
			},
			expectSuccess: false,
			expectError:   "value is required",
		},
		{
			name:    "invalid namespace",
			agentID: "agent-writer",
			params: map[string]interface{}{
				"key":       "test-key",
				"value":     "Test data",
				"namespace": "invalid",
			},
			expectSuccess: false,
			expectError:   "Unknown namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create shared memory store
			store := createTestStore(t)

			// Create tool
			tool := NewSharedMemoryWriteTool(store, tt.agentID)

			// Validate tool interface
			assert.Equal(t, "shared_memory_write", tool.Name())
			assert.NotEmpty(t, tool.Description())
			assert.NotNil(t, tool.InputSchema())
			assert.Empty(t, tool.Backend())

			// Execute tool
			ctx := context.Background()
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate result
			assert.Equal(t, tt.expectSuccess, result.Success)

			if tt.expectSuccess {
				assert.Nil(t, result.Error)
				require.NotNil(t, result.Data)
				if tt.validateResult != nil {
					tt.validateResult(t, result.Data.(map[string]interface{}))
				}
			} else {
				require.NotNil(t, result.Error)
				assert.Contains(t, result.Error.Message, tt.expectError)
			}
		})
	}
}

// TestSharedMemoryReadTool tests the shared_memory_read tool
func TestSharedMemoryReadTool(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		params         map[string]interface{}
		setupStore     func(*communication.SharedMemoryStore)
		expectSuccess  bool
		expectError    string
		validateResult func(*testing.T, map[string]interface{})
	}{
		{
			name:    "read existing JSON data",
			agentID: "agent-reader",
			params: map[string]interface{}{
				"key":       "test-key-json",
				"namespace": "global",
			},
			setupStore: func(store *communication.SharedMemoryStore) {
				jsonData := map[string]interface{}{"name": "Alice", "score": 95}
				jsonBytes, _ := json.Marshal(jsonData)
				req := &loomv1.PutSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
					Key:       "test-key-json",
					Value:     jsonBytes,
					AgentId:   "agent-writer",
				}
				_, _ = store.Put(context.Background(), req)
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.True(t, data["found"].(bool))
				assert.Equal(t, "test-key-json", data["key"])
				assert.Equal(t, "global", data["namespace"])
				assert.Equal(t, "agent-writer", data["written_by"])
				assert.Equal(t, "json", data["value_type"])

				// Validate JSON value
				value, ok := data["value"].(map[string]interface{})
				require.True(t, ok, "value should be a map")
				assert.Equal(t, "Alice", value["name"])
				assert.Equal(t, float64(95), value["score"])
			},
		},
		{
			name:    "read existing text data",
			agentID: "agent-reader",
			params: map[string]interface{}{
				"key":       "test-key-text",
				"namespace": "workflow",
			},
			setupStore: func(store *communication.SharedMemoryStore) {
				req := &loomv1.PutSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
					Key:       "test-key-text",
					Value:     []byte("Plain text data"),
					AgentId:   "agent-writer",
				}
				_, _ = store.Put(context.Background(), req)
			},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.True(t, data["found"].(bool))
				assert.Equal(t, "text", data["value_type"])
				assert.Equal(t, "Plain text data", data["value"])
			},
		},
		{
			name:    "read non-existent key",
			agentID: "agent-reader",
			params: map[string]interface{}{
				"key":       "non-existent-key",
				"namespace": "global",
			},
			setupStore:    func(store *communication.SharedMemoryStore) {},
			expectSuccess: true,
			validateResult: func(t *testing.T, data map[string]interface{}) {
				assert.False(t, data["found"].(bool))
				assert.Equal(t, "non-existent-key", data["key"])
			},
		},
		{
			name:    "missing key parameter",
			agentID: "agent-reader",
			params: map[string]interface{}{
				"namespace": "global",
			},
			setupStore:    func(store *communication.SharedMemoryStore) {},
			expectSuccess: false,
			expectError:   "key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create shared memory store
			store := createTestStore(t)

			// Setup store if needed
			if tt.setupStore != nil {
				tt.setupStore(store)
			}

			// Create tool
			tool := NewSharedMemoryReadTool(store, tt.agentID)

			// Validate tool interface
			assert.Equal(t, "shared_memory_read", tool.Name())
			assert.NotEmpty(t, tool.Description())
			assert.NotNil(t, tool.InputSchema())
			assert.Empty(t, tool.Backend())

			// Execute tool
			ctx := context.Background()
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Validate result
			assert.Equal(t, tt.expectSuccess, result.Success)

			if tt.expectSuccess {
				assert.Nil(t, result.Error)
				require.NotNil(t, result.Data)
				if tt.validateResult != nil {
					tt.validateResult(t, result.Data.(map[string]interface{}))
				}
			} else {
				require.NotNil(t, result.Error)
				assert.Contains(t, result.Error.Message, tt.expectError)
			}
		})
	}
}

// TestCommunicationToolsConcurrency tests concurrent access to communication tools
func TestCommunicationToolsConcurrency(t *testing.T) {
	const numGoroutines = 10
	const messagesPerGoroutine = 5

	t.Run("concurrent message sending", func(t *testing.T) {
		queue := createTestQueue(t)
		tool := NewSendMessageTool(queue, "sender")

		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer func() { done <- true }()

				for j := 0; j < messagesPerGoroutine; j++ {
					params := map[string]interface{}{
						"to_agent": "receiver",
						"message":  "concurrent message",
					}

					result, err := tool.Execute(context.Background(), params)
					assert.NoError(t, err)
					assert.True(t, result.Success)
				}
			}(i)
		}

		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})

	t.Run("concurrent shared memory writes", func(t *testing.T) {
		store := createTestStore(t)

		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer func() { done <- true }()

				tool := NewSharedMemoryWriteTool(store, "agent")

				for j := 0; j < messagesPerGoroutine; j++ {
					params := map[string]interface{}{
						"key":       "concurrent-key",
						"value":     "concurrent value",
						"namespace": "global",
					}

					result, err := tool.Execute(context.Background(), params)
					assert.NoError(t, err)
					assert.True(t, result.Success)
				}
			}(i)
		}

		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})
}

// TestCommunicationToolsWithNilInfrastructure tests tools with nil infrastructure
func TestCommunicationToolsWithNilInfrastructure(t *testing.T) {
	t.Run("send_message with nil queue", func(t *testing.T) {
		tool := NewSendMessageTool(nil, "agent")
		params := map[string]interface{}{
			"to_agent": "receiver",
			"message":  "test",
		}

		result, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error.Code, "QUEUE_NOT_AVAILABLE")
	})

	t.Run("receive_message with nil queue", func(t *testing.T) {
		tool := NewReceiveMessageTool(nil, "agent")
		params := map[string]interface{}{}

		result, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error.Code, "QUEUE_NOT_AVAILABLE")
	})

	t.Run("shared_memory_write with nil store", func(t *testing.T) {
		tool := NewSharedMemoryWriteTool(nil, "agent")
		params := map[string]interface{}{
			"key":   "test",
			"value": "test",
		}

		result, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error.Code, "STORE_NOT_AVAILABLE")
	})

	t.Run("shared_memory_read with nil store", func(t *testing.T) {
		tool := NewSharedMemoryReadTool(nil, "agent")
		params := map[string]interface{}{
			"key": "test",
		}

		result, err := tool.Execute(context.Background(), params)
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error.Code, "STORE_NOT_AVAILABLE")
	})
}

// TestCommunicationToolsRegistry tests the registry functions
func TestCommunicationToolsRegistry(t *testing.T) {
	t.Run("CommunicationToolNames", func(t *testing.T) {
		names := CommunicationToolNames()
		// Visualization tools are NOT included by default (metaagent assigns them)
		// Point-to-point (2) + pub-sub (3) + shared memory (2) + query (2) = 9 tools
		assert.Len(t, names, 9)
		assert.Contains(t, names, "send_message")
		assert.Contains(t, names, "receive_message")
		assert.Contains(t, names, "subscribe")
		assert.Contains(t, names, "publish")
		assert.Contains(t, names, "receive_broadcast")
		assert.Contains(t, names, "shared_memory_write")
		assert.Contains(t, names, "shared_memory_read")
		assert.Contains(t, names, "top_n_query")
		assert.Contains(t, names, "group_by_query")
	})

	t.Run("CommunicationTools with all infrastructure", func(t *testing.T) {
		queue := createTestQueue(t)
		store := createTestStore(t)
		tools := CommunicationTools(queue, nil, store, "test-agent")

		// 2 message + 2 shared memory + 2 query = 6 tools (viz tools NOT included)
		assert.Len(t, tools, 6)

		// Check tool names
		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["send_message"])
		assert.True(t, names["receive_message"])
		assert.True(t, names["shared_memory_write"])
		assert.True(t, names["shared_memory_read"])
		assert.True(t, names["top_n_query"])
		assert.True(t, names["group_by_query"])
	})

	t.Run("CommunicationTools with queue only", func(t *testing.T) {
		queue := createTestQueue(t)
		tools := CommunicationTools(queue, nil, nil, "test-agent")

		// 2 message tools only (no store means no query tools, viz tools NOT included)
		assert.Len(t, tools, 2)

		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["send_message"])
		assert.True(t, names["receive_message"])
	})

	t.Run("CommunicationTools with store only", func(t *testing.T) {
		store := createTestStore(t)
		tools := CommunicationTools(nil, nil, store, "test-agent")

		// 2 shared memory + 2 query = 4 tools (viz tools NOT included)
		assert.Len(t, tools, 4)

		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["shared_memory_write"])
		assert.True(t, names["shared_memory_read"])
		assert.True(t, names["top_n_query"])
		assert.True(t, names["group_by_query"])
	})

	t.Run("CommunicationTools with no infrastructure", func(t *testing.T) {
		tools := CommunicationTools(nil, nil, nil, "test-agent")
		// No tools when no infrastructure (viz tools NOT included by default)
		assert.Len(t, tools, 0)
	})

	t.Run("VisualizationTools separate from CommunicationTools", func(t *testing.T) {
		vizTools := VisualizationTools()
		assert.Len(t, vizTools, 2)

		names := make(map[string]bool)
		for _, tool := range vizTools {
			names[tool.Name()] = true
		}
		assert.True(t, names["generate_workflow_visualization"])
		assert.True(t, names["generate_visualization"])
	})
}

// TestMessageReferencePayload tests receiving message with reference payload
func TestMessageReferencePayload(t *testing.T) {
	queue := createTestQueue(t)

	// Send message with reference payload
	payload := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Reference{
			Reference: &loomv1.Reference{
				Id:   "ref-12345",
				Type: loomv1.ReferenceType_REFERENCE_TYPE_LARGE_PAYLOAD,
			},
		},
	}

	_, err := queue.Send(context.Background(), "sender", "receiver", "test", payload, nil)
	require.NoError(t, err)

	// Receive message
	tool := NewReceiveMessageTool(queue, "receiver")
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.True(t, data["has_message"].(bool))
	assert.Contains(t, data["message"], "Reference: ref-12345")
}

// TestSharedMemoryMetadata tests shared memory with metadata
func TestSharedMemoryMetadata(t *testing.T) {
	store := createTestStore(t)

	// Write with metadata
	writeTool := NewSharedMemoryWriteTool(store, "writer")
	writeParams := map[string]interface{}{
		"key":       "data-with-metadata",
		"value":     "Test data",
		"namespace": "global",
		"metadata": map[string]interface{}{
			"source":  "test",
			"version": "1.0",
		},
	}

	writeResult, err := writeTool.Execute(context.Background(), writeParams)
	require.NoError(t, err)
	assert.True(t, writeResult.Success)

	// Read and verify metadata
	readTool := NewSharedMemoryReadTool(store, "reader")
	readParams := map[string]interface{}{
		"key":       "data-with-metadata",
		"namespace": "global",
	}

	// Add small delay to ensure write completes
	time.Sleep(10 * time.Millisecond)

	readResult, err := readTool.Execute(context.Background(), readParams)
	require.NoError(t, err)
	assert.True(t, readResult.Success)

	data := readResult.Data.(map[string]interface{})
	assert.True(t, data["found"].(bool))

	metadata, ok := data["metadata"].(map[string]string)
	if ok {
		assert.Equal(t, "test", metadata["source"])
		assert.Equal(t, "1.0", metadata["version"])
	}
}

// TestMessageQueueEventDrivenNotification verifies that the message queue notifies
// agents immediately when messages arrive, enabling event-driven workflows.
func TestMessageQueueEventDrivenNotification(t *testing.T) {
	queue := createTestQueue(t)
	defer queue.Close()

	// Create notification channel for receiver agent
	receiverID := "weather-analyst"
	notifyChan := make(chan struct{}, 10)
	queue.RegisterNotificationChannel(receiverID, notifyChan)
	defer queue.UnregisterNotificationChannel(receiverID)

	// Send a message from vacation-planner to weather-analyst
	senderID := "vacation-planner"
	sendTool := NewSendMessageTool(queue, senderID)

	params := map[string]interface{}{
		"to_agent":     receiverID,
		"message":      "What is the weather in San Marcos right now?",
		"message_type": "question",
	}

	result, err := sendTool.Execute(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success, "message send should succeed")

	// Verify notification was sent (should be immediate)
	select {
	case <-notifyChan:
		// Success! Notification received
		t.Log("Notification received immediately after message enqueue")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected notification within 100ms, but none received")
	}

	// Verify message is actually in queue
	receiveTool := NewReceiveMessageTool(queue, receiverID)
	receiveParams := map[string]interface{}{
		"timeout_seconds": float64(0), // Non-blocking
	}

	receiveResult, err := receiveTool.Execute(context.Background(), receiveParams)
	require.NoError(t, err)
	require.NotNil(t, receiveResult)
	assert.True(t, receiveResult.Success, "receive should succeed")

	data := receiveResult.Data.(map[string]interface{})
	assert.True(t, data["has_message"].(bool), "should have received the message")
	assert.Equal(t, senderID, data["from_agent"])
	assert.Equal(t, "What is the weather in San Marcos right now?", data["message"])
}
