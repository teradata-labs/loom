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

// TestTriModalIntegration_WorkflowCoordination tests all three modes working together
// in a realistic workflow coordination scenario:
// - Shared Memory: Stores workflow state
// - Broadcast Bus: Notifies agents of state changes
// - Message Queue: Agent-to-agent task messages
func TestTriModalIntegration_WorkflowCoordination(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Setup: Create all three communication modes
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Scenario: Workflow with 3 agents coordinating a multi-step task
	// Agent1: Coordinator (updates workflow state, broadcasts notifications)
	// Agent2: Worker (receives tasks via queue, updates progress via shared memory)
	// Agent3: Monitor (subscribes to notifications, reads shared memory)

	// Agent3: Subscribe to workflow notifications
	monitorSub, err := bus.Subscribe(ctx, "agent3", "workflow.*", nil, 10)
	require.NoError(t, err)
	defer func() {
		_ = bus.Unsubscribe(ctx, monitorSub.ID)
	}()

	var monitorNotifications []string
	var monitorMu sync.Mutex
	monitorDone := make(chan struct{})

	go func() {
		defer close(monitorDone)
		for i := 0; i < 3; i++ { // Expect 3 notifications
			select {
			case msg := <-monitorSub.Channel:
				monitorMu.Lock()
				monitorNotifications = append(monitorNotifications, string(msg.Payload.GetValue()))
				monitorMu.Unlock()
			case <-time.After(5 * time.Second):
				t.Logf("Monitor timeout waiting for notification %d", i+1)
				return
			}
		}
	}()

	// Step 1: Coordinator initializes workflow state in Shared Memory
	putResp, err := sharedMem.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "task-123-state",
		Value:     []byte(`{"status":"initialized","step":0}`),
		AgentId:   "agent1",
		Metadata:  map[string]string{"workflow": "task-123"},
	})
	require.NoError(t, err)
	assert.NotNil(t, putResp)

	// Step 2: Coordinator broadcasts workflow start notification
	_, _, err = bus.Publish(ctx, "workflow.started", &loomv1.BusMessage{
		Id:        "notif-1",
		Topic:     "workflow.started",
		FromAgent: "agent1",
		Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("Workflow task-123 started")}},
		Timestamp: time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	// Step 3: Coordinator sends task to Agent2 via Message Queue
	taskMsg := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Value{
			Value: []byte(`{"task":"process-data","workflow_id":"task-123"}`),
		},
	}
	_, err = queue.Send(ctx, "agent1", "agent2", "workflow.task", taskMsg, nil)
	require.NoError(t, err)

	// Step 4: Agent2 receives task from queue
	receivedTask, err := queue.Dequeue(ctx, "agent2")
	require.NoError(t, err)
	require.NotNil(t, receivedTask)
	assert.Equal(t, "agent2", receivedTask.ToAgent)
	assert.Equal(t, "agent1", receivedTask.FromAgent)

	// Step 5: Agent2 acknowledges task receipt
	err = queue.Acknowledge(ctx, receivedTask.ID)
	require.NoError(t, err)

	// Step 6: Agent2 updates workflow progress in Shared Memory
	_, err = sharedMem.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "task-123-state",
		Value:     []byte(`{"status":"processing","step":1,"agent":"agent2"}`),
		AgentId:   "agent2",
	})
	require.NoError(t, err)

	// Step 7: Coordinator broadcasts progress notification
	_, _, err = bus.Publish(ctx, "workflow.progress", &loomv1.BusMessage{
		Id:        "notif-2",
		Topic:     "workflow.progress",
		FromAgent: "agent1",
		Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("Task processing by agent2")}},
		Timestamp: time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	// Step 8: Agent2 completes task and updates state
	_, err = sharedMem.Put(ctx, &loomv1.PutSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "task-123-state",
		Value:     []byte(`{"status":"completed","step":2,"agent":"agent2","result":"success"}`),
		AgentId:   "agent2",
	})
	require.NoError(t, err)

	// Step 9: Coordinator broadcasts completion notification
	_, _, err = bus.Publish(ctx, "workflow.completed", &loomv1.BusMessage{
		Id:        "notif-3",
		Topic:     "workflow.completed",
		FromAgent: "agent1",
		Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("Workflow task-123 completed successfully")}},
		Timestamp: time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	// Step 10: Agent3 (monitor) verifies all notifications received
	select {
	case <-monitorDone:
		// Success
	case <-time.After(6 * time.Second):
		t.Fatal("Monitor did not receive all notifications in time")
	}

	monitorMu.Lock()
	assert.Len(t, monitorNotifications, 3, "Monitor should receive 3 notifications")
	assert.Contains(t, string(monitorNotifications[0]), "started")
	assert.Contains(t, string(monitorNotifications[1]), "processing")
	assert.Contains(t, string(monitorNotifications[2]), "completed")
	monitorMu.Unlock()

	// Step 11: Agent3 reads final workflow state from Shared Memory
	getResp, err := sharedMem.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "task-123-state",
		AgentId:   "agent3",
	})
	require.NoError(t, err)
	require.True(t, getResp.Found)
	assert.Contains(t, string(getResp.Value.Value), "completed")
	assert.Contains(t, string(getResp.Value.Value), "success")

	// Verify statistics
	// After acknowledgment, the message is removed from the queue (expected behavior)
	assert.Equal(t, 0, queue.GetQueueDepth("agent2"), "Queue should be empty after acknowledgment")
	assert.Equal(t, int64(1), queue.totalEnqueued.Load(), "Should have enqueued 1 message")
	assert.Equal(t, int64(1), queue.totalDequeued.Load(), "Should have dequeued 1 message")
	assert.Equal(t, int64(1), queue.totalAcked.Load(), "Should have acknowledged 1 message")

	statsResp, err := sharedMem.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(1), statsResp.KeyCount, "Should have 1 key in GLOBAL namespace")
}

