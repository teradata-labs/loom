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
	successes int
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

func (r *recordingObserver) ObserveSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes++
}

// Ratelimit headers on SUCCESSFUL responses must reach the CapacityObserver.
// That is this hook's whole job: throttle and success (AIMD) observations
// are driven provider-agnostically at the agent's LLM funnel from the call
// outcome, so a 429 must produce NO observer call here, and a non-2xx
// response's headers must never calibrate — a failing gateway proves nothing
// about capacity.
func TestCapacityObserverReceivesSuccessHeadersOnly(t *testing.T) {
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		switch call {
		case 1: // throttle: surfaces to the caller as an llm.ThrottleError
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"exceeded rate limit"}}`))
		case 2: // server error WITH headers: must not calibrate
			w.Header().Set("x-ratelimit-limit-tokens", "999")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"internal","type":"server_error"}}`))
		default: // clean 2xx with headers: calibrates
			w.Header().Set("x-ratelimit-limit-tokens", "1500000")
			w.Header().Set("x-ratelimit-remaining-tokens", "745399")
			w.Header().Set("x-ratelimit-reset-tokens", "30")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
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

	_, err = client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err, "second call is a 500")

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "ok")

	obs.mu.Lock()
	defer obs.mu.Unlock()
	assert.Empty(t, obs.throttles, "the funnel, not the client, observes throttles now")
	assert.Zero(t, obs.successes, "the funnel, not the client, observes successes now")
	assert.Equal(t, int64(1_500_000), obs.limit, "the 500's bogus headers must not calibrate; the 2xx's must")
	assert.Equal(t, int64(745_399), obs.remaining)
	assert.Equal(t, 30*time.Second, obs.reset)
}
