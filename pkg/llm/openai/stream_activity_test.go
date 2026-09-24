// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// TestClient_ChatStream_ToolCallDeltasNotifyStreamActivity pins that each
// chunk carrying tool_calls deltas — which never reach the token callback —
// reports stream activity.
func TestClient_ChatStream_ToolCallDeltasNotifyStreamActivity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Building it."}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"create_ui_app","arguments":"{\"html\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"<div>"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"</div>\"}"}}]},"finish_reason":"tool_calls"}]}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	activity := 0
	ctx := types.WithStreamActivity(context.Background(), func() { activity++ })
	var tokens []string

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "build"}}, nil, func(tok string) { tokens = append(tokens, tok) })
	require.NoError(t, err)

	assert.Equal(t, 3, activity, "one activity notification per tool_calls chunk")
	assert.Equal(t, []string{"Building it."}, tokens)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "<div></div>", resp.ToolCalls[0].Input["html"])
	assert.Equal(t, "tool_use", resp.StopReason)
}
