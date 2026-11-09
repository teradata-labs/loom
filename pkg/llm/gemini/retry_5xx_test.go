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
package gemini

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
	okGenerateContent = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	okGeminiStream    = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}` + "\n\n"
)

// transientTestClient builds a client whose limiter retries quickly
// (MaxRetries 3, 5ms backoff) and whose requests go to srvURL. The API key is
// unique per test so the process-wide shared limiter registry hands each test
// its own limiter.
func transientTestClient(t *testing.T, srvURL string) *Client {
	t.Helper()
	c := NewClient(Config{
		APIKey: "test-key-not-real-5xx-" + t.Name(),
		Model:  "gemini-2.5-flash",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			MaxRetries:        3,
			RetryBackoff:      5 * time.Millisecond,
		},
	})
	c.httpClient.Transport = &mockTransport{baseURL: srvURL, original: http.DefaultTransport}
	return c
}

// send runs one Chat or ChatStream call and returns the response content
// (for streaming, the response content plus every token the callback saw).
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

// A provider 5xx is retried by the rate limiter on both the Chat
// (:generateContent) and ChatStream (:streamGenerateContent) paths, which
// both send through sendOnce. Two failures then a success must be absorbed:
// exactly three server hits and a successful response.
func TestGemini5xxIsRetriedThroughRateLimiter(t *testing.T) {
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
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if hits.Add(1) <= 2 {
						w.WriteHeader(status)
						_, _ = w.Write([]byte(`{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`))
						return
					}
					if stream {
						assert.Contains(t, r.URL.Path, ":streamGenerateContent")
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte(okGeminiStream))
						return
					}
					assert.Contains(t, r.URL.Path, ":generateContent")
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(okGenerateContent))
				}))
				defer srv.Close()

				content, err := send(transientTestClient(t, srv.URL), stream)
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
func TestGemini5xxExhaustsBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(pathName(stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`))
			}))
			defer srv.Close()

			_, err := send(transientTestClient(t, srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(4), hits.Load(), "MaxRetries=3 means 4 attempts")
			assert.True(t, llm.IsRetriesExhausted(err))
			assert.True(t, llm.IsTransient(err))
			assert.Contains(t, err.Error(), "status 503")
		})
	}
}

// A 4xx is the caller's problem: one hit, no retry, on either path.
func TestGemini4xxIsNotRetried(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(pathName(stream), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`))
			}))
			defer srv.Close()

			_, err := send(transientTestClient(t, srv.URL), stream)
			require.Error(t, err)
			assert.Equal(t, int32(1), hits.Load())
			assert.False(t, llm.IsTransient(err))
			assert.False(t, llm.IsRetriesExhausted(err))
			assert.Contains(t, err.Error(), "status 400")
		})
	}
}
