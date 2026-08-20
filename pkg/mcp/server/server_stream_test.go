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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// captureSSE records every event written, implementing transport.SSEWriter.
type captureSSE struct {
	events [][]byte
}

func (c *captureSSE) WriteEvent(data []byte) error {
	c.events = append(c.events, append([]byte(nil), data...))
	return nil
}

// fakeStreamingProvider implements both ToolProvider and StreamingToolProvider.
type fakeStreamingProvider struct {
	streaming     map[string]bool
	progressSteps []float64
	finalText     string
	streamErr     error
	callToolUsed  bool
	streamUsed    bool
}

func (p *fakeStreamingProvider) ListTools(context.Context) ([]protocol.Tool, error) { return nil, nil }

func (p *fakeStreamingProvider) CallTool(_ context.Context, name string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	p.callToolUsed = true
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: "sync:" + name}}}, nil
}

func (p *fakeStreamingProvider) SupportsStreaming(name string) bool { return p.streaming[name] }

func (p *fakeStreamingProvider) CallToolStream(_ context.Context, _ string, _ map[string]interface{}, _ string, emit ProgressEmitter) (*protocol.CallToolResult, error) {
	p.streamUsed = true
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	for _, s := range p.progressSteps {
		_ = emit.EmitProgress(s, 100)
	}
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: p.finalText}}}, nil
}

func newStreamTestServer(t *testing.T, p ToolProvider) *MCPServer {
	t.Helper()
	return NewMCPServer("test", "0.0.0", nil, WithToolProvider(p))
}

func TestHandleMessageStream_StreamsProgressThenResult(t *testing.T) {
	p := &fakeStreamingProvider{
		streaming:     map[string]bool{"loom_weave": true},
		progressSteps: []float64{50, 100},
		finalText:     "the answer",
	}
	srv := newStreamTestServer(t, p)
	w := &captureSSE{}

	msg := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"loom_weave","arguments":{"query":"hi"},"_meta":{"progressToken":"tok"}}}`)
	final, err := srv.HandleMessageStream(context.Background(), msg, w)
	require.NoError(t, err)

	assert.True(t, p.streamUsed, "streaming path should be used")
	assert.False(t, p.callToolUsed, "synchronous CallTool should not be used")

	require.Len(t, w.events, 2, "expected one progress event per step")
	for i, raw := range w.events {
		var notif struct {
			Method string `json:"method"`
			Params struct {
				ProgressToken string  `json:"progressToken"`
				Progress      float64 `json:"progress"`
				Total         float64 `json:"total"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal(raw, &notif))
		assert.Equal(t, "notifications/progress", notif.Method)
		assert.Equal(t, "tok", notif.Params.ProgressToken)
		assert.Equal(t, p.progressSteps[i], notif.Params.Progress)
		assert.Equal(t, float64(100), notif.Params.Total)
	}

	var resp struct {
		ID     int64 `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(final, &resp))
	assert.Equal(t, int64(7), resp.ID)
	require.Len(t, resp.Result.Content, 1)
	assert.Equal(t, "the answer", resp.Result.Content[0].Text)
}

func TestHandleMessageStream_NoProgressTokenSuppressesProgress(t *testing.T) {
	p := &fakeStreamingProvider{
		streaming:     map[string]bool{"loom_weave": true},
		progressSteps: []float64{50, 100},
		finalText:     "done",
	}
	srv := newStreamTestServer(t, p)
	w := &captureSSE{}

	// No _meta.progressToken: progress is opt-in, so no progress events emitted.
	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"loom_weave","arguments":{}}}`)
	final, err := srv.HandleMessageStream(context.Background(), msg, w)
	require.NoError(t, err)

	assert.True(t, p.streamUsed)
	assert.Empty(t, w.events, "no progress events without a progress token")
	assert.Contains(t, string(final), "done")
}

func TestHandleMessageStream_NonStreamableToolFallsBack(t *testing.T) {
	p := &fakeStreamingProvider{
		streaming: map[string]bool{"loom_weave": true}, // "echo" is NOT streamable
		finalText: "unused",
	}
	srv := newStreamTestServer(t, p)
	w := &captureSSE{}

	msg := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	final, err := srv.HandleMessageStream(context.Background(), msg, w)
	require.NoError(t, err)

	assert.True(t, p.callToolUsed, "non-streamable tool should use synchronous CallTool")
	assert.False(t, p.streamUsed)
	assert.Empty(t, w.events)
	assert.Contains(t, string(final), "sync:echo")
}

