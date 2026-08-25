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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// HTTP 429 must surface as an error INSIDE the rate limiter so its retry
// fires — httpClient.Do returns nil error for any status, so before this the
// limiter never saw throttling for the gemini client and 429s went straight
// to the caller. The request must also be rebuilt per attempt: the old code
// built it once outside the closure, so a retry would have re-sent a consumed
// (empty) body. Both attempts' bodies are asserted identical and complete.
func TestChat429IsRetriedWithFullBody(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()

	client := NewClient(Config{
		APIKey: "test-key-not-real-gemini429",
		Model:  "gemini-2.5-flash",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      5 * time.Millisecond,
			MaxRetries:        4,
		},
	})
	client.httpClient.Transport = &mockTransport{
		baseURL:  srv.URL,
		original: http.DefaultTransport,
	}

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hello gemini"}}, nil)
	require.NoError(t, err, "one 429 then success must be absorbed by the limiter's retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "server must see the failed attempt and the retry")
	assert.NotEmpty(t, bodies[1], "retry must carry the request body, not a consumed reader")
	assert.Equal(t, bodies[0], bodies[1], "retry must re-send the identical full body")

	var req GenerateContentRequest
	require.NoError(t, json.Unmarshal(bodies[1], &req), "retried body must be complete valid JSON")
	require.Len(t, req.Contents, 1)
	require.Len(t, req.Contents[0].Parts, 1)
	assert.Equal(t, "hello gemini", req.Contents[0].Parts[0].Text)
}
