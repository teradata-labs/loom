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

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHawkJudgeExporter_SuccessfulBatchExport(t *testing.T) {
	// Setup mock Hawk server
	var receivedVerdicts []*loomv1.JudgeResult
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/judge-verdicts", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		verdicts, ok := payload["verdicts"].([]interface{})
		require.True(t, ok)

		mu.Lock()
		for range verdicts {
			receivedVerdicts = append(receivedVerdicts, &loomv1.JudgeResult{
				JudgeId:   "test",
				JudgeName: "test-judge",
			})
		}
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create exporter with small batch size
	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		APIKey:        "test-api-key",
		BatchSize:     3,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    10,
		Timeout:       5 * time.Second,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Export 5 verdicts (should trigger 2 batches: 3 + 2)
	for i := 0; i < 5; i++ {
		err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
			JudgeId:      "test-judge-1",
			JudgeName:    "Test Judge",
			Verdict:      "PASS",
			OverallScore: 85.0,
			JudgedAt:     timestamppb.Now(),
		})
		require.NoError(t, err)
	}

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	// Verify verdicts received
	mu.Lock()
	assert.Greater(t, len(receivedVerdicts), 0, "Should have received verdicts")
	mu.Unlock()
}

func TestHawkJudgeExporter_BufferFullFlush(t *testing.T) {
	var flushCount int32
	var mu sync.Mutex
	var receivedBatches []int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		verdicts := payload["verdicts"].([]interface{})

		mu.Lock()
		receivedBatches = append(receivedBatches, len(verdicts))
		mu.Unlock()

		atomic.AddInt32(&flushCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     5, // Flush when 5 verdicts accumulated
		FlushInterval: 10 * time.Second,
		BufferSize:    20,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Export exactly 5 verdicts (should trigger one batch)
	for i := 0; i < 5; i++ {
		err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
			JudgeId:   "judge-1",
			JudgeName: "Judge 1",
			Verdict:   "PASS",
		})
		require.NoError(t, err)
	}

	// Wait for batch to be sent
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(1), atomic.LoadInt32(&flushCount), "Should flush once when buffer full")

	mu.Lock()
	assert.Equal(t, []int{5}, receivedBatches, "Should receive batch of 5")
	mu.Unlock()
}

func TestHawkJudgeExporter_TimerFlush(t *testing.T) {
	var flushCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&flushCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     100,                    // Large batch size
		FlushInterval: 100 * time.Millisecond, // Short flush interval
		BufferSize:    10,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Export 2 verdicts (not enough to trigger batch flush)
	for i := 0; i < 2; i++ {
		err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
			JudgeId:   "judge-1",
			JudgeName: "Judge 1",
			Verdict:   "PASS",
		})
		require.NoError(t, err)
	}

	// Wait for timer to trigger flush
	time.Sleep(200 * time.Millisecond)

	assert.Greater(t, atomic.LoadInt32(&flushCount), int32(0), "Timer should trigger flush")
}

func TestHawkJudgeExporter_GracefulShutdown(t *testing.T) {
	var mu sync.Mutex
	var receivedVerdictCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		verdicts := payload["verdicts"].([]interface{})

		mu.Lock()
		receivedVerdictCount += len(verdicts)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     100, // Large batch size to prevent auto-flush
		FlushInterval: 10 * time.Second,
		BufferSize:    50,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)

	// Export 10 verdicts
	for i := 0; i < 10; i++ {
		err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
			JudgeId:   "judge-1",
			JudgeName: "Judge 1",
			Verdict:   "PASS",
		})
		require.NoError(t, err)
	}

	// Stop should flush remaining verdicts
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := exporter.Stop(stopCtx)
	require.NoError(t, err)

	// Verify all verdicts flushed
	mu.Lock()
	assert.Equal(t, 10, receivedVerdictCount, "All verdicts should be flushed on shutdown")
	mu.Unlock()
}

