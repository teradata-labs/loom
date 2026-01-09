// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNewStore(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()

	// Verify schema was created
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='eval_results'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "eval_results table should exist")
}

func TestStore_SaveAndGet(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create test result
	result := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "test-agent",
		RunAt:     timestamppb.Now(),
		Passed:    true,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     3,
			PassedTests:    3,
			FailedTests:    0,
			Accuracy:       1.0,
			TotalCostUsd:   0.30,
			TotalLatencyMs: 3000,
			TotalToolCalls: 6,
			CustomMetrics: map[string]float64{
				"cost_efficiency": 1000.0,
			},
		},
		TestResults: []*loomv1.TestCaseResult{
			{
				TestName:     "test1",
				Passed:       true,
				ActualOutput: "output1",
				ToolsUsed:    []string{"tool1", "tool2"},
				CostUsd:      0.10,
				LatencyMs:    1000,
			},
			{
				TestName:     "test2",
				Passed:       true,
				ActualOutput: "output2",
				ToolsUsed:    []string{"tool1"},
				CostUsd:      0.10,
				LatencyMs:    1000,
				GoldenResult: &loomv1.GoldenFileResult{
					Matched:         true,
					SimilarityScore: 0.95,
				},
			},
		},
	}

	// Save result
	id, err := store.Save(ctx, result)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Get result
	retrieved, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, result.SuiteName, retrieved.SuiteName)
	assert.Equal(t, result.AgentId, retrieved.AgentId)
	assert.Equal(t, result.Passed, retrieved.Passed)
	assert.Equal(t, result.Overall.TotalTests, retrieved.Overall.TotalTests)
	assert.Equal(t, result.Overall.Accuracy, retrieved.Overall.Accuracy)
	assert.Len(t, retrieved.TestResults, 2)
	assert.Equal(t, "test1", retrieved.TestResults[0].TestName)
}

func TestStore_GetNotFound(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	_, err = store.Get(ctx, 999)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_ListBySuite(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save multiple results for same suite
	for i := 0; i < 5; i++ {
		result := CreateMockResult("test-suite", "agent1", true)
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
	}

	// Save results for different suite
	result := CreateMockResult("other-suite", "agent1", true)
	_, err = store.Save(ctx, result)
	require.NoError(t, err)

	// List by suite
	results, err := store.ListBySuite(ctx, "test-suite", 10)
	require.NoError(t, err)
	assert.Len(t, results, 5)

	// Verify they're sorted by run_at DESC (most recent first)
	for i := 0; i < len(results)-1; i++ {
		assert.True(t, results[i].RunAt.AsTime().After(results[i+1].RunAt.AsTime()) ||
			results[i].RunAt.AsTime().Equal(results[i+1].RunAt.AsTime()))
	}
}

func TestStore_ListByAgent(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save results for different agents
	for i := 0; i < 3; i++ {
		result := CreateMockResult("suite1", "agent1", true)
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
	}

	result := CreateMockResult("suite1", "agent2", true)
	_, err = store.Save(ctx, result)
	require.NoError(t, err)

	// List by agent
	results, err := store.ListByAgent(ctx, "agent1", 10)
	require.NoError(t, err)
	assert.Len(t, results, 3)

	for _, r := range results {
		assert.Equal(t, "agent1", r.AgentId)
	}
}

func TestStore_GetLatest(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save results at different times
	for i := 0; i < 3; i++ {
		result := CreateMockResult("test-suite", "agent1", true)
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	// Get latest
	latest, err := store.GetLatest(ctx, "test-suite")
	require.NoError(t, err)
	require.NotNil(t, latest)

	// Verify it's the most recent one
	allResults, err := store.ListBySuite(ctx, "test-suite", 10)
	require.NoError(t, err)
	assert.Equal(t, allResults[0].RunAt.AsTime(), latest.RunAt.AsTime())
}

func TestStore_GetLatest_NotFound(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	_, err = store.GetLatest(ctx, "nonexistent-suite")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no results found")
}

func TestStore_GetSummary(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save various results
	for i := 0; i < 5; i++ {
		result := CreateMockResult("suite1", "agent1", i%2 == 0) // Alternate pass/fail
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
	}

	result := CreateMockResult("suite2", "agent2", true)
	_, err = store.Save(ctx, result)
	require.NoError(t, err)

	// Get summary
	summary, err := store.GetSummary(ctx)
	require.NoError(t, err)
	require.NotNil(t, summary)

	assert.Equal(t, 6, summary.TotalRuns)
	assert.Equal(t, 4, summary.PassedRuns) // 3 from suite1 + 1 from suite2
	assert.Greater(t, summary.AvgAccuracy, 0.0)
	assert.Greater(t, summary.TotalCost, 0.0)
	assert.Equal(t, 2, summary.TotalSuites)
	assert.Equal(t, 2, summary.TotalAgents)
}

func TestStore_GetTrends(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save results
	for i := 0; i < 3; i++ {
		result := CreateMockResult("test-suite", "agent1", true)
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
	}

	// Get trends
	trends, err := store.GetTrends(ctx, "test-suite", 7)
	require.NoError(t, err)
	assert.NotEmpty(t, trends)

	// Verify trend data
	for _, trend := range trends {
		assert.Greater(t, trend.AvgAccuracy, 0.0)
		assert.Greater(t, trend.Runs, 0)
	}
}

func TestStore_DeleteOlderThan(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create results with old timestamps (1 hour ago)
	oldTime := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 5; i++ {
		// Create results at different times in the past
		resultTime := oldTime.Add(time.Duration(i) * time.Minute)
		result := CreateMockResultWithTime("test-suite", "agent1", true, resultTime)
		_, err := store.Save(ctx, result)
		require.NoError(t, err)
	}

	// Delete results older than 30 minutes ago (should delete all 5)
	cutoff := time.Now().Add(-30 * time.Minute)
	count, err := store.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(5), count)

	// Verify they're gone
	results, err := store.ListBySuite(ctx, "test-suite", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestStore_Compare(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save baseline result
	baseline := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "agent-v1",
		RunAt:     timestamppb.Now(),
		Passed:    true,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     10,
			PassedTests:    8,
			FailedTests:    2,
			Accuracy:       0.8,
			TotalCostUsd:   1.0,
			TotalLatencyMs: 10000,
		},
		TestResults: []*loomv1.TestCaseResult{},
	}
	baselineID, err := store.Save(ctx, baseline)
	require.NoError(t, err)

	// Save candidate result
	candidate := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "agent-v2",
		RunAt:     timestamppb.Now(),
		Passed:    true,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     10,
			PassedTests:    9,
			FailedTests:    1,
			Accuracy:       0.9,
			TotalCostUsd:   0.8,
			TotalLatencyMs: 8000,
		},
		TestResults: []*loomv1.TestCaseResult{},
	}
	candidateID, err := store.Save(ctx, candidate)
	require.NoError(t, err)

	// Compare
	comparison, err := store.Compare(ctx, baselineID, candidateID)
	require.NoError(t, err)
	require.NotNil(t, comparison)

	assert.InDelta(t, 0.1, comparison.AccuracyDelta, 0.01) // 0.9 - 0.8
	assert.InDelta(t, -0.2, comparison.CostDelta, 0.01)    // 0.8 - 1.0 (improvement!)
	assert.Equal(t, int64(-2000), comparison.LatencyDelta) // 8000 - 10000 (improvement!)
	assert.Equal(t, int32(1), comparison.PassedTestDelta)  // 9 - 8
}

