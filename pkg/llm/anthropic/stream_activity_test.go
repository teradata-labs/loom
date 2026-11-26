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

// TestClient_ChatStream_ToolInputDeltasNotifyStreamActivity pins that every
// input_json_delta — which never reaches the token callback — reports stream
// activity, while text deltas do not double-count.
func TestClient_ChatStream_ToolInputDeltasNotifyStreamActivity(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Building it."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"create_ui_app"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"html\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"<div>"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"</div>\"}"}}

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

	activity := 0
	ctx := types.WithStreamActivity(context.Background(), func() { activity++ })
	var tokens []string

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "build"}}, nil, func(tok string) { tokens = append(tokens, tok) })
	require.NoError(t, err)

	assert.Equal(t, 3, activity, "one activity notification per input_json_delta")
	assert.Equal(t, []string{"Building it."}, tokens, "text deltas still flow through the token callback only")
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "<div></div>", resp.ToolCalls[0].Input["html"])
}
