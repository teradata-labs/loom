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

//go:build fts5

package storage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewSQLiteStorage(t *testing.T) {
	tests := []struct {
		name    string
		dbPath  string
		wantErr bool
	}{
		{
			name:    "valid path",
			dbPath:  filepath.Join(t.TempDir(), "test.db"),
			wantErr: false,
		},
		{
			name:    "empty path",
			dbPath:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage, err := NewSQLiteStorage(tt.dbPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSQLiteStorage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if storage != nil {
				defer storage.Close()
			}

			if !tt.wantErr {
				// Verify database file was created
				if _, err := os.Stat(tt.dbPath); os.IsNotExist(err) {
					t.Errorf("database file was not created at %s", tt.dbPath)
				}
			}
		})
	}
}

func TestSQLiteStorage_CreateEval(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

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
		{
			name: "duplicate ID",
			eval: &Eval{
				ID:     "eval-1", // Same as first test
				Name:   "Duplicate",
				Suite:  "test",
				Status: "running",
			},
			wantErr: true, // Should fail on unique constraint
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := storage.CreateEval(ctx, tt.eval)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateEval() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && tt.eval != nil {
				// Verify eval was stored
				var count int
				err := storage.db.QueryRow("SELECT COUNT(*) FROM evals WHERE id = ?", tt.eval.ID).Scan(&count)
				if err != nil {
					t.Fatalf("failed to query eval: %v", err)
				}
				if count != 1 {
					t.Errorf("eval count = %d, want 1", count)
				}
			}
		})
	}
}

func TestSQLiteStorage_UpdateEvalStatus(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

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
				// Verify status was updated
				var status string
				err := storage.db.QueryRow("SELECT status FROM evals WHERE id = ?", tt.evalID).Scan(&status)
				if err != nil {
					t.Fatalf("failed to query status: %v", err)
				}
				if status != tt.status {
					t.Errorf("status = %q, want %q", status, tt.status)
				}
			}
		})
	}
}

func TestSQLiteStorage_CreateEvalRun(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create an eval first
	eval := &Eval{ID: "eval-1", Name: "Test", Suite: "test", Status: "running"}
	if err := storage.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

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
				ID: "run-2",
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
				// Verify run was stored
				var count int
				err := storage.db.QueryRow("SELECT COUNT(*) FROM eval_runs WHERE id = ?", tt.run.ID).Scan(&count)
				if err != nil {
					t.Fatalf("failed to query run: %v", err)
				}
				if count != 1 {
					t.Errorf("run count = %d, want 1", count)
				}
			}
		})
	}
}

func TestSQLiteStorage_CalculateEvalMetrics(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()
	evalID := "eval-1"

	// Create eval
	eval := &Eval{ID: evalID, Name: "Test", Suite: "test", Status: "running"}
	if err := storage.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

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

func TestSQLiteStorage_UpsertEvalMetrics(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create eval first
	eval := &Eval{ID: "eval-1", Name: "Test", Suite: "test", Status: "running"}
	if err := storage.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

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
	var totalRuns int32
	err := storage.db.QueryRow("SELECT total_runs FROM eval_metrics WHERE eval_id = ?", metrics.EvalID).Scan(&totalRuns)
	if err != nil {
		t.Fatalf("failed to query metrics: %v", err)
	}
	if totalRuns != 10 {
		t.Errorf("TotalRuns = %d, want 10", totalRuns)
	}

	// Update
	metrics.TotalRuns = 20
	if err := storage.UpsertEvalMetrics(ctx, metrics); err != nil {
		t.Fatalf("UpsertEvalMetrics() update failed: %v", err)
	}

	// Verify update
	err = storage.db.QueryRow("SELECT total_runs FROM eval_metrics WHERE eval_id = ?", metrics.EvalID).Scan(&totalRuns)
	if err != nil {
		t.Fatalf("failed to query updated metrics: %v", err)
	}
	if totalRuns != 20 {
		t.Errorf("TotalRuns = %d, want 20", totalRuns)
	}
}

func TestSQLiteStorage_Persistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.db")

	// Create storage and add data
	storage1, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() failed: %v", err)
	}

	ctx := context.Background()
	eval := &Eval{ID: "eval-1", Name: "Test", Suite: "test", Status: "running"}
	if err := storage1.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

	storage1.Close()

	// Reopen database and verify data persists
	storage2, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() reopen failed: %v", err)
	}
	defer storage2.Close()

	var count int
	err = storage2.db.QueryRow("SELECT COUNT(*) FROM evals WHERE id = ?", eval.ID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query persisted eval: %v", err)
	}
	if count != 1 {
		t.Errorf("persisted eval count = %d, want 1", count)
	}
}

func TestSQLiteStorage_Concurrent(t *testing.T) {
	storage, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create eval first
	eval := &Eval{ID: "eval-concurrent", Name: "Concurrent Test", Suite: "test", Status: "running"}
	if err := storage.CreateEval(ctx, eval); err != nil {
		t.Fatalf("CreateEval() failed: %v", err)
	}

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
	var count int
	err := storage.db.QueryRow("SELECT COUNT(*) FROM eval_runs WHERE eval_id = ?", "eval-concurrent").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count runs: %v", err)
	}

	expected := numGoroutines * runsPerGoroutine
	if count != expected {
		t.Errorf("run count = %d, want %d", count, expected)
	}
}

func TestSQLiteStorage_Close(t *testing.T) {
	storage, _ := setupSQLiteTest(t)

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
}

// setupSQLiteTest creates a temporary SQLite storage for testing
func setupSQLiteTest(t *testing.T) (*SQLiteStorage, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	storage, err := NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() failed: %v", err)
	}

	cleanup := func() {
		storage.Close()
	}

	return storage, cleanup
}
