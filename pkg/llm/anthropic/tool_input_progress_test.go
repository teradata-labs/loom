// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// TestClient_ChatStream_ToolInputDeltasReportProgress pins that every
// input_json_delta reports the call's index, id, name and argument bytes so
// far, alongside the existing stream-activity pulse.
func TestClient_ChatStream_ToolInputDeltasReportProgress(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"files"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" \"a.ipynb\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_def","name":"read_notebook"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"id\":42}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "toolu_abc", ToolName: "files", Bytes: 8},
		{Index: 0, ToolCallID: "toolu_abc", ToolName: "files", Bytes: 19},
		{Index: 1, ToolCallID: "toolu_def", ToolName: "read_notebook", Bytes: 9},
	}, got)

	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "a.ipynb", resp.ToolCalls[0].Input["path"])
}

// TestClient_ChatStream_ToolInputDeltasReportProgress_IdentityBeforeArgs pins
// that a tool_use block whose id and name arrive in content_block_start (the
// normal Anthropic shape) reports them on the very first delta.
func TestClient_ChatStream_ToolInputDeltasReportProgress_IdentityBeforeArgs(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_xyz","name":"server_read"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "toolu_xyz", ToolName: "server_read", Bytes: 12},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, map[string]interface{}{"path": "x"}, resp.ToolCalls[0].Input)
}

// TestClient_ChatStream_TruncatedToolCallLeavesNoStrayInputKeys pins that a
// tool_use block cut off mid-argument (stop_reason=max_tokens) yields an
// empty Input map — the buffered JSON never parses, so nothing partial may
// leak into the returned tool call.
func TestClient_ChatStream_TruncatedToolCallLeavesNoStrayInputKeys(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_cut","name":"files"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\",\"content\":\"hello wor"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":64}}

event: message_stop
data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	// The delta still reports progress even though the call never completes.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "toolu_cut", ToolName: "files", Bytes: 36},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "toolu_cut", resp.ToolCalls[0].ID)
	assert.Equal(t, "files", resp.ToolCalls[0].Name)
	assert.Empty(t, resp.ToolCalls[0].Input, "truncated call must not leak partial argument keys")
}

// TestClient_ChatStream_EmptyPartialJSONDeltaIsSkipped pins that an
// input_json_delta with an empty partial_json neither grows the buffer nor
// emits a progress event.
func TestClient_ChatStream_EmptyPartialJSONDeltaIsSkipped(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_gap","name":"files"}}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	// Only the non-empty delta produces an event, with the buffer's real size.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "toolu_gap", ToolName: "files", Bytes: 12},
	}, got)

	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, map[string]interface{}{"path": "x"}, resp.ToolCalls[0].Input)
}
