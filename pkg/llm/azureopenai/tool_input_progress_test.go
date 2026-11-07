// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package azureopenai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestClient_ChatStream_ToolCallDeltasReportProgress pins that each tool_calls
// delta reports the call's id, name and argument bytes so far, including when
// the id and name arrive after the first argument fragment.
func TestClient_ChatStream_ToolCallDeltasReportProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/openai/deployments/gpt-4o/")
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

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o",
		APIKey:       "test-key",
		APIVersion:   "2024-10-21",
	})
	require.NoError(t, err)

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 8},
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 19},
		{Index: 1, ToolCallID: "", ToolName: "", Bytes: 1},
		{Index: 1, ToolCallID: "call_2", ToolName: "read_notebook", Bytes: 2},
	}, got)

	require.Len(t, resp.ToolCalls, 2)
	// The client assembles ToolCalls by ranging over a map, so their order is
	// not deterministic — look them up by ID instead of by position.
	byID := make(map[string]llmtypes.ToolCall, len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		byID[tc.ID] = tc
	}
	first, ok := byID["call_1"]
	require.True(t, ok, "call_1 missing from response")
	assert.Equal(t, "files", first.Name)
	assert.Equal(t, "a.ipynb", first.Input["path"])
	// call_2 sent its arguments before its id and name; both must be backfilled.
	// Its arguments are "{}", which parses to an empty (but non-nil) Input.
	second, ok := byID["call_2"]
	require.True(t, ok, "call_2 missing from response")
	assert.Equal(t, "read_notebook", second.Name)
	assert.NotNil(t, second.Input)
	assert.NotContains(t, second.Input, "_args")
	assert.NotContains(t, second.Input, "_raw")
}