// TestTriModalIntegration_RequestResponseWithSharedState tests request-response
// messaging combined with shared state for result storage
func TestTriModalIntegration_RequestResponseWithSharedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := zaptest.NewLogger(t)

	// Setup
	queue, err := NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	// Subscribe to processing events
	eventSub, err := bus.Subscribe(ctx, "monitor", "task.*", nil, 10)
	require.NoError(t, err)
	defer func() {
		_ = bus.Unsubscribe(ctx, eventSub.ID)
	}()

	var processingEvents []string
	var eventsMu sync.Mutex

	go func() {
		for {
			select {
			case msg := <-eventSub.Channel:
				eventsMu.Lock()
				processingEvents = append(processingEvents, string(msg.Payload.GetValue()))
				eventsMu.Unlock()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Scenario: Agent1 requests computation from Agent2
	// Agent2 processes requests, stores results in Shared Memory, broadcasts events

	// Track requests processed
	var requestsProcessed int
	var processMu sync.Mutex

	// Simulate Agent2 dequeue loop
	agent2Done := make(chan struct{})
	go func() {
		defer close(agent2Done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Dequeue request
				msg, err := queue.Dequeue(ctx, "agent2")
				if err != nil || msg == nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}

				processMu.Lock()
				requestsProcessed++
				processMu.Unlock()

				// Compute result based on message type
				result := []byte(fmt.Sprintf("computed-result-for-%s", msg.MessageType))

				// Store result in Shared Memory
				resultKey := fmt.Sprintf("result-%s", msg.MessageType)
				_, _ = sharedMem.Put(ctx, &loomv1.PutSharedMemoryRequest{
					Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
					Key:       resultKey,
					Value:     result,
					AgentId:   "agent2",
					Metadata:  map[string]string{"task_type": msg.MessageType},
				})

				// Broadcast processing event
				_, _, _ = bus.Publish(ctx, "task.processed", &loomv1.BusMessage{
					Id:        fmt.Sprintf("event-%s", msg.ID),
					Topic:     "task.processed",
					FromAgent: "agent2",
					Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte(msg.MessageType)}},
					Timestamp: time.Now().UnixMilli(),
				})

				// Send response with same correlation ID
				// Note: Responses with correlation IDs are routed directly to waiting SendAndReceive
				responseMsg := &QueueMessage{
					ID:            fmt.Sprintf("%s-response", msg.ID),
					ToAgent:       msg.FromAgent,
					FromAgent:     "agent2",
					MessageType:   "response",
					Payload:       &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: result}},
					CorrelationID: msg.CorrelationID,
					Priority:      0,
					EnqueuedAt:    time.Now(),
					ExpiresAt:     time.Now().Add(1 * time.Hour),
					MaxRetries:    3,
					Status:        QueueMessageStatusPending,
				}
				_ = queue.Enqueue(ctx, responseMsg)
				_ = queue.Acknowledge(ctx, msg.ID)
			}
		}
	}()

	// Give worker time to start
	time.Sleep(50 * time.Millisecond)

	// Agent1 sends first request
	request1 := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Value{
			Value: []byte("compute-fibonacci-10"),
		},
	}
	response1, err := queue.SendAndReceive(ctx, "agent1", "agent2", "task-1", request1, nil, 5)
	require.NoError(t, err)
	require.NotNil(t, response1)
	assert.Contains(t, string(response1.GetValue()), "computed-result-for-task-1")

	// Agent1 sends second request with different task type
	request2 := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Value{
			Value: []byte("compute-prime-100"),
		},
	}
	response2, err := queue.SendAndReceive(ctx, "agent1", "agent2", "task-2", request2, nil, 5)
	require.NoError(t, err)
	require.NotNil(t, response2)
	assert.Contains(t, string(response2.GetValue()), "computed-result-for-task-2")

	// Verify both requests were processed
	processMu.Lock()
	assert.Equal(t, 2, requestsProcessed, "Agent2 should have processed 2 requests")
	processMu.Unlock()

	// Verify processing events
	time.Sleep(100 * time.Millisecond) // Let events propagate
	eventsMu.Lock()
	assert.GreaterOrEqual(t, len(processingEvents), 2, "Should have at least 2 processing events")
	eventsMu.Unlock()

	// Verify results stored in Shared Memory
	result1Resp, err := sharedMem.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "result-task-1",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	require.True(t, result1Resp.Found)
	assert.Contains(t, string(result1Resp.Value.Value), "computed-result-for-task-1")

	result2Resp, err := sharedMem.Get(ctx, &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
		Key:       "result-task-2",
		AgentId:   "agent1",
	})
	require.NoError(t, err)
	require.True(t, result2Resp.Found)
	assert.Contains(t, string(result2Resp.Value.Value), "computed-result-for-task-2")

	// Verify shared memory stats
	statsResp, err := sharedMem.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(2), statsResp.KeyCount, "Should have 2 results stored")

	cancel()
	<-agent2Done
}