func TestHandleMessageStream_NonToolCallFallsBack(t *testing.T) {
	p := &fakeStreamingProvider{streaming: map[string]bool{"loom_weave": true}}
	srv := newStreamTestServer(t, p)
	w := &captureSSE{}

	// A method with no registered handler should yield a JSON-RPC MethodNotFound,
	// matching HandleMessage's behavior (proves the fallback path is taken).
	msg := []byte(`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	final, err := srv.HandleMessageStream(context.Background(), msg, w)
	require.NoError(t, err)

	assert.Empty(t, w.events)
	assert.Contains(t, string(final), "method not found")
}

// Round-2 finding 3: a legacy client hitting an MRTR pause on the streaming
// path must get the same explicit -32600 the synchronous path returns — not
// an isError result carrying a raw internal error string.
func TestHandleMessageStream_LegacyMRTRPauseIsExplicitError(t *testing.T) {
	p := &fakeStreamingProvider{
		streaming: map[string]bool{"loom_weave": true},
		streamErr: &protocol.InputRequiredError{
			Requests: protocol.InputRequests{
				"q1": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"which db?"}`)},
			},
			RequestState: "sealed",
		},
	}
	srv := newStreamTestServer(t, p)
	w := &captureSSE{}

	// No _meta.protocolVersion: a legacy-era request.
	msg := []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"loom_weave","arguments":{}}}`)
	final, err := srv.HandleMessageStream(context.Background(), msg, w)
	require.NoError(t, err)

	var resp struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(final, &resp))
	assert.Equal(t, int64(9), resp.ID)
	assert.Nil(t, resp.Result, "legacy MRTR pause must not be downgraded to a result")
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidRequest, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "MRTR-capable")
}

// Round-2 finding 4: a retry whose inputResponses/requestState members are
// present but malformed must fail fast as InvalidParams on both dispatch
// paths — never parse as a zero RetryInput that silently re-elicits.
func TestMalformedRetryInputIsInvalidParams(t *testing.T) {
	srv := newStreamTestServer(t, &fakeStreamingProvider{streaming: map[string]bool{"loom_weave": true}})

	badRetry := `{"name":"loom_weave","arguments":{},"inputResponses":42,` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`

	// Synchronous path.
	out, err := srv.HandleMessage(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+badRetry+`}`))
	require.NoError(t, err)
	var resp struct {
		ID    int64 `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "inputResponses")
	assert.Equal(t, int64(1), resp.ID)

	// Streaming path: rejected before the tool runs.
	p2 := &fakeStreamingProvider{streaming: map[string]bool{"loom_weave": true}}
	srv2 := newStreamTestServer(t, p2)
	out, err = srv2.HandleMessageStream(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+badRetry+`}`), &captureSSE{})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidParams, resp.Error.Code)
	assert.False(t, p2.streamUsed, "malformed retry must be rejected before the handler runs")

	// A well-formed retry still dispatches.
	goodRetry := `{"name":"loom_weave","arguments":{},"inputResponses":{"q1":"postgres"},"requestState":"sealed",` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`
	out, err = srv.HandleMessage(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+goodRetry+`}`))
	require.NoError(t, err)
	var ok struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out, &ok))
	assert.NotNil(t, ok.Result)
}

// Round-2 finding 6: ValidateRequest failures happen after unmarshal, so the
// request id is detectable and JSON-RPC 2.0 requires echoing it.
func TestValidateRequestErrorEchoesID(t *testing.T) {
	srv := newStreamTestServer(t, &fakeStreamingProvider{})
	out, err := srv.HandleMessage(context.Background(),
		[]byte(`{"jsonrpc":"1.0","id":7,"method":"ping"}`))
	require.NoError(t, err)
	var resp struct {
		ID    json.RawMessage `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidRequest, resp.Error.Code)
	assert.JSONEq(t, `7`, string(resp.ID), "error must correlate to the request id, not null")
}
