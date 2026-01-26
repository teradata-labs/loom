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
	"sync"
	"testing"
	"time"
)

func TestNewMemoryStorage(t *testing.T) {
	tests := []struct {
		name      string
		maxTraces int
		wantMax   int
	}{
		{
			name:      "default size",
			maxTraces: 10000,
			wantMax:   10000,
		},
		{
			name:      "custom size",
			maxTraces: 5000,
			wantMax:   5000,
		},
		{
			name:      "zero defaults to 10000",
			maxTraces: 0,
			wantMax:   10000,
		},
		{
			name:      "negative defaults to 10000",
			maxTraces: -100,
			wantMax:   10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := NewMemoryStorage(tt.maxTraces)
			if storage == nil {
				t.Fatal("NewMemoryStorage returned nil")
			}
			if storage.maxTraces != tt.wantMax {
				t.Errorf("maxTraces = %d, want %d", storage.maxTraces, tt.wantMax)
			}
			storage.Close()
		})
	}
}

func TestMemoryStorage_CreateEval(t *testing.T) {
	tests := []struct {
		name    string
		eval    *Eval
		wantErr bool
	}{
		{
			name: "valid eval",
			eval: &Eval{
				ID:     "eval-1",
				Name:   "Test Eval",
				Suite:  "test",
				Status: "running",
			},
			wantErr: false,
		},
		{
			name:    "nil eval",
			eval:    nil,
			wantErr: true,
		},
		{
			name: "empty ID",
			eval: &Eval{
				Name:   "Test",
				Suite:  "test",
				Status: "running",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := NewMemoryStorage(100)
			defer storage.Close()

			err := storage.CreateEval(context.Background(), tt.eval)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateEval() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && tt.eval != nil {
				// Verify eval was stored
				storage.mu.RLock()
				stored, exists := storage.evals[tt.eval.ID]
				storage.mu.RUnlock()

				if !exists {
					t.Error("eval was not stored")
				}
				if stored.CreatedAt == 0 {
					t.Error("CreatedAt timestamp not set")
				}
				if stored.UpdatedAt == 0 {
					t.Error("UpdatedAt timestamp not set")
				}
			}
		})
	}
}

func TestMemoryStorage_UpdateEvalStatus(t *testing.T) {
	storage := NewMemoryStorage(100)
	defer storage.Close()

	ctx := context.Background()

	// Create an eval first
	eval := &Eval{
		ID:     "eval-1",
		Name:   "Test",
		Suite:  "test",
		Status: "running",
	}
	if err := storage.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

	tests := []struct {
		name    string
		evalID  string
		status  string
		wantErr bool
	}{
		{
			name:    "update existing eval",
			evalID:  "eval-1",
			status:  "completed",
			wantErr: false,
		},
		{
			name:    "update non-existent eval",
			evalID:  "eval-999",
			status:  "completed",
			wantErr: true,
		},
		{
			name:    "empty eval ID",
			evalID:  "",
			status:  "completed",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := storage.UpdateEvalStatus(ctx, tt.evalID, tt.status)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateEvalStatus() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				storage.mu.RLock()
				updated := storage.evals[tt.evalID]
				storage.mu.RUnlock()

				if updated.Status != tt.status {
					t.Errorf("status = %q, want %q", updated.Status, tt.status)
				}
			}
		})
	}
}

func TestMemoryStorage_CreateEvalRun(t *testing.T) {
	storage := NewMemoryStorage(100)
	defer storage.Close()

	ctx := context.Background()

	tests := []struct {
		name    string
		run     *EvalRun
		wantErr bool
	}{
		{
			name: "valid run",
			run: &EvalRun{
				ID:              "run-1",
				EvalID:          "eval-1",
				Query:           "test query",
				Model:           "claude",
				Response:        "test response",
				ExecutionTimeMS: 100,
				TokenCount:      50,
				Success:         true,
				Timestamp:       time.Now().Unix(),
			},
			wantErr: false,
		},
		{
			name:    "nil run",
			run:     nil,
			wantErr: true,
		},
		{
			name: "empty run ID",
			run: &EvalRun{
				EvalID: "eval-1",
			},
			wantErr: true,
		},
		{
			name: "empty eval ID",
			run: &EvalRun{
				ID: "run-1",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := storage.CreateEvalRun(ctx, tt.run)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateEvalRun() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && tt.run != nil {
				storage.mu.RLock()
				stored, exists := storage.runs[tt.run.ID]
				storage.mu.RUnlock()

				if !exists {
					t.Error("run was not stored")
				}
				if stored.ID != tt.run.ID {
					t.Errorf("run ID = %q, want %q", stored.ID, tt.run.ID)
				}
			}
		})
	}
}

