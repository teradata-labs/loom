// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

type recordingObserver struct {
	mu        sync.Mutex
	limit     int64
	remaining int64
	reset     time.Duration
	throttles []time.Duration
}

func (r *recordingObserver) UpdateFromHeaders(limit, remaining int64, reset time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limit, r.remaining, r.reset = limit, remaining, reset
}

func (r *recordingObserver) ObserveThrottle(retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.throttles = append(r.throttles, retryAfter)
}

// Every response's ratelimit headers must reach the CapacityObserver, and a
// 429's Retry-After must arrive as an ObserveThrottle. This is the telemetry
// feed the LLM slot scheduler calibrates from.
func TestCapacityObserverReceivesHeadersAndThrottles(t *testing.T) {
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"exceeded rate limit"}}`))
			return
		}
		w.Header().Set("x-ratelimit-limit-tokens", "1500000")
		w.Header().Set("x-ratelimit-remaining-tokens", "745399")
		w.Header().Set("x-ratelimit-reset-tokens", "30")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	obs := &recordingObserver{}
	client, err := NewClient(Config{
		Endpoint:         srv.URL,
		DeploymentID:     "gpt-4o-capacity-test",
		APIKey:           "test-key-not-real",
		CapacityObserver: obs,
		// No rate limiter: prove the harvest is independent of it. The 429
		// then surfaces as an error (no retry without a limiter) — expected.
	})
	require.NoError(t, err)

	_, err = client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err, "first call 429s and no limiter means no retry")

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "ok")

	obs.mu.Lock()
	defer obs.mu.Unlock()
	require.Len(t, obs.throttles, 1, "the 429 must produce exactly one ObserveThrottle")
	assert.Equal(t, 7*time.Second, obs.throttles[0], "Retry-After must be parsed")
	assert.Equal(t, int64(1_500_000), obs.limit)
	assert.Equal(t, int64(745_399), obs.remaining)
	assert.Equal(t, 30*time.Second, obs.reset)
}
