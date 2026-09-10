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

func retryTestLimiterConfig() llm.RateLimiterConfig {
	return llm.RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		BurstCapacity:     8,
		MinDelay:          time.Millisecond,
		RetryBackoff:      5 * time.Millisecond,
		MaxRetries:        4,
	}
}

// HTTP 429 must surface as an error INSIDE the rate limiter so its retry
// fires — httpClient.Do returns nil error for any status, so before this the
// limiter never saw throttling for the openai client and 429s went straight
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
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"too_many_requests"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client := NewClient(Config{
		APIKey:            "test-key-not-real-chat429",
		Model:             "gpt-4o",
		Endpoint:          srv.URL,
		RateLimiterConfig: retryTestLimiterConfig(),
	})

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hello there"}}, nil)
	require.NoError(t, err, "one 429 then success must be absorbed by the limiter's retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "server must see the failed attempt and the retry")
	assert.NotEmpty(t, bodies[1], "retry must carry the request body, not a consumed reader")
	assert.Equal(t, bodies[0], bodies[1], "retry must re-send the identical full body")

	var req ChatCompletionRequest
	require.NoError(t, json.Unmarshal(bodies[1], &req), "retried body must be complete valid JSON")
	assert.Equal(t, "gpt-4o", req.Model)
	require.Len(t, req.Messages, 1)
	assert.Equal(t, "hello there", req.Messages[0].Content)
}

// Same regression for the streaming path (which mistral/litellm inherit).
func TestChatStream429IsRetriedWithFullBody(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"too_many_requests"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client := NewClient(Config{
		APIKey:            "test-key-not-real-stream429",
		Model:             "gpt-4o",
		Endpoint:          srv.URL,
		RateLimiterConfig: retryTestLimiterConfig(),
	})

	resp, err := client.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hello stream"}}, nil, nil)
	require.NoError(t, err, "one 429 then success must be absorbed by the limiter's retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "server must see the failed attempt and the retry")
	assert.NotEmpty(t, bodies[1], "retry must carry the request body, not a consumed reader")
	assert.Equal(t, bodies[0], bodies[1], "retry must re-send the identical full body")

	var req ChatCompletionRequest
	require.NoError(t, json.Unmarshal(bodies[1], &req), "retried body must be complete valid JSON")
	assert.True(t, req.Stream)
	require.Len(t, req.Messages, 1)
	assert.Equal(t, "hello stream", req.Messages[0].Content)
}