func TestMemoryStorage_Eviction(t *testing.T) {
	// Create small storage to test eviction
	storage := NewMemoryStorage(10)
	defer storage.Close()

	ctx := context.Background()

	// Fill storage to capacity + more
	for i := 0; i < 15; i++ {
		run := &EvalRun{
			ID:        string(rune('a' + i)),
			EvalID:    "eval-1",
			Timestamp: int64(i),
		}
		if err := storage.CreateEvalRun(ctx, run); err != nil {
			t.Fatalf("CreateEvalRun(%d) failed: %v", i, err)
		}
	}

	// Check that eviction happened
	stats := storage.GetStats()
	if stats["runs"] > 10 {
		t.Errorf("runs = %d, want <= 10 (eviction should have occurred)", stats["runs"])
	}

	// Verify oldest runs were evicted (lowest timestamps)
	storage.mu.RLock()
	_, hasOldest := storage.runs["a"] // First run (timestamp 0)
	_, hasNewest := storage.runs["o"] // Last run (timestamp 14)
	storage.mu.RUnlock()

	if hasOldest {
		t.Error("oldest run should have been evicted")
	}
	if !hasNewest {
		t.Error("newest run should still exist")
	}
}

func TestMemoryStorage_CalculateEvalMetrics(t *testing.T) {
	storage := NewMemoryStorage(100)
	defer storage.Close()

	ctx := context.Background()
	evalID := "eval-1"

	// Test empty eval
	t.Run("empty eval", func(t *testing.T) {
		metrics, err := storage.CalculateEvalMetrics(ctx, evalID)
		if err != nil {
			t.Fatalf("CalculateEvalMetrics() failed: %v", err)
		}
		if metrics.TotalRuns != 0 {
			t.Errorf("TotalRuns = %d, want 0", metrics.TotalRuns)
		}
	})

	// Add test runs
	runs := []*EvalRun{
		{
			ID:              "run-1",
			EvalID:          evalID,
			ExecutionTimeMS: 100,
			TokenCount:      50,
			Success:         true,
			Timestamp:       1000,
		},
		{
			ID:              "run-2",
			EvalID:          evalID,
			ExecutionTimeMS: 200,
			TokenCount:      75,
			Success:         true,
			Timestamp:       2000,
		},
		{
			ID:              "run-3",
			EvalID:          evalID,
			ExecutionTimeMS: 150,
			TokenCount:      60,
			Success:         false,
			ErrorMessage:    "test error",
			Timestamp:       3000,
		},
	}

	for _, run := range runs {
		if err := storage.CreateEvalRun(ctx, run); err != nil {
			t.Fatalf("CreateEvalRun() failed: %v", err)
		}
	}

	t.Run("with runs", func(t *testing.T) {
		metrics, err := storage.CalculateEvalMetrics(ctx, evalID)
		if err != nil {
			t.Fatalf("CalculateEvalMetrics() failed: %v", err)
		}

		if metrics.TotalRuns != 3 {
			t.Errorf("TotalRuns = %d, want 3", metrics.TotalRuns)
		}
		if metrics.SuccessfulRuns != 2 {
			t.Errorf("SuccessfulRuns = %d, want 2", metrics.SuccessfulRuns)
		}
		if metrics.FailedRuns != 1 {
			t.Errorf("FailedRuns = %d, want 1", metrics.FailedRuns)
		}

		wantSuccessRate := 2.0 / 3.0
		if metrics.SuccessRate < wantSuccessRate-0.01 || metrics.SuccessRate > wantSuccessRate+0.01 {
			t.Errorf("SuccessRate = %f, want ~%f", metrics.SuccessRate, wantSuccessRate)
		}

		wantAvgExecTime := 150.0 // (100 + 200 + 150) / 3
		if metrics.AvgExecutionTimeMS < wantAvgExecTime-1 || metrics.AvgExecutionTimeMS > wantAvgExecTime+1 {
			t.Errorf("AvgExecutionTimeMS = %f, want ~%f", metrics.AvgExecutionTimeMS, wantAvgExecTime)
		}

		if metrics.TotalTokens != 185 { // 50 + 75 + 60
			t.Errorf("TotalTokens = %d, want 185", metrics.TotalTokens)
		}

		wantAvgTokens := 185.0 / 3.0
		if metrics.AvgTokensPerRun < wantAvgTokens-1 || metrics.AvgTokensPerRun > wantAvgTokens+1 {
			t.Errorf("AvgTokensPerRun = %f, want ~%f", metrics.AvgTokensPerRun, wantAvgTokens)
		}

		if metrics.FirstRunTimestamp != 1000 {
			t.Errorf("FirstRunTimestamp = %d, want 1000", metrics.FirstRunTimestamp)
		}
		if metrics.LastRunTimestamp != 3000 {
			t.Errorf("LastRunTimestamp = %d, want 3000", metrics.LastRunTimestamp)
		}
	})
}

