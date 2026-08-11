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
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// sdkTestServer captures whether each request asked for streaming and serves
// a canned "ok" completion over the matching wire format (SSE vs JSON).
type sdkTestServer struct {
	mu          sync.Mutex
	streamFlags []bool
}

func (s *sdkTestServer) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]interface{}
	_ = json.Unmarshal(body, &req)
	stream, _ := req["stream"].(bool)

	s.mu.Lock()
	s.streamFlags = append(s.streamFlags, stream)
	s.mu.Unlock()

	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant",` +
			`"content":[{"type":"text","text":"ok"}],"model":"test-model",` +
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range []struct{ event, data string }{
		{"message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"test-model","usage":{"input_tokens":10,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	} {
		_, _ = w.Write([]byte("event: " + ev.event + "\ndata: " + ev.data + "\n\n"))
	}
}

func (s *sdkTestServer) requests() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.streamFlags...)
}

// TestSDKClient_Chat_RoutesByMaxTokens verifies the non-streaming pre-flight
// routing in Chat: catalog-sized max_tokens must transparently go over the
// streaming API (the SDK would reject non-streaming Messages.New client-side),
// while small max_tokens keeps the plain non-streaming call.
func TestSDKClient_Chat_RoutesByMaxTokens(t *testing.T) {
	messages := []llmtypes.Message{{Role: "user", Content: "Reply with exactly: ok"}}

	tests := []struct {
		name       string
		maxTokens  int64
		wantStream bool
	}{
		{
			name:       "catalog-sized max_tokens falls back to streaming",
			maxTokens:  64000,
			wantStream: true,
		},
		{
			name:       "small max_tokens stays non-streaming",
			maxTokens:  4096,
			wantStream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &sdkTestServer{}
			ts := httptest.NewServer(http.HandlerFunc(srv.handler))
			defer ts.Close()

			client := &SDKClient{
				client: anthropic.NewClient(
					option.WithBaseURL(ts.URL),
					option.WithAPIKey("test-key"),
				),
				modelID:     "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
				maxTokens:   tt.maxTokens,
				temperature: 1.0,
			}

			resp, err := client.Chat(context.Background(), messages, nil)
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, "ok", resp.Content)
			assert.Equal(t, "end_turn", resp.StopReason)
			assert.Equal(t, 10, resp.Usage.InputTokens)
			assert.Equal(t, 2, resp.Usage.OutputTokens)

			flags := srv.requests()
			require.Len(t, flags, 1, "expected exactly one upstream request")
			assert.Equal(t, tt.wantStream, flags[0],
				"wire stream flag for max_tokens=%d", tt.maxTokens)
			if tt.wantStream {
				assert.Equal(t, true, resp.Metadata["streaming"])
			} else {
				assert.Nil(t, resp.Metadata["streaming"])
			}
		})
	}
}

// TestConvertMessagesToSDK_SystemBlocksCarryTheirOwnMarker pins the §5.2 step 8
// contract on the Bedrock path: each system message becomes its own block and
// carries cache_control only when compile marked it. Merging ROM and the
// summary into one block would put both behind a single breakpoint, so a fold —
// which rewrites only the summary — would invalidate ROM's cached prefix too.
func TestConvertMessagesToSDK_SystemBlocksCarryTheirOwnMarker(t *testing.T) {
	c := &SDKClient{modelID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", maxTokens: 1024}

	systemBlocks, sdkMessages := c.convertMessagesToSDK([]llmtypes.Message{
		{Role: "system", Content: "ROM operating guide", CacheBreakpoint: true},
		{Role: "system", Content: "covers msg:1-5 …summary…", CacheBreakpoint: true},
		{Role: "system", Content: "transient soft reminder"},
		{Role: "user", Content: "hi"},
	})

	require.Len(t, systemBlocks, 3, "one block per system message, never merged")
	require.Len(t, sdkMessages, 1, "the user turn is the only wire message")

	require.Equal(t, "ROM operating guide", systemBlocks[0].Text)
	require.Equal(t, "covers msg:1-5 …summary…", systemBlocks[1].Text)
	require.Equal(t, "transient soft reminder", systemBlocks[2].Text)

	for i, wantMarked := range []bool{true, true, false} {
		marked := systemBlocks[i].CacheControl.Type != ""
		require.Equal(t, wantMarked, marked,
			"system block %d cache_control present=%v, want %v", i, marked, wantMarked)
	}
}

// toolUseMessage builds an SDK message containing a single tool_use block
// with the given (sanitized) tool name, as it would arrive off the wire.
func toolUseMessage(t *testing.T, toolName string) *anthropic.Message {
	t.Helper()
	raw := `{"id":"msg_test","type":"message","role":"assistant",` +
		`"content":[{"type":"tool_use","id":"tu_1","name":"` + toolName + `","input":{"path":"x"}}],` +
		`"model":"test-model","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`
	var msg anthropic.Message
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))
	return &msg
}

