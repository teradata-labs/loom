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
		// Point-to-point (1) + pub-sub (1) + shared memory (2) + query (2) = 6 tools
		// Note: receive_message, subscribe, receive_broadcast removed (event-driven auto-injection)
		assert.Len(t, names, 6)
		assert.Contains(t, names, "send_message")
		assert.Contains(t, names, "publish")
		assert.Contains(t, names, "shared_memory_write")
		assert.Contains(t, names, "shared_memory_read")
		assert.Contains(t, names, "top_n_query")
		assert.Contains(t, names, "group_by_query")
	})

	t.Run("CommunicationTools with all infrastructure", func(t *testing.T) {
		queue := createTestQueue(t)
		store := createTestStore(t)
		tools := CommunicationTools(queue, nil, store, "test-agent")

		// 1 message + 2 shared memory + 2 query = 5 tools (viz tools NOT included)
		// Note: receive_message removed (event-driven auto-injection)
		assert.Len(t, tools, 5)

		// Check tool names
		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["send_message"])
		assert.True(t, names["shared_memory_write"])
		assert.True(t, names["shared_memory_read"])
		assert.True(t, names["top_n_query"])
		assert.True(t, names["group_by_query"])
	})

	t.Run("CommunicationTools with queue only", func(t *testing.T) {
		queue := createTestQueue(t)
		tools := CommunicationTools(queue, nil, nil, "test-agent")

		// 1 message tool only (no store means no query tools, viz tools NOT included)
		// Note: receive_message removed (event-driven auto-injection)
		assert.Len(t, tools, 1)

		names := make(map[string]bool)
		for _, tool := range tools {
			names[tool.Name()] = true
		}
		assert.True(t, names["send_message"])
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
}

// mockAgentRegistry is a mock implementation of AgentRegistry for testing
type mockAgentRegistry struct {
	configs map[string]*loomv1.AgentConfig
}

func (m *mockAgentRegistry) GetConfig(name string) *loomv1.AgentConfig {
	return m.configs[name]
}

// TestSendMessageAutoHealing tests the auto-healing functionality for workflow agent IDs
func TestSendMessageAutoHealing(t *testing.T) {
	tests := []struct {
		name                string
		senderAgentID       string
		targetAgentID       string
		registeredAgents    []string // Agent IDs registered in the mock registry
		expectResolvedTo    string   // Expected resolved agent ID
		shouldLogResolution bool     // Whether resolution should be logged
	}{
		{
			name:                "coordinator sends to short name - auto-heals to workflow-prefixed name",
			senderAgentID:       "time-reporter",
			targetAgentID:       "time-printer",
			registeredAgents:    []string{"time-reporter:time-printer"},
			expectResolvedTo:    "time-reporter:time-printer",
			shouldLogResolution: true,
		},
		{
			name:                "sub-agent sends to short name - auto-heals using workflow prefix",
			senderAgentID:       "time-reporter:sub-agent-1",
			targetAgentID:       "time-printer",
			registeredAgents:    []string{"time-reporter:time-printer"},
			expectResolvedTo:    "time-reporter:time-printer",
			shouldLogResolution: true,
		},
		{
			name:                "short name not found - uses original (no healing)",
			senderAgentID:       "time-reporter",
			targetAgentID:       "non-existent-agent",
			registeredAgents:    []string{"time-reporter:time-printer"},
			expectResolvedTo:    "non-existent-agent",
			shouldLogResolution: false,
		},
		{
			name:                "full workflow-prefixed name provided - no healing needed",
			senderAgentID:       "time-reporter",
			targetAgentID:       "time-reporter:time-printer",
			registeredAgents:    []string{"time-reporter:time-printer"},
			expectResolvedTo:    "time-reporter:time-printer",
			shouldLogResolution: false,
		},
		{
			name:                "regular agent (not workflow) - no healing",
			senderAgentID:       "standalone-agent",
			targetAgentID:       "other-agent",
			registeredAgents:    []string{"other-agent"},
			expectResolvedTo:    "other-agent",
			shouldLogResolution: false,
		},
		{
			name:                "nested sub-agent extracts correct workflow prefix",
			senderAgentID:       "workflow:sub:nested",
			targetAgentID:       "target",
			registeredAgents:    []string{"workflow:target"},
			expectResolvedTo:    "workflow:target",
			shouldLogResolution: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create message queue
			queue := createTestQueue(t)
			defer queue.Close()

			// Create mock registry
			registry := &mockAgentRegistry{
				configs: make(map[string]*loomv1.AgentConfig),
			}
			for _, agentID := range tt.registeredAgents {
				registry.configs[agentID] = &loomv1.AgentConfig{
					Name: agentID,
				}
			}

			// Create logger to capture log output
			logger := zap.NewNop()

			// Create tool with registry
			tool := NewSendMessageTool(queue, tt.senderAgentID)
			tool.SetAgentRegistry(registry, logger)

			// Execute tool
			params := map[string]interface{}{
				"to_agent": tt.targetAgentID,
				"message":  "Test message",
			}

			result, err := tool.Execute(context.Background(), params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify that the message was sent to the expected resolved agent ID
			if result.Success {
				data := result.Data.(map[string]interface{})
				assert.Equal(t, tt.expectResolvedTo, data["to_agent"],
					"Expected message to be sent to resolved agent ID")
			} else {
				// For failed cases, verify the error message mentions the expected agent ID
				if result.Error != nil {
					assert.Contains(t, result.Error.Message, tt.expectResolvedTo,
						"Error message should mention the expected (possibly unresolved) agent ID")
				}
			}
		})
	}
}

// TestExtractWorkflowName tests the workflow name extraction logic
func TestExtractWorkflowName(t *testing.T) {
	tests := []struct {
		agentID      string
		expectedName string
	}{
		{"time-reporter", "time-reporter"},
		{"time-reporter:time-printer", "time-reporter"},
		{"time-reporter:sub:nested", "time-reporter"},
		{"standalone-agent", "standalone-agent"},
		{"workflow:a:b:c", "workflow"},
		{"", ""},            // Empty string
		{":", ""},           // Just a colon
		{":agent", ""},      // Starting with colon
		{"agent:", "agent"}, // Ending with colon
		{"a:b:c:d:e", "a"},  // Multiple colons
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			result := extractWorkflowName(tt.agentID)
			assert.Equal(t, tt.expectedName, result)
		})
	}
}

// TestSendMessageAutoHealingWithoutRegistry tests that the tool works correctly without a registry
func TestSendMessageAutoHealingWithoutRegistry(t *testing.T) {
	queue := createTestQueue(t)
	defer queue.Close()

	// Create tool WITHOUT setting registry
	tool := NewSendMessageTool(queue, "sender-agent")

	params := map[string]interface{}{
		"to_agent": "receiver-agent",
		"message":  "Test message",
	}

	result, err := tool.Execute(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should succeed - no auto-healing, just uses original agent ID
	assert.True(t, result.Success)
	data := result.Data.(map[string]interface{})
	assert.Equal(t, "receiver-agent", data["to_agent"])
}
