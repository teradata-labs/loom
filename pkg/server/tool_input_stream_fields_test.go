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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestApplyToolInputStreamFields_CarriesCallIdentityAndSize(t *testing.T) {
	progress := &loomv1.WeaveProgress{ToolName: "files"}

	applyToolInputStreamFields(progress, agent.ProgressEvent{
		IsToolInputStream: true,
		ToolName:          "files",
		ToolCallID:        "call_1",
		ToolInputBytes:    2048,
		ToolCallIndex:     2,
	})

	assert.True(t, progress.IsToolInputStream)
	assert.Equal(t, "call_1", progress.ToolCallId)
	assert.Equal(t, int64(2048), progress.ToolInputBytes)
	assert.Equal(t, int32(2), progress.ToolCallIndex)
	assert.Equal(t, "files", progress.ToolName)
}

func TestApplyToolInputStreamFields_IgnoresOtherEvents(t *testing.T) {
	progress := &loomv1.WeaveProgress{}

	applyToolInputStreamFields(progress, agent.ProgressEvent{
		IsTokenStream: true,
		ToolCallID:    "call_1",
	})

	assert.False(t, progress.IsToolInputStream)
	assert.Empty(t, progress.ToolCallId)
	assert.Zero(t, progress.ToolInputBytes)
}

// toolInputProgressLLM reports a tool call's progress while it streams, the way
// a provider that knows the call's identity does.
type toolInputProgressLLM struct{}

func (toolInputProgressLLM) Name() string  { return "tool-input-progress" }
func (toolInputProgressLLM) Model() string { return "test-model" }

func (toolInputProgressLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
}

func (toolInputProgressLLM) ChatStream(ctx context.Context, _ []types.Message, _ []shuttle.Tool, _ types.TokenCallback) (*types.LLMResponse, error) {
	types.NotifyToolInputProgress(ctx, types.ToolInputProgress{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 2048})
	types.NotifyStreamActivity(ctx)
	return &types.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
}

// The server must carry a tool-input stream event's id and size to the client;
// removing the mapping call from the stream loop fails this.
func TestServer_StreamWeave_ToolInputStreamEventCarriesIdAndBytes(t *testing.T) {
	srv := NewServer(agent.NewAgent(&mockBackend{}, toolInputProgressLLM{}), nil)
	stream := &mockStreamWeaveServer{ctx: context.Background()}

	err := srv.StreamWeave(&loomv1.WeaveRequest{Query: "write it", SessionId: "sess_tool_input"}, stream)
	require.NoError(t, err)

	stream.mu.Lock()
	defer stream.mu.Unlock()
	var got *loomv1.WeaveProgress
	for _, msg := range stream.messages {
		if msg.IsToolInputStream {
			got = msg
			break
		}
	}
	require.NotNil(t, got, "expected an is_tool_input_stream event")
	assert.Equal(t, "call_1", got.ToolCallId)
	assert.Equal(t, "files", got.ToolName)
	assert.Equal(t, int64(2048), got.ToolInputBytes)
	assert.False(t, got.IsToolStarted, "a streaming call must not look like a started tool")
}