// TestTriModalIntegration_ConcurrentWorkflows tests multiple workflows
// running concurrently using all three communication modes
func TestTriModalIntegration_ConcurrentWorkflows(t *testing.T) {
	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Setup
	bus := NewMessageBus(nil, nil, nil, logger)
	defer bus.Close()

	queue, err := NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	defer queue.Close()

	sharedMem, err := NewSharedMemoryStore(nil, logger)
	require.NoError(t, err)
	defer sharedMem.Close()

	// Run 5 concurrent workflows
	numWorkflows := 5
	var wg sync.WaitGroup
	wg.Add(numWorkflows)

	for i := 0; i < numWorkflows; i++ {
		workflowID := fmt.Sprintf("workflow-%d", i)

		go func(wfID string) {
			defer wg.Done()

			// Each workflow: store state, send message, broadcast notification

			// 1. Store workflow state
			_, err := sharedMem.Put(ctx, &loomv1.PutSharedMemoryRequest{
				Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
				Key:       wfID,
				Value:     []byte(fmt.Sprintf(`{"workflow":"%s","status":"running"}`, wfID)),
				AgentId:   "coordinator",
			})
			if err != nil {
				t.Errorf("Failed to store state for %s: %v", wfID, err)
				return
			}

			// 2. Send task message
			taskMsg := &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(fmt.Sprintf(`{"workflow":"%s","task":"process"}`, wfID)),
				},
			}
			_, err = queue.Send(ctx, "coordinator", fmt.Sprintf("worker-%s", wfID), "task", taskMsg, nil)
			if err != nil {
				t.Errorf("Failed to send task for %s: %v", wfID, err)
				return
			}

			// 3. Broadcast notification
			_, _, err = bus.Publish(ctx, fmt.Sprintf("workflow.%s", wfID), &loomv1.BusMessage{
				Id:        fmt.Sprintf("notif-%s", wfID),
				Topic:     fmt.Sprintf("workflow.%s", wfID),
				FromAgent: "coordinator",
				Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte(wfID)}},
				Timestamp: time.Now().UnixMilli(),
			})
			if err != nil {
				t.Errorf("Failed to publish notification for %s: %v", wfID, err)
			}
		}(workflowID)
	}

	wg.Wait()

	// Verify all workflows stored state
	statsResp, err := sharedMem.GetStats(ctx, loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL)
	require.NoError(t, err)
	assert.Equal(t, int64(numWorkflows), statsResp.KeyCount, "Should have stored state for all workflows")

	// Verify all tasks sent
	assert.Equal(t, int64(numWorkflows), queue.totalEnqueued.Load(), "Should have enqueued tasks for all workflows")

	// Verify bus published all notifications (check total_published counter)
	// Note: MessageBus doesn't have a GetStats() method, but we verified no errors
}
