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
