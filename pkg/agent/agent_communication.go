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
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/storage"
)

// boundedPayloadValue marshals data for an inter-agent message and bounds the
// result at threshold bytes (D3: inter-agent messages carry content inline,
// bounded by the same 16384 write rule as any stored row; a ref crossing
// agents is a ref crossing turns, which §4.3 forbids).
//
// Within the bound the marshaled bytes ride exactly. Over it, the payload is
// REPLACED by a small self-describing JSON document — never the marshaled
// bytes cut mid-structure, which is not JSON and which no receiver can
// unmarshal. The receiver therefore always gets a well-formed document, and an
// over-bound send says so in the payload itself.
// Returns the wire value and the TRUE marshaled size, so the metadata records
// what was produced even when the value carries the bound notice instead.
func boundedPayloadValue(data interface{}, threshold int) ([]byte, int, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal data: %w", err)
	}
	if len(raw) <= threshold {
		return raw, len(raw), nil
	}
	stub, err := json.Marshal(map[string]interface{}{
		"truncated":      true,
		"original_bytes": len(raw),
		"preview":        previewOf(string(raw)),
		"note": fmt.Sprintf("payload exceeded the %d-byte inter-agent bound and was not sent; "+
			"ask the sender for a narrower slice.", threshold),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal bound notice: %w", err)
	}
	return stub, len(raw), nil
}

// Send sends a message to another agent using value or reference semantics.
// The communication policy determines whether to use direct value or reference.
func (a *Agent) Send(ctx context.Context, toAgent string, messageType string, data interface{}) (*loomv1.CommunicationMessage, error) {
	if a.commPolicy == nil {
		return nil, fmt.Errorf("communication policy not configured")
	}

	// Marshal data to JSON, bounded inline (D3). The metadata records the TRUE
	// size, so a receiver can see what the bound elided.
	dataBytes, trueSize, err := boundedPayloadValue(data, int(storage.DefaultSharedMemoryThreshold))
	if err != nil {
		return nil, err
	}

	// Create message payload
	payload := &loomv1.MessagePayload{
		Metadata: &loomv1.PayloadMetadata{
			SizeBytes:   int64(trueSize),
			ContentType: "application/json",
			Compression: "none",
			Encoding:    "none",
		},
	}
	payload.Data = &loomv1.MessagePayload_Value{Value: dataBytes}

	// Get policy for message type
	policy := a.commPolicy.GetPolicy(messageType)

	// Create communication message
	msg := &loomv1.CommunicationMessage{
		Id:        generateMessageID(),
		FromAgent: a.config.Name,
		ToAgent:   toAgent,
		Payload:   payload,
		Policy:    policy,
		Timestamp: time.Now().Unix(),
	}

	return msg, nil
}

// Receive receives and resolves a message from another agent.
// If the message uses reference semantics, the reference is resolved to actual data.
func (a *Agent) Receive(ctx context.Context, msg *loomv1.CommunicationMessage) (interface{}, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil message")
	}

	if msg.Payload == nil {
		return nil, fmt.Errorf("nil payload")
	}

	var dataBytes []byte

	// Extract data based on payload type
	switch data := msg.Payload.Data.(type) {
	case *loomv1.MessagePayload_Value:
		// Direct value
		dataBytes = data.Value

	case *loomv1.MessagePayload_Reference:
		// The ref path is deleted (blueprint D3): inter-agent messages carry
		// content inline. A reference payload has no resolver.
		return nil, fmt.Errorf("reference payloads are no longer supported — inter-agent messages carry content inline")

	default:
		return nil, fmt.Errorf("unknown payload type")
	}

	// Unmarshal data
	var result interface{}
	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return result, nil
}

// generateMessageID generates a unique message identifier
func generateMessageID() string {
	return fmt.Sprintf("msg-%d", time.Now().UnixNano())
}

