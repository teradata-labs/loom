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

// The API splits the cache write by entry lifetime (cache_creation). The
// client records the split on the usage, on the plain and the streaming
// path, so a billed row carries it; a response with no split records zero.
func TestClient_RecordsTheCacheWriteSplitByLifetime(t *testing.T) {
	t.Parallel()
	const plainSplit = `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn",
"content":[{"type":"text","text":"ok"}],
"usage":{"input_tokens":50,"output_tokens":10,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_1h_input_tokens":600,"ephemeral_5m_input_tokens":300}}}`
	const plainNoSplit = `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn",
"content":[{"type":"text","text":"ok"}],
"usage":{"input_tokens":50,"output_tokens":10,"cache_creation_input_tokens":900}}`
	const streamSplit = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"usage":{"input_tokens":50,"output_tokens":0,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_1h_input_tokens":600,"ephemeral_5m_input_tokens":300}}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	tests := []struct {
		name            string
		body            string
		stream          bool
		want1h, want5m  int
		wantCacheCreate int
	}{
		{name: "plain response with a split", body: plainSplit, want1h: 600, want5m: 300, wantCacheCreate: 900},
		{name: "plain response without a split", body: plainNoSplit, want1h: 0, want5m: 0, wantCacheCreate: 900},
		{name: "streamed response with a split", body: streamSplit, stream: true, want1h: 600, want5m: 300, wantCacheCreate: 900},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client := NewClient(Config{APIKey: "test-key", Endpoint: server.URL})
			msgs := []types.Message{{Role: "user", Content: "hi"}}

			var resp *types.LLMResponse
			var err error
			if tt.stream {
				resp, err = client.ChatStream(context.Background(), msgs, nil, func(string) {})
			} else {
				resp, err = client.Chat(&mockContext{Context: context.Background()}, msgs, nil)
			}
			require.NoError(t, err, "the call must succeed")
			assert.Equal(t, tt.wantCacheCreate, resp.Usage.CacheCreationInputTokens, "the cache write total is wrong")
			assert.Equal(t, tt.want1h, resp.Usage.CacheCreation1hInputTokens, "the 1h part of the cache write is wrong")
			assert.Equal(t, tt.want5m, resp.Usage.CacheCreation5mInputTokens, "the 5m part of the cache write is wrong")
		})
	}
}
