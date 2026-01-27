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

// Package storage provides in-process trace storage for embedded observability.
//
// This package implements self-contained memory and SQLite storage backends
// for storing traces, metrics, and evaluation data. It replaces the external
// Hawk dependency with lightweight, self-contained implementations.
//
// Storage Backends:
//
// Memory Storage:
//   - Ring buffer with configurable size (default: 10,000 traces)
//   - Thread-safe with RWMutex
//   - Automatic FIFO eviction when full
//   - Fast lookups by trace ID
//   - Suitable for development, testing, and short-lived processes
//
// SQLite Storage:
//   - Persistent storage with indexed queries
//   - Connection pooling for concurrent access
//   - Requires -tags fts5 build flag
//   - Suitable for production use and long-term storage
//
// Usage Example:
//
//	// Create memory storage
//	storage := storage.NewMemoryStorage(10000)
//	defer storage.Close()
//
//	// Or create SQLite storage (requires -tags fts5)
//	storage, err := storage.NewSQLiteStorage("/path/to/traces.db")
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer storage.Close()
//
//	// Store an evaluation
//	eval := &storage.Eval{
//		ID:     "eval-123",
//		Name:   "My Evaluation",
//		Suite:  "test-suite",
//		Status: "running",
//	}
//	err = storage.CreateEval(ctx, eval)
//
//	// Store a trace/span
//	run := &storage.EvalRun{
//		ID:              "run-456",
//		EvalID:          "eval-123",
//		Query:           "What is 2+2?",
//		Model:           "claude-sonnet-4",
//		Response:        "4",
//		ExecutionTimeMS: 150,
//		TokenCount:      25,
//		Success:         true,
//	}
//	err = storage.CreateEvalRun(ctx, run)
//
//	// Calculate and store metrics
//	metrics, err := storage.CalculateEvalMetrics(ctx, "eval-123")
//	err = storage.UpsertEvalMetrics(ctx, metrics)
package storage