// SendAsync sends a message asynchronously (fire-and-forget).
// If the destination agent is offline, the message is queued for later delivery.
// Returns immediately without waiting for the message to be delivered.
func (a *Agent) SendAsync(ctx context.Context, toAgent string, messageType string, data interface{}) (string, error) {
	if a.commPolicy == nil {
		return "", fmt.Errorf("communication policy not configured")
	}

	bounded, trueSize, err := boundedPayloadValue(data, int(storage.DefaultSharedMemoryThreshold))
	if err != nil {
		return "", err
	}
	payload := &loomv1.MessagePayload{
		Metadata: &loomv1.PayloadMetadata{
			SizeBytes:   int64(trueSize),
			ContentType: "application/json",
			Compression: "none",
			Encoding:    "none",
		},
		Data: &loomv1.MessagePayload_Value{Value: bounded},
	}

	// Generate message ID
	messageID := generateMessageID()

	// Create queue message
	queueMsg := &communication.QueueMessage{
		ID:          messageID,
		ToAgent:     toAgent,
		FromAgent:   a.config.Name,
		MessageType: messageType,
		Payload:     payload,
		Metadata:    make(map[string]string),
		Priority:    0, // Default priority
		EnqueuedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		MaxRetries:  3,
	}

	// Enqueue message if message queue is configured
	if a.messageQueue != nil {
		if err := a.messageQueue.Enqueue(ctx, queueMsg); err != nil {
			return "", fmt.Errorf("failed to enqueue message: %w", err)
		}
		return messageID, nil
	}

	// Fallback: if no message queue configured, return error
	return "", fmt.Errorf("message queue not configured - cannot send async message")

}

// SendAndReceive sends a message and waits for a response (RPC-style).
// Blocks until response is received or timeout occurs.
func (a *Agent) SendAndReceive(ctx context.Context, toAgent string, messageType string, data interface{}, timeout time.Duration) (interface{}, error) {
	if a.commPolicy == nil {
		return nil, fmt.Errorf("communication policy not configured")
	}

	// Create context with timeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Marshal data to JSON, bounded inline (D3). The metadata records the TRUE
	// size, so a receiver can see what the bound elided.
	dataBytes, trueSize, err := boundedPayloadValue(data, int(storage.DefaultSharedMemoryThreshold))
	if err != nil {
		return nil, err
	}

	// Create message payload
	payload := &loomv1.MessagePayload{
		Metadata: &loomv1.PayloadMetadata{
			SizeBytes:   int64(trueSize),
			ContentType: "application/json",
			Compression: "none",
			Encoding:    "none",
		},
	}
	payload.Data = &loomv1.MessagePayload_Value{Value: dataBytes}

	// Use message queue for request-response if configured
	if a.messageQueue != nil {
		// Convert timeout to seconds for MessageQueue API
		timeoutSeconds := int(timeout.Seconds())
		if timeoutSeconds == 0 {
			timeoutSeconds = 30 // Default 30s
		}

		// Send request and wait for response
		responsePayload, err := a.messageQueue.SendAndReceive(
			ctx,
			a.config.Name, // fromAgent
			toAgent,
			messageType,
			payload,
			make(map[string]string), // metadata
			timeoutSeconds,
		)

		if err != nil {
			return nil, fmt.Errorf("send and receive failed: %w", err)
		}

		// Resolve response payload to actual data
		var responseBytes []byte
		switch data := responsePayload.Data.(type) {
		case *loomv1.MessagePayload_Value:
			responseBytes = data.Value
		case *loomv1.MessagePayload_Reference:
			// The ref path is deleted (blueprint D3).
			return nil, fmt.Errorf("reference payloads are no longer supported — inter-agent messages carry content inline")
		default:
			return nil, fmt.Errorf("unknown response payload type")
		}

		// Unmarshal response data
		var result interface{}
		if err := json.Unmarshal(responseBytes, &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}

		return result, nil
	}

	// Fallback: if no message queue configured, return error
	return nil, fmt.Errorf("message queue not configured - cannot use SendAndReceive")
}

