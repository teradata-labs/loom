// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestSDKClient_ChatStream_ToolInputDeltasReportProgress pins that every
// input_json_delta on the Anthropic-SDK streaming path reports the call's
// index, id, name and argument bytes so far.
func TestSDKClient_ChatStream_ToolInputDeltasReportProgress(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"server_read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	c := &SDKClient{
		client: anthropic.NewClient(
			option.WithBaseURL(ts.URL),
			option.WithAPIKey("test-key"),
		),
		modelID:     "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		maxTokens:   1024,
		temperature: 1.0,
	}

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := c.ChatStream(
		ctx,
		[]llmtypes.Message{{Role: "user", Content: "go"}},
		[]shuttle.Tool{&mockTool{name: "server:read"}},
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "tu_1", ToolName: "server:read", Bytes: 8},
		{Index: 0, ToolCallID: "tu_1", ToolName: "server:read", Bytes: 12},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "server:read", resp.ToolCalls[0].Name)
	assert.Equal(t, map[string]interface{}{"path": "x"}, resp.ToolCalls[0].Input)
}

// TestSDKClient_ChatStream_TruncatedToolCallLeavesNoStrayInputKeys pins that a
// tool_use block cut off mid-argument (stop_reason=max_tokens) yields an
// empty Input map — the buffered JSON never parses, so nothing partial may
// leak into the returned tool call.
func TestSDKClient_ChatStream_TruncatedToolCallLeavesNoStrayInputKeys(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_cut","name":"server_read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\",\"content\":\"hello wor"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":64}}

event: message_stop
data: {"type":"message_stop"}

`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	c := &SDKClient{
		client: anthropic.NewClient(
			option.WithBaseURL(ts.URL),
			option.WithAPIKey("test-key"),
		),
		modelID:     "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		maxTokens:   1024,
		temperature: 1.0,
	}

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := c.ChatStream(
		ctx,
		[]llmtypes.Message{{Role: "user", Content: "go"}},
		[]shuttle.Tool{&mockTool{name: "server:read"}},
		nil,
	)
	require.NoError(t, err)

	// The delta still reports progress even though the call never completes.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "tu_cut", ToolName: "server:read", Bytes: 36},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "tu_cut", resp.ToolCalls[0].ID)
	assert.Equal(t, "server:read", resp.ToolCalls[0].Name)
	assert.Empty(t, resp.ToolCalls[0].Input, "truncated call must not leak partial argument keys")
}

// TestSDKClient_ChatStream_EmptyPartialJSONDeltaIsSkipped pins that an
// input_json_delta with an empty partial_json neither grows the buffer nor
// emits a progress event.
func TestSDKClient_ChatStream_EmptyPartialJSONDeltaIsSkipped(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_gap","name":"server_read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	c := &SDKClient{
		client: anthropic.NewClient(
			option.WithBaseURL(ts.URL),
			option.WithAPIKey("test-key"),
		),
		modelID:     "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		maxTokens:   1024,
		temperature: 1.0,
	}

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := c.ChatStream(
		ctx,
		[]llmtypes.Message{{Role: "user", Content: "go"}},
		[]shuttle.Tool{&mockTool{name: "server:read"}},
		nil,
	)
	require.NoError(t, err)

	// Only the non-empty delta produces an event, with the buffer's real size.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "tu_gap", ToolName: "server:read", Bytes: 12},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, map[string]interface{}{"path": "x"}, resp.ToolCalls[0].Input)
}
