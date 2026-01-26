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

package storage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStorage provides in-memory trace storage with ring buffer eviction.
// Thread-safe for concurrent access. Suitable for development and testing.
type MemoryStorage struct {
	mu         sync.RWMutex
	maxTraces  int
	evals      map[string]*Eval
	runs       map[string]*EvalRun // All runs by ID
	runsByEval map[string][]string // Run IDs grouped by eval ID
	metrics    map[string]*EvalMetrics
	closed     bool
}

// NewMemoryStorage creates a new in-memory storage backend
func NewMemoryStorage(maxTraces int) *MemoryStorage {
	if maxTraces <= 0 {
		maxTraces = 10000
	}
	return &MemoryStorage{
		maxTraces:  maxTraces,
		evals:      make(map[string]*Eval),
		runs:       make(map[string]*EvalRun),
		runsByEval: make(map[string][]string),
		metrics:    make(map[string]*EvalMetrics),
	}
}

// CreateEval creates a new evaluation session
func (m *MemoryStorage) CreateEval(ctx context.Context, eval *Eval) error {
	if eval == nil {
		return fmt.Errorf("eval cannot be nil")
	}
	if eval.ID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("storage is closed")
	}

	// Set timestamps if not provided
	if eval.CreatedAt == 0 {
		eval.CreatedAt = time.Now().Unix()
	}
	if eval.UpdatedAt == 0 {
		eval.UpdatedAt = eval.CreatedAt
	}

	m.evals[eval.ID] = eval
	return nil
}

// UpdateEvalStatus updates the status of an evaluation
func (m *MemoryStorage) UpdateEvalStatus(ctx context.Context, evalID string, status string) error {
	if evalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("storage is closed")
	}

	eval, exists := m.evals[evalID]
	if !exists {
		return fmt.Errorf("eval not found: %s", evalID)
	}

	eval.Status = status
	eval.UpdatedAt = time.Now().Unix()
	return nil
}

// CreateEvalRun stores a new trace/span
func (m *MemoryStorage) CreateEvalRun(ctx context.Context, run *EvalRun) error {
	if run == nil {
		return fmt.Errorf("eval run cannot be nil")
	}
	if run.ID == "" {
		return fmt.Errorf("eval run ID cannot be empty")
	}
	if run.EvalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("storage is closed")
	}

	// Check if we need to evict old runs (FIFO)
	if len(m.runs) >= m.maxTraces {
		m.evictOldestRunsLocked()
	}

	// Store run
	m.runs[run.ID] = run

	// Add to eval's run list
	m.runsByEval[run.EvalID] = append(m.runsByEval[run.EvalID], run.ID)

	return nil
}

// evictOldestRunsLocked removes oldest 10% of runs to make space (caller must hold lock)
func (m *MemoryStorage) evictOldestRunsLocked() {
	// Find oldest runs by timestamp
	type runEntry struct {
		id        string
		timestamp int64
	}

	runs := make([]runEntry, 0, len(m.runs))
	for id, run := range m.runs {
		runs = append(runs, runEntry{id: id, timestamp: run.Timestamp})
	}

	// Sort by timestamp
	for i := 0; i < len(runs)-1; i++ {
		for j := i + 1; j < len(runs); j++ {
			if runs[i].timestamp > runs[j].timestamp {
				runs[i], runs[j] = runs[j], runs[i]
			}
		}
	}

	// Remove oldest 10%
	evictCount := m.maxTraces / 10
	if evictCount < 1 {
		evictCount = 1
	}

	for i := 0; i < evictCount && i < len(runs); i++ {
		runID := runs[i].id
		run := m.runs[runID]

		// Remove from runs map
		delete(m.runs, runID)

		// Remove from runsByEval index
		if evalRuns, exists := m.runsByEval[run.EvalID]; exists {
			newRuns := make([]string, 0, len(evalRuns)-1)
			for _, id := range evalRuns {
				if id != runID {
					newRuns = append(newRuns, id)
				}
			}
			m.runsByEval[run.EvalID] = newRuns
		}
	}
}

// CalculateEvalMetrics calculates aggregated metrics for an evaluation
func (m *MemoryStorage) CalculateEvalMetrics(ctx context.Context, evalID string) (*EvalMetrics, error) {
	if evalID == "" {
		return nil, fmt.Errorf("eval ID cannot be empty")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, fmt.Errorf("storage is closed")
	}

	runIDs, exists := m.runsByEval[evalID]
	if !exists || len(runIDs) == 0 {
		// Return empty metrics
		return &EvalMetrics{
			EvalID:    evalID,
			UpdatedAt: time.Now().Unix(),
		}, nil
	}

	metrics := &EvalMetrics{
		EvalID: evalID,
	}

	var totalExecTime int64
	var totalTokens int64
	var firstTimestamp int64 = 9999999999
	var lastTimestamp int64

	for _, runID := range runIDs {
		run, exists := m.runs[runID]
		if !exists {
			continue // Skip if run was evicted
		}

		metrics.TotalRuns++
		if run.Success {
			metrics.SuccessfulRuns++
		} else {
			metrics.FailedRuns++
		}

		totalExecTime += run.ExecutionTimeMS
		totalTokens += int64(run.TokenCount)

		if run.Timestamp < firstTimestamp {
			firstTimestamp = run.Timestamp
		}
		if run.Timestamp > lastTimestamp {
			lastTimestamp = run.Timestamp
		}
	}

	// Calculate averages
	if metrics.TotalRuns > 0 {
		metrics.SuccessRate = float64(metrics.SuccessfulRuns) / float64(metrics.TotalRuns)
		metrics.AvgExecutionTimeMS = float64(totalExecTime) / float64(metrics.TotalRuns)
		metrics.AvgTokensPerRun = float64(totalTokens) / float64(metrics.TotalRuns)
	}

	metrics.TotalTokens = totalTokens
	metrics.FirstRunTimestamp = firstTimestamp
	metrics.LastRunTimestamp = lastTimestamp
	metrics.UpdatedAt = time.Now().Unix()

	return metrics, nil
}

// UpsertEvalMetrics stores or updates metrics for an evaluation
func (m *MemoryStorage) UpsertEvalMetrics(ctx context.Context, metrics *EvalMetrics) error {
	if metrics == nil {
		return fmt.Errorf("metrics cannot be nil")
	}
	if metrics.EvalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("storage is closed")
	}

	m.metrics[metrics.EvalID] = metrics
	return nil
}

// Close closes the memory storage (no-op for memory storage)
func (m *MemoryStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	return nil
}

// GetStats returns current storage statistics (for testing/debugging)
func (m *MemoryStorage) GetStats() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]int{
		"evals":   len(m.evals),
		"runs":    len(m.runs),
		"metrics": len(m.metrics),
	}
}

// Compile-time interface check
var _ Storage = (*MemoryStorage)(nil)
