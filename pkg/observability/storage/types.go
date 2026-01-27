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
// This package implements self-contained memory and SQLite storage backends
// for storing traces, metrics, and evaluation data without external dependencies.
package storage

// Eval represents an evaluation session (grouping of related traces)
type Eval struct {
	ID        string // Unique evaluation ID
	Name      string // Human-readable name
	Suite     string // Suite or category name
	Status    string // Status: "running", "completed", "failed"
	CreatedAt int64  // Unix timestamp
	UpdatedAt int64  // Unix timestamp
}

// EvalRun represents a single trace/span within an evaluation
type EvalRun struct {
	ID                string // Unique span ID
	EvalID            string // Evaluation this run belongs to
	Query             string // Input query/prompt
	Model             string // LLM model used
	ConfigurationJSON string // JSON-encoded configuration/attributes
	Response          string // Output response
	ExecutionTimeMS   int64  // Execution duration in milliseconds
	TokenCount        int32  // Total tokens used
	Success           bool   // Whether execution succeeded
	ErrorMessage      string // Error message if failed
	SessionID         string // Session ID for grouping
	Timestamp         int64  // Unix timestamp
}

// EvalMetrics represents aggregated metrics for an evaluation
type EvalMetrics struct {
	EvalID             string  // Evaluation ID
	TotalRuns          int32   // Total number of runs
	SuccessfulRuns     int32   // Number of successful runs
	FailedRuns         int32   // Number of failed runs
	SuccessRate        float64 // Success rate (0.0-1.0)
	AvgExecutionTimeMS float64 // Average execution time
	TotalTokens        int64   // Total tokens used
	AvgTokensPerRun    float64 // Average tokens per run
	TotalCost          float64 // Total estimated cost (if available)
	FirstRunTimestamp  int64   // Timestamp of first run
	LastRunTimestamp   int64   // Timestamp of last run
	UpdatedAt          int64   // When metrics were last updated
}
