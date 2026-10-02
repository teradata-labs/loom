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
package openai

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
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

const (
	okChatCompletion = `{"id":"1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	okStreamBody     = "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
)

// transientTestClient builds a client whose limiter retries quickly: the
// limiter config the PR #411 review specified (MaxRetries 3, 5ms backoff),
// with admission pacing loose enough not to dominate the test's runtime.
func transientTestClient(url string) *Client {
	return NewClient(Config{
		APIKey:   "test-key-not-real-5xx",
		Model:    "gpt-4o",
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

// send runs one Chat or ChatStream call and returns the response content
// (for streaming, the response content plus every token the callback saw).
func send(t *testing.T, c *Client, stream bool) (string, error) {
	t.Helper()
	msgs := []llmtypes.Message{{Role: "user", Content: "hi"}}
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

// A provider 5xx whose status is known before any content streams is retried
// by the rate limiter, on both the Chat and ChatStream paths (both go through
// sendRequestWithClient). Two failures then a success must be absorbed:
// exactly three server hits and a successful response.
func TestOpenAI5xxIsRetriedThroughRateLimiter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{
			http.StatusServiceUnavailable, // the review's case
			http.StatusInternalServerError,
			http.StatusBadGateway,
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
						_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable","type":"server_error"}}`))
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte(okStreamBody))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(okChatCompletion))
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
func TestOpenAI5xxExhaustsBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"unavailable","type":"server_error"}}`))
			}))
			defer srv.Close()

			_, err := send(t, transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(4), hits.Load(), "MaxRetries=3 means 4 attempts")
			assert.True(t, llm.IsRetriesExhausted(err))
			assert.True(t, llm.IsTransient(err))
			assert.Contains(t, err.Error(), "status 503")
		})
	}
}

// A 4xx is the caller's problem: one hit, no retry, on either path.
func TestOpenAI4xxIsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
			}))
			defer srv.Close()

			_, err := send(t, transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(1), hits.Load())
			assert.False(t, llm.IsTransient(err))
		})
	}
}
