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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/types"
)

const (
	okMessage   = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	okSSEStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	overloadedBody = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
)

// transientTestClient builds a client whose limiter retries quickly
// (MaxRetries 3, 5ms backoff), matching the OpenAI 5xx tests.
func transientTestClient(url string) *Client {
	return NewClient(Config{
		APIKey:   "test-key-not-real-5xx",
		Endpoint: url,
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			MaxRetries:        3,
			RetryBackoff:      5 * time.Millisecond,
		},
	})
}

// send runs one Chat (callAPI) or ChatStream call and returns the response
// content (for streaming, the response content plus every token the callback
// saw).
func send(t *testing.T, c *Client, stream bool) (string, error) {
	t.Helper()
	msgs := []types.Message{{Role: "user", Content: "hi"}}
	if !stream {
		resp, err := c.Chat(context.Background(), msgs, nil)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
	// ChatStream invokes the token callback synchronously on the calling
	// goroutine, so a plain builder is safe here.
	var streamed strings.Builder
	resp, err := c.ChatStream(context.Background(), msgs, nil, func(tok string) { streamed.WriteString(tok) })
	if err != nil {
		return "", err
	}
	return resp.Content + "|" + streamed.String(), nil
}

// A provider 5xx (including Anthropic's 529 overloaded) is retried by the rate
// limiter on both the callAPI and ChatStream paths: two failures then a
// success must be absorbed, with exactly three server hits.
func TestAnthropic5xxIsRetriedThroughRateLimiter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
			529,
		} {
			name := fmt.Sprintf("chat/%d", status)
			if stream {
				name = fmt.Sprintf("stream/%d", status)
			}
			t.Run(name, func(t *testing.T) {
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if hits.Add(1) <= 2 {
						w.WriteHeader(status)
						_, _ = w.Write([]byte(overloadedBody))
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte(okSSEStream))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(okMessage))
				}))
				defer srv.Close()

				content, err := send(t, transientTestClient(srv.URL), stream)
				require.NoError(t, err, "two %d responses then success must be absorbed by the limiter", status)
				assert.Equal(t, int32(3), hits.Load(), "2 failures + 1 success")
				assert.Contains(t, content, "ok")
				if stream {
					// Only the successful attempt's tokens reach the caller.
					assert.Equal(t, "ok|ok", content)
				}
			})
		}
	}
}

// The transient budget is MaxRetries+1 attempts; once spent, the typed
// RetriesExhaustedError surfaces with the provider's status intact.
func TestAnthropic5xxExhaustsBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(529)
				_, _ = w.Write([]byte(overloadedBody))
			}))
			defer srv.Close()

			_, err := send(t, transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(4), hits.Load(), "MaxRetries=3 means 4 attempts")
			assert.True(t, llm.IsRetriesExhausted(err))
			assert.True(t, llm.IsTransient(err))
			assert.False(t, llm.IsThrottle(err), "a 529 is not a 429: it must not reach the throttle signal")
			assert.Contains(t, err.Error(), "status 529")
		})
	}
}

// A 4xx is the caller's problem: one hit, no retry, on either path.
func TestAnthropic4xxIsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
			}))
			defer srv.Close()

			_, err := send(t, transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(1), hits.Load())
			assert.False(t, llm.IsTransient(err))
		})
	}
}
