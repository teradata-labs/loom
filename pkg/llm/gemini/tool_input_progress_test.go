// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package gemini

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

// TestClient_ChatStream_FunctionCallsReportProgress pins that each complete
// function call Gemini delivers in a part reports the call's index, id, name
// and full argument size, alongside the existing stream-activity pulse.
func TestClient_ChatStream_FunctionCallsReportProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Reading it."}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"files","args":{"path":"a.ipynb"}}}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_notebook","args":{"id":42}}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-flash-preview"})
	client.httpClient.Transport = &mockTransport{baseURL: server.URL}

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	// json.Marshal of the args map: {"path":"a.ipynb"} = 18 bytes,
	// {"id":42} = 9 bytes.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "files", ToolName: "files", Bytes: 18},
		{Index: 1, ToolCallID: "read_notebook", ToolName: "read_notebook", Bytes: 9},
	}, got)

	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "a.ipynb", resp.ToolCalls[0].Input["path"])
}

// TestClient_ChatStream_ParallelCallsToSameToolGetDistinctIndices pins the
// motivating case for tool_call_index: Gemini has no call IDs, so two parallel
// calls to the same tool repeat the function name in ToolCallID. The progress
// events must still carry distinct indices (0 and 1) so clients can tell the
// calls apart.
func TestClient_ChatStream_ParallelCallsToSameToolGetDistinctIndices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"files","args":{"path":"a.ipynb"}}}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"files","args":{"path":"b.ipynb"}}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-flash-preview"})
	client.httpClient.Transport = &mockTransport{baseURL: server.URL}

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	// json.Marshal of the args map: {"path":"a.ipynb"} = 18 bytes,
	// {"path":"b.ipynb"} = 18 bytes.
	require.Len(t, got, 2)
	assert.Equal(t, 0, got[0].Index)
	assert.Equal(t, 1, got[1].Index)
	assert.Equal(t, "files", got[0].ToolCallID, "Gemini repeats the function name as the call ID")
	assert.Equal(t, got[0].ToolCallID, got[1].ToolCallID, "both calls share one ID — only the index tells them apart")
	assert.Equal(t, 18, got[0].Bytes)
	assert.Equal(t, 18, got[1].Bytes)

	// The final tool-call list preserves the delivery order, so index N
	// corresponds to resp.ToolCalls[N] on Gemini.
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "a.ipynb", resp.ToolCalls[0].Input["path"])
	assert.Equal(t, "b.ipynb", resp.ToolCalls[1].Input["path"])
}