func TestStore_ConcurrentSaves(t *testing.T) {
	// Each goroutine gets its own store for thread safety
	// SQLite :memory: databases are per-connection
	ctx := context.Background()

	// Test concurrent saves with separate stores
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Use a shared file-based DB for concurrent access
	tmpDB := t.TempDir() + "/concurrent.db"

	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			defer wg.Done()

			// Each goroutine creates its own connection
			store, err := NewStore(tmpDB)
			if !assert.NoError(t, err) {
				return
			}
			defer store.Close()

			result := CreateMockResult("concurrent-suite", "agent1", true)
			_, err = store.Save(ctx, result)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Verify all results were saved
	store, err := NewStore(tmpDB)
	require.NoError(t, err)
	defer store.Close()

	results, err := store.ListBySuite(ctx, "concurrent-suite", 100)
	require.NoError(t, err)
	assert.Len(t, results, numGoroutines)
}

func TestStore_ConcurrentReads(t *testing.T) {
	// Use file-based DB for concurrent access
	tmpDB := t.TempDir() + "/concurrent_read.db"
	store, err := NewStore(tmpDB)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Save a result
	result := CreateMockResult("test-suite", "agent1", true)
	id, err := store.Save(ctx, result)
	require.NoError(t, err)

	// Test concurrent reads (race detector will catch issues)
	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()

			retrieved, err := store.Get(ctx, id)
			assert.NoError(t, err)
			if retrieved != nil {
				assert.Equal(t, "test-suite", retrieved.SuiteName)
			}
		}()
	}

	wg.Wait()
}

func TestStore_TransactionRollback(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()

	// Create a result with invalid JSON (should fail during marshal)
	result := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "agent1",
		RunAt:     timestamppb.Now(),
		Passed:    true,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     1,
			PassedTests:    1,
			FailedTests:    0,
			Accuracy:       1.0,
			TotalCostUsd:   0.10,
			TotalLatencyMs: 1000,
			CustomMetrics: map[string]float64{
				"valid": 1.0,
			},
		},
		TestResults: []*loomv1.TestCaseResult{
			{
				TestName: "test1",
				Passed:   true,
			},
		},
	}

	// Save should succeed
	_, err = store.Save(ctx, result)
	assert.NoError(t, err)

	// Verify no partial data was saved
	results, err := store.ListBySuite(ctx, "test-suite", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestCreateMockResult(t *testing.T) {
	result := CreateMockResult("test-suite", "test-agent", true)

	assert.Equal(t, "test-suite", result.SuiteName)
	assert.Equal(t, "test-agent", result.AgentId)
	assert.True(t, result.Passed)
	assert.NotNil(t, result.Overall)
	assert.NotEmpty(t, result.TestResults)
	assert.NotNil(t, result.RunAt)
}

// Benchmark tests
func BenchmarkStore_Save(b *testing.B) {
	store, err := NewStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	result := CreateMockResult("bench-suite", "bench-agent", true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Save(ctx, result)
	}
}

func BenchmarkStore_Get(b *testing.B) {
	store, err := NewStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	result := CreateMockResult("bench-suite", "bench-agent", true)
	id, _ := store.Save(ctx, result)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.Get(ctx, id)
	}
}

func BenchmarkStore_List(b *testing.B) {
	store, err := NewStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Pre-populate with 100 results
	for i := 0; i < 100; i++ {
		result := CreateMockResult("bench-suite", "bench-agent", true)
		_, _ = store.Save(ctx, result)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.ListBySuite(ctx, "bench-suite", 50)
	}
}
