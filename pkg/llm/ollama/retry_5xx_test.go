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
package ollama

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

const okOllamaChat = `{"model":"llama3.1","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":1}` + "\n"

// transientTestClient builds a client whose limiter retries quickly
// (MaxRetries 3, 5ms backoff). The shared limiter is scoped by endpoint, so
// each test server gets its own limiter.
func transientTestClient(srvURL string) *Client {
	return NewClient(Config{
		Endpoint: srvURL,
		Model:    "llama3.1",
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
// Chat delegates to ChatStream with a nil callback, so both entry points
// reach the same rate-limited sendOnce; both are covered so a future split
// of the paths cannot drop the 5xx retry from one of them unnoticed.
func send(c *Client, stream bool) (string, error) {
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

func pathName(stream bool) string {
	if stream {
		return "stream"
	}
	return "chat"
}

// newChatServer serves /api/show for the tool-support probe and counts
// /api/chat hits, answering each with handle.
func newChatServer(t *testing.T, hits *atomic.Int32, handle func(n int32, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mockShowResponse(w, r) {
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handle(hits.Add(1), w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A 5xx (on Ollama typically a model loading or being evicted, or a fronting
// gateway's 502/503/504) is retried by the rate limiter. Two failures then a
// success must be absorbed: exactly three /api/chat hits and a successful
// response.
func TestOllama5xxIsRetriedThroughRateLimiter(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
			529,
		} {
			t.Run(fmt.Sprintf("%s/%d", pathName(stream), status), func(t *testing.T) {
				var hits atomic.Int32
				srv := newChatServer(t, &hits, func(n int32, w http.ResponseWriter) {
					if n <= 2 {
						w.WriteHeader(status)
						_, _ = w.Write([]byte(`{"error":"model is loading"}`))
						return
					}
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = w.Write([]byte(okOllamaChat))
				})

				content, err := send(transientTestClient(srv.URL), stream)
				require.NoError(t, err, "two %d responses then success must be absorbed by the limiter", status)
				assert.Equal(t, int32(3), hits.Load(), "2 failures + 1 success")
				if stream {
					// Only the successful attempt's tokens reach the caller.
					assert.Equal(t, "ok|ok", content)
				} else {
					assert.Equal(t, "ok", content)
				}
			})
		}
	}
}

// The transient budget is MaxRetries+1 attempts; once spent, the typed
// RetriesExhaustedError surfaces with the provider's status intact.
func TestOllama5xxExhaustsBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(pathName(stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := newChatServer(t, &hits, func(_ int32, w http.ResponseWriter) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"server busy"}`))
			})

			_, err := send(transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(4), hits.Load(), "MaxRetries=3 means 4 attempts")
			assert.True(t, llm.IsRetriesExhausted(err))
			assert.True(t, llm.IsTransient(err))
			assert.Contains(t, err.Error(), "status 503")
		})
	}
}

// A 4xx is the caller's problem: one hit, no retry, on either path.
func TestOllama4xxIsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(pathName(stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := newChatServer(t, &hits, func(_ int32, w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"model 'llama3.1' not found"}`))
			})

			_, err := send(transientTestClient(srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(1), hits.Load())
			assert.False(t, llm.IsTransient(err))
			assert.False(t, llm.IsRetriesExhausted(err))
			assert.Contains(t, err.Error(), "status 404")
		})
	}
}
