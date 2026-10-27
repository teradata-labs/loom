// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// TestClient_ChatStream_ToolCallsReportProgress pins that each complete tool
// call Ollama delivers in a chunk reports the call's index, id, name and full
// argument size, alongside the existing stream-activity pulse.
func TestClient_ChatStream_ToolCallsReportProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		lines := []string{
			`{"model":"llama3.1","message":{"role":"assistant","content":"Reading it."}}`,
			`{"model":"llama3.1","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"files","arguments":{"path":"a.ipynb"}}}]}}`,
			`{"model":"llama3.1","message":{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"read_notebook","arguments":{"id":42}}}]}}`,
			`{"model":"llama3.1","message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":10,"eval_count":5}`,
		}
		for _, line := range lines {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "llama3.1"})

	var got []types.ToolInputProgress
	ctx := types.WithToolInputProgress(context.Background(), func(p types.ToolInputProgress) { got = append(got, p) })

	resp, err := client.ChatStream(ctx, []types.Message{{Role: "user", Content: "go"}}, nil, nil)
	require.NoError(t, err)

	// json.Marshal of the parsed args map: {"path":"a.ipynb"} = 18 bytes,
	// {"id":42} = 9 bytes.
	assert.Equal(t, []types.ToolInputProgress{
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 18},
		{Index: 1, ToolCallID: "call_2", ToolName: "read_notebook", Bytes: 9},
	}, got)

	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "a.ipynb", resp.ToolCalls[0].Input["path"])
}