// SendWithAck sends a message and waits for acknowledgment.
// Returns nil if message was successfully delivered and acknowledged.
func (a *Agent) SendWithAck(ctx context.Context, toAgent string, messageType string, data interface{}, timeout time.Duration) error {
	if a.commPolicy == nil {
		return fmt.Errorf("communication policy not configured")
	}

	// Create context with timeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	bounded, trueSize, err := boundedPayloadValue(data, int(storage.DefaultSharedMemoryThreshold))
	if err != nil {
		return err
	}
	payload := &loomv1.MessagePayload{
		Metadata: &loomv1.PayloadMetadata{
			SizeBytes:   int64(trueSize),
			ContentType: "application/json",
			Compression: "none",
			Encoding:    "none",
		},
		Data: &loomv1.MessagePayload_Value{Value: bounded},
	}

	// Generate message ID for tracking
	messageID := generateMessageID()

	// Create queue message
	queueMsg := &communication.QueueMessage{
		ID:          messageID,
		ToAgent:     toAgent,
		FromAgent:   a.config.Name,
		MessageType: messageType,
		Payload:     payload,
		Metadata:    make(map[string]string),
		Priority:    0, // Default priority
		EnqueuedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		MaxRetries:  3,
	}

	// Use message queue for acknowledgment-based delivery if configured
	if a.messageQueue != nil {
		// Enqueue message
		if err := a.messageQueue.Enqueue(ctx, queueMsg); err != nil {
			return fmt.Errorf("failed to enqueue message: %w", err)
		}

		// Wait for acknowledgment by polling the queue
		// The recipient must acknowledge the message via messageQueue.Acknowledge()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		deadline := time.Now().Add(timeout)
		for {
			select {
			case <-ticker.C:
				// Check if message has been acknowledged
				// We need to check the database for the message status
				// For now, we'll use a simpler approach: the message disappears when acked
				depth := a.messageQueue.GetQueueDepth(toAgent)
				_ = depth // Depth check alone isn't sufficient; message might be in-flight

				// In a production system, we'd query the message status from the database
				// For now, if we reach here after timeout, assume failure
				if time.Now().After(deadline) {
					return fmt.Errorf("acknowledgment timeout after %v", timeout)
				}

			case <-ctx.Done():
				return fmt.Errorf("acknowledgment canceled: %w", ctx.Err())
			}
		}
	}

	// Fallback: if no message queue configured, return error
	return fmt.Errorf("message queue not configured - cannot use SendWithAck")
}

// ReceiveWithTimeout receives a message with a timeout.
// Returns nil if no message is available within the timeout period.
func (a *Agent) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (*loomv1.CommunicationMessage, error) {
	// Create context with timeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Use message queue if configured
	if a.messageQueue != nil {
		// Poll for messages with backoff
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Try to dequeue a message
				queueMsg, err := a.messageQueue.Dequeue(ctx, a.config.Name)
				if err != nil {
					return nil, fmt.Errorf("failed to dequeue message: %w", err)
				}

				if queueMsg != nil {
					// Convert QueueMessage to CommunicationMessage
					commMsg := &loomv1.CommunicationMessage{
						Id:        queueMsg.ID,
						FromAgent: queueMsg.FromAgent,
						ToAgent:   queueMsg.ToAgent,
						Payload:   queueMsg.Payload,
						Timestamp: queueMsg.EnqueuedAt.Unix(),
					}
					return commMsg, nil
				}

				// No message available, continue polling

			case <-ctx.Done():
				if ctx.Err() == context.DeadlineExceeded {
					return nil, nil // Timeout, no message available
				}
				return nil, ctx.Err()
			}
		}
	}

	// Fallback: if no message queue configured, just wait for timeout
	<-ctx.Done()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil // Timeout, no message available
	}
	return nil, ctx.Err()
}
