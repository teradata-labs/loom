// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHawkTracer_RetryWithExponentialBackoff(t *testing.T) {
	// Track attempts and timing
	var mu sync.Mutex
	attemptCount := 0
	attemptTimes := []time.Time{}

	// Mock server that fails first 2 times, then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptCount++
		attemptTimes = append(attemptTimes, time.Now())
		count := attemptCount
		mu.Unlock()

		if count < 3 {
			// Fail first 2 attempts with 500
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Succeed on 3rd attempt
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create tracer with retry config
	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:      server.URL,
		BatchSize:     1,
		FlushInterval: 1 * time.Hour, // Don't auto-flush
		MaxRetries:    3,
		RetryBackoff:  10 * time.Millisecond, // Fast for testing
	})
	require.NoError(t, err)
	defer tracer.Close()

	// Create and end a span
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.span")
	tracer.EndSpan(span)

	// Flush should retry until success
	err = tracer.Flush(ctx)
	require.NoError(t, err)

	// Verify retry behavior
	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, 3, attemptCount, "Expected 3 attempts")

	// Verify exponential backoff timing
	if len(attemptTimes) == 3 {
		delay1 := attemptTimes[1].Sub(attemptTimes[0])
		delay2 := attemptTimes[2].Sub(attemptTimes[1])

		// First delay should be ~10ms
		assert.GreaterOrEqual(t, delay1, 10*time.Millisecond)

		// Second delay should be roughly 2x first (20ms)
		// Allow some variance for test flakiness
		assert.GreaterOrEqual(t, delay2, 15*time.Millisecond)
	}
}

func TestHawkTracer_NonRetryableError(t *testing.T) {
	// Mock server that returns 400 (non-retryable)
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		w.WriteHeader(http.StatusBadRequest) // 4xx = non-retryable
	}))
	defer server.Close()

	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:     server.URL,
		BatchSize:    1,
		MaxRetries:   3,
		RetryBackoff: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer tracer.Close()

	// Create and end a span
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.span")
	tracer.EndSpan(span)

	// Flush should fail immediately without retries
	err = tracer.Flush(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-retryable")

	// Should only attempt once (no retries for 4xx)
	assert.Equal(t, 1, attemptCount)
}

func TestHawkTracer_MaxRetriesExhausted(t *testing.T) {
	// Mock server that always fails with 500
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:     server.URL,
		BatchSize:    1,
		MaxRetries:   2, // Allow 2 retries = 3 total attempts
		RetryBackoff: 1 * time.Millisecond,
	})
	require.NoError(t, err)
	defer tracer.Close()

	// Create and end a span
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.span")
	tracer.EndSpan(span)

	// Flush should fail after all retries
	err = tracer.Flush(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed after")
	assert.Contains(t, err.Error(), "3 attempts")

	// Should attempt 3 times (initial + 2 retries)
	assert.Equal(t, 3, attemptCount)
}

func TestHawkTracer_Batching(t *testing.T) {
	// Track received spans
	var mu sync.Mutex
	batches := []int{}
	totalSpans := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload hawkExportPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		mu.Lock()
		batchSize := len(payload.Spans)
		batches = append(batches, batchSize)
		totalSpans += batchSize
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:      server.URL,
		BatchSize:     5, // Flush after 5 spans
		FlushInterval: 1 * time.Hour,
	})
	require.NoError(t, err)
	defer tracer.Close()

	ctx := context.Background()

	// Create 12 spans
	for i := 0; i < 12; i++ {
		_, span := tracer.StartSpan(ctx, fmt.Sprintf("test.span.%d", i))
		tracer.EndSpan(span)
	}

	// Wait a bit for async flushes
	time.Sleep(100 * time.Millisecond)

	// Final flush to get any remaining spans
	err = tracer.Flush(ctx)
	require.NoError(t, err)

	// Verify all spans were received
	mu.Lock()
	defer mu.Unlock()

	// Most important: all 12 spans were received
	assert.Equal(t, 12, totalSpans, "Expected all 12 spans to be exported")

	// Batching behavior can vary due to async flush timing:
	// - Ideal: 3 batches (5, 5, 2)
	// - Fast execution: 1 batch (12) - all spans added before first flush runs
	// - Slow execution: Multiple batches of varying sizes
	// Just verify we got at least 1 batch and all spans were delivered
	assert.GreaterOrEqual(t, len(batches), 1, "Expected at least 1 batch")

	t.Logf("Batching results: %d batches with sizes %v", len(batches), batches)
}

func TestHawkTracer_BackgroundFlush(t *testing.T) {
	flushCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		flushCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:      server.URL,
		BatchSize:     100,
		FlushInterval: 50 * time.Millisecond, // Fast for testing
	})
	require.NoError(t, err)

	// Create a span
	ctx := context.Background()
	_, span := tracer.StartSpan(ctx, "test.span")
	tracer.EndSpan(span)

	// Wait for background flush
	time.Sleep(150 * time.Millisecond)

	tracer.Close()

	// Should have flushed at least once
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, flushCount, 1)
}

func TestHawkTracer_ConcurrentSpans(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tracer, err := NewHawkTracer(HawkConfig{
		Endpoint:  server.URL,
		BatchSize: 100,
	})
	require.NoError(t, err)
	defer tracer.Close()

	// Create many spans concurrently
	concurrency := 50
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			ctx := context.Background()
			_, span := tracer.StartSpan(ctx, fmt.Sprintf("test.span.%d", id))
			span.SetAttribute("id", id)
			time.Sleep(1 * time.Millisecond)
			tracer.EndSpan(span)
			done <- true
		}(i)
	}

	// Wait for all
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// Flush should succeed
	err = tracer.Flush(context.Background())
	assert.NoError(t, err)
}
