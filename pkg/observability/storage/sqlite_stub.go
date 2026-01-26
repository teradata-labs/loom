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

//go:build !fts5

package storage

import (
	"context"
	"fmt"
)

// NewSQLiteStorage returns an error when built without fts5 support
func NewSQLiteStorage(dbPath string) (Storage, error) {
	return nil, fmt.Errorf("SQLite storage requires build with -tags fts5")
}

// SQLiteStorage is a stub type when built without fts5
type SQLiteStorage struct{}

// These stub methods ensure SQLiteStorage implements Storage interface
func (s *SQLiteStorage) CreateEval(ctx context.Context, eval *Eval) error {
	return fmt.Errorf("SQLite storage requires build with -tags fts5")
}

func (s *SQLiteStorage) UpdateEvalStatus(ctx context.Context, evalID string, status string) error {
	return fmt.Errorf("SQLite storage requires build with -tags fts5")
}

func (s *SQLiteStorage) CreateEvalRun(ctx context.Context, run *EvalRun) error {
	return fmt.Errorf("SQLite storage requires build with -tags fts5")
}

func (s *SQLiteStorage) CalculateEvalMetrics(ctx context.Context, evalID string) (*EvalMetrics, error) {
	return nil, fmt.Errorf("SQLite storage requires build with -tags fts5")
}

func (s *SQLiteStorage) UpsertEvalMetrics(ctx context.Context, metrics *EvalMetrics) error {
	return fmt.Errorf("SQLite storage requires build with -tags fts5")
}

func (s *SQLiteStorage) Close() error {
	return nil
}

// Compile-time interface check
var _ Storage = (*SQLiteStorage)(nil)
