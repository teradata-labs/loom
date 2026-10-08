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

// TestClient_ChatStream_ToolCallDeltasReportProgress pins that each tool_calls
// delta reports the call's id, name and argument bytes so far, including when
// the id and name arrive after the first argument fragment.
func TestClient_ChatStream_ToolCallDeltasReportProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"files","arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"a.ipynb\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"read_notebook","arguments":"}"}}]}}]}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	_, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 8},
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 19},
		{Index: 1, ToolCallID: "", ToolName: "", Bytes: 1},
		{Index: 1, ToolCallID: "call_2", ToolName: "read_notebook", Bytes: 2},
	}, got)
}