func TestHawkJudgeExporter_HTTPError(t *testing.T) {
	// Server returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     2,
		FlushInterval: 50 * time.Millisecond,
		BufferSize:    10,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Export verdict
	err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
		JudgeId:   "judge-1",
		JudgeName: "Judge 1",
		Verdict:   "PASS",
	})
	require.NoError(t, err, "ExportJudgeResult should not fail on HTTP error")

	// Wait for flush attempt
	time.Sleep(100 * time.Millisecond)

	// Exporter should continue working despite HTTP error
}

func TestHawkJudgeExporter_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     10,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    10,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Create cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Export with cancelled context
	err := exporter.ExportJudgeResult(cancelledCtx, &loomv1.JudgeResult{
		JudgeId:   "judge-1",
		JudgeName: "Judge 1",
	})

	assert.Error(t, err, "Should return error for cancelled context")
}

func TestHawkJudgeExporter_ConcurrentExports(t *testing.T) {
	var mu sync.Mutex
	var receivedVerdictCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		verdicts := payload["verdicts"].([]interface{})

		mu.Lock()
		receivedVerdictCount += len(verdicts)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     10,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    100,
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Export concurrently from multiple goroutines
	const numGoroutines = 10
	const verdictsPerGoroutine = 10

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < verdictsPerGoroutine; j++ {
				err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
					JudgeId:   "judge-1",
					JudgeName: "Judge 1",
					Verdict:   "PASS",
				})
				assert.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	// Stop and flush
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := exporter.Stop(stopCtx)
	require.NoError(t, err)

	// Verify all verdicts received
	mu.Lock()
	assert.Equal(t, numGoroutines*verdictsPerGoroutine, receivedVerdictCount, "All concurrent verdicts should be exported")
	mu.Unlock()
}

func TestHawkJudgeExporter_StopMultipleTimes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint: server.URL,
		Logger:   zaptest.NewLogger(t),
		Tracer:   NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)

	// Stop multiple times should be safe
	err1 := exporter.Stop(ctx)
	err2 := exporter.Stop(ctx)
	err3 := exporter.Stop(ctx)

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NoError(t, err3)
}

func TestHawkJudgeExporter_ExportAfterStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint: server.URL,
		Logger:   zaptest.NewLogger(t),
		Tracer:   NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	_ = exporter.Stop(ctx)

	// Export after stop should return error
	err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
		JudgeId:   "judge-1",
		JudgeName: "Judge 1",
	})

	assert.Error(t, err, "Should return error when exporting after stop")
}

func TestHawkJudgeExporter_DefaultConfig(t *testing.T) {
	// Test with nil config (should use defaults)
	exporter := NewHawkJudgeExporter(nil)

	assert.NotNil(t, exporter)
	assert.Equal(t, "http://localhost:8080", exporter.endpoint)
	assert.Equal(t, 10, exporter.batchSize)
	assert.Equal(t, 5*time.Second, exporter.flushInterval)
	assert.NotNil(t, exporter.buffer)
	assert.NotNil(t, exporter.httpClient)
	assert.NotNil(t, exporter.logger)
	assert.NotNil(t, exporter.tracer)
}

func TestHawkJudgeExporter_BufferOverflow(t *testing.T) {
	// Slow server to cause buffer to fill
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter := NewHawkJudgeExporter(&HawkJudgeExporterConfig{
		Endpoint:      server.URL,
		BatchSize:     1,
		FlushInterval: 10 * time.Millisecond,
		BufferSize:    2, // Very small buffer
		Logger:        zaptest.NewLogger(t),
		Tracer:        NewNoOpTracer(),
	})

	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Try to export many verdicts quickly
	var bufferFullCount int
	for i := 0; i < 10; i++ {
		err := exporter.ExportJudgeResult(ctx, &loomv1.JudgeResult{
			JudgeId:   "judge-1",
			JudgeName: "Judge 1",
		})
		if err != nil && err.Error() == "buffer full" {
			bufferFullCount++
		}
	}

	// Some exports should fail due to buffer overflow
	assert.Greater(t, bufferFullCount, 0, "Some exports should fail when buffer overflows")
}
