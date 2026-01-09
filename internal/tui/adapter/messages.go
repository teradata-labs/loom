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
package adapter

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// MessageAdapter wraps the gRPC client for message operations.
// Implements message.Service interface.
type MessageAdapter struct {
	client *client.Client
}

// NewMessageAdapter creates a new message adapter.
func NewMessageAdapter(c *client.Client) *MessageAdapter {
	return &MessageAdapter{client: c}
}

// List returns all messages for a session.
func (m *MessageAdapter) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	messages, err := m.client.GetConversationHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	result := make([]message.Message, len(messages))
	for i, msg := range messages {
		result[i] = ProtoToMessage(msg, sessionID)
	}
	return result, nil
}

// Subscribe subscribes to message events.
// Note: Real-time message updates come through WeaveProgress streaming,
// not a separate subscription. This is handled by the CoordinatorAdapter.
func (m *MessageAdapter) Subscribe(ctx context.Context) <-chan pubsub.Event[message.Message] {
	ch := make(chan pubsub.Event[message.Message])
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// ProtoToMessage converts a proto Message to internal message.Message type.
func ProtoToMessage(m *loomv1.Message, sessionID string) message.Message {
	role := message.Role(m.Role)
	msg := message.NewMessage(m.Id, sessionID, role)

	// Add text content
	if m.Content != "" {
		msg.AddPart(message.ContentText{Text: m.Content})
	}

	// Add tool calls
	for i, tc := range m.ToolCalls {
		// Generate a tool call ID from message ID and index since proto doesn't have one
		toolCallID := fmt.Sprintf("%s-tc-%d", m.Id, i)

		msg.AddPart(message.ToolCall{
			ID:        toolCallID,
			Name:      tc.Name,
			Arguments: tc.ArgsJson,
			Input:     tc.ArgsJson,
			Finished:  tc.ResultJson != "",
		})

		// If there's a result, add it as a tool result
		if tc.ResultJson != "" {
			msg.AddPart(message.ToolResult{
				ToolCallID: toolCallID,
				Content:    tc.ResultJson,
				IsError:    false, // Could parse result to detect errors
			})
		}
	}

	return msg
}

// ProtoToToolCall converts a proto ToolCall to message.ToolCall.
// Note: ToolCall proto doesn't have an Id field, so we use the name as the ID.
func ProtoToToolCall(tc *loomv1.ToolCall) message.ToolCall {
	return message.ToolCall{
		ID:        tc.Name, // Use name as ID since proto doesn't have an Id field
		Name:      tc.Name,
		Arguments: tc.ArgsJson,
		Input:     tc.ArgsJson,
		Finished:  tc.ResultJson != "",
	}
}