func TestMemoryStorage_UpsertEvalMetrics(t *testing.T) {
	storage := NewMemoryStorage(100)
	defer storage.Close()

	ctx := context.Background()

	metrics := &EvalMetrics{
		EvalID:      "eval-1",
		TotalRuns:   10,
		SuccessRate: 0.9,
		UpdatedAt:   time.Now().Unix(),
	}

	// Insert
	if err := storage.UpsertEvalMetrics(ctx, metrics); err != nil {
		t.Fatalf("UpsertEvalMetrics() failed: %v", err)
	}

	// Verify insert
	storage.mu.RLock()
	stored := storage.metrics[metrics.EvalID]
	storage.mu.RUnlock()

	if stored.TotalRuns != 10 {
		t.Errorf("TotalRuns = %d, want 10", stored.TotalRuns)
	}

	// Update
	metrics.TotalRuns = 20
	if err := storage.UpsertEvalMetrics(ctx, metrics); err != nil {
		t.Fatalf("UpsertEvalMetrics() update failed: %v", err)
	}

	// Verify update
	storage.mu.RLock()
	updated := storage.metrics[metrics.EvalID]
	storage.mu.RUnlock()

	if updated.TotalRuns != 20 {
		t.Errorf("TotalRuns = %d, want 20", updated.TotalRuns)
	}
}

func TestMemoryStorage_Concurrent(t *testing.T) {
	storage := NewMemoryStorage(1000)
	defer storage.Close()

	ctx := context.Background()
	var wg sync.WaitGroup
	numGoroutines := 10
	runsPerGoroutine := 10

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < runsPerGoroutine; j++ {
				run := &EvalRun{
					ID:        string(rune('A' + goroutineID*100 + j)),
					EvalID:    "eval-concurrent",
					Timestamp: time.Now().Unix(),
				}
				if err := storage.CreateEvalRun(ctx, run); err != nil {
					t.Errorf("CreateEvalRun() failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify all runs were stored
	stats := storage.GetStats()
	expected := numGoroutines * runsPerGoroutine
	if stats["runs"] != expected {
		t.Errorf("runs = %d, want %d", stats["runs"], expected)
	}
}

func TestMemoryStorage_Close(t *testing.T) {
	storage := NewMemoryStorage(100)

	// Close storage
	if err := storage.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Verify operations fail after close
	ctx := context.Background()
	eval := &Eval{ID: "eval-1", Name: "Test", Suite: "test", Status: "running"}
	if err := storage.CreateEval(ctx, eval); err == nil {
		t.Error("CreateEval() should fail after Close()")
	}

	run := &EvalRun{ID: "run-1", EvalID: "eval-1"}
	if err := storage.CreateEvalRun(ctx, run); err == nil {
		t.Error("CreateEvalRun() should fail after Close()")
	}
}