// TestSDKClient_ToolNameMap_RequestLocal pins the fix for the shared
// tool-name-map defect: each request's sanitized→original mapping must survive
// another request's conversion. Request A registers "server:read" (sanitized
// "server_read"); request B then registers a colliding literal "server_read".
// Converting A's response with A's mapping must still restore "server:read".
// Before the fix the mapping lived on the client and B's conversion replaced
// it, so A's response came back with the sanitized name.
func TestSDKClient_ToolNameMap_RequestLocal(t *testing.T) {
	c := &SDKClient{modelID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", maxTokens: 1024}

	// Request A: tool with a colon-namespaced name.
	_, mapA := c.convertToolsToSDK([]shuttle.Tool{&mockTool{name: "server:read"}})
	require.Equal(t, "server:read", mapA["server_read"])

	// Request B converts before A's response arrives, with a colliding name.
	_, mapB := c.convertToolsToSDK([]shuttle.Tool{&mockTool{name: "server_read"}})
	require.Equal(t, "server_read", mapB["server_read"])

	msg := toolUseMessage(t, "server_read")

	// A's response, converted with A's mapping, restores A's original name.
	respA := c.convertResponseFromSDK(msg, mapA)
	require.Len(t, respA.ToolCalls, 1)
	assert.Equal(t, "server:read", respA.ToolCalls[0].Name)

	// B's response keeps B's literal name.
	respB := c.convertResponseFromSDK(msg, mapB)
	require.Len(t, respB.ToolCalls, 1)
	assert.Equal(t, "server_read", respB.ToolCalls[0].Name)

	// A nil mapping (request without tools) falls back to the sanitized name.
	respNil := c.convertResponseFromSDK(msg, nil)
	require.Len(t, respNil.ToolCalls, 1)
	assert.Equal(t, "server_read", respNil.ToolCalls[0].Name)
}

func TestSDKClient_ChatStream_ToolNameMapRequestLocal(t *testing.T) {
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"server_read"}}

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

	resp, err := c.ChatStream(
		context.Background(),
		[]llmtypes.Message{{Role: "user", Content: "go"}},
		[]shuttle.Tool{&mockTool{name: "server:read"}},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "server:read", resp.ToolCalls[0].Name)
	assert.Equal(t, map[string]interface{}{"path": "x"}, resp.ToolCalls[0].Input)
}

// TestSDKClient_ConcurrentChat_ToolNames exercises the crash scenario from the
// field report: one shared SDKClient receiving concurrent Chat calls that both
// carry tools (as fork-join/parallel workflows do with a shared provider).
// Before the fix this was a data race on the client's shared map — fatal
// "concurrent map writes" under load and wrong tool names under benign
// interleavings. Run with -race.
func TestSDKClient_ConcurrentChat_ToolNames(t *testing.T) {
	// The fake server always answers with a tool_use for sanitized "server_read".
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant",` +
			`"content":[{"type":"tool_use","id":"tu_1","name":"server_read","input":{}}],` +
			`"model":"test-model","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`))
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

	messages := []llmtypes.Message{{Role: "user", Content: "go"}}

	// Goroutine A's tool sanitizes to "server_read"; goroutine B's tool IS
	// "server_read". Each response must restore its own request's name.
	cases := []struct {
		toolName string
		want     string
	}{
		{toolName: "server:read", want: "server:read"},
		{toolName: "server_read", want: "server_read"},
	}

	const iterations = 25
	var wg sync.WaitGroup
	errs := make(chan error, len(cases)*iterations)
	start := make(chan struct{})

	for _, tc := range cases {
		wg.Add(1)
		go func(toolName, want string) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				resp, err := c.Chat(context.Background(), messages,
					[]shuttle.Tool{&mockTool{name: toolName}})
				if err != nil {
					errs <- err
					return
				}
				if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != want {
					errs <- fmt.Errorf("tool %q: got %+v, want name %q",
						toolName, resp.ToolCalls, want)
					return
				}
			}
		}(tc.toolName, tc.want)
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
