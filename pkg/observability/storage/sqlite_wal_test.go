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
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLiteStorageJournalModeIsWAL is the regression test for issue #370:
// this store used to open in SQLite's default rollback-journal mode, where
// every writer takes an EXCLUSIVE lock on the whole file and readers block
// writers, so its 10 pooled connections serialised and exhausted their
// busy_timeout under fleet load.
//
// journal_mode must be WAL on EVERY pooled connection, not just the one that
// happened to run an Exec — the same defect PRAGMA busy_timeout had before
// internal/sqlitedriver.DSN existed (see internal/sqlitedriver/dsn_test.go).
// Connections are pinned with db.Conn and held so the pool is forced to open
// fresh ones rather than hand back the same one.
func TestSQLiteStorageJournalModeIsWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "obs.db")

	storage, err := NewSQLiteStorage(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	const pinned = 6 // fewer than SetMaxOpenConns(10)
	conns := make([]*sql.Conn, 0, pinned)
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	for i := 0; i < pinned; i++ {
		c, err := storage.db.Conn(context.Background())
		require.NoError(t, err, "pin connection %d", i)
		conns = append(conns, c)
	}
	require.Equal(t, pinned, storage.db.Stats().OpenConnections,
		"connections must be distinct for this test to prove anything")

	for i, c := range conns {
		var journalMode string
		require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode))
		assert.Equal(t, "wal", strings.ToLower(journalMode),
			"conn %d: journal_mode must be WAL on every pooled connection", i)

		var busyTimeout int
		require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout))
		assert.Equal(t, 5000, busyTimeout,
			"conn %d: busy_timeout must be 5000 on every pooled connection", i)
	}
}

// isLockError reports whether err is SQLite lock contention (SQLITE_BUSY /
// SQLITE_LOCKED) or a write that ran out of time waiting for the lock.
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"database is locked", "database table is locked", "sqlite_busy", "busy", "deadline exceeded"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// seedContentionRows bulk-inserts n eval runs in one transaction so the
// concurrency test starts from a table with real history in it rather than an
// empty one.
func seedContentionRows(t *testing.T, s *SQLiteStorage, evalID string, n int) {
	t.Helper()
	tx, err := s.db.Begin()
	require.NoError(t, err)
	stmt, err := tx.Prepare(`INSERT INTO eval_runs (
		id, eval_id, query, model, configuration_json, response,
		execution_time_ms, token_count, success, error_message,
		session_id, timestamp
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		_, err := stmt.Exec(fmt.Sprintf("seed-%d", i), evalID, "seed query", "seed-model",
			"{}", "seed response", int64(i), int32(i%1000), 1, "", "seed-session", time.Now().Unix())
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
}

// holdingRead runs an aggregate over eval_runs inside a read transaction and
// keeps that transaction open until ctx is cancelled, modelling a long
// CalculateEvalMetrics scan over a fleet-sized table (or an operator's
// sqlite3 session) running while spans are being written.
//
// Holding the read is what makes this test deterministic rather than a race
// against the local disk. A long-running read is the invariant difference
// between the two journal modes: under a rollback journal the reader holds
// SHARED for the whole transaction and no writer can take the EXCLUSIVE lock
// it needs to commit until every SHARED is released, so writers burn their
// busy_timeout no matter how fast the hardware is. Under WAL the reader works
// from a snapshot and never blocks the writer, no matter how slow the read is.
func holdingRead(ctx context.Context, s *SQLiteStorage, evalID string, up chan<- struct{}) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The read lock is taken by the first statement, not by BEGIN (SQLite
	// transactions are deferred).
	var runs int
	var tokens int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(token_count), 0) FROM eval_runs WHERE eval_id = ?",
		evalID).Scan(&runs, &tokens); err != nil {
		return err
	}

	up <- struct{}{}
	<-ctx.Done()
	return nil
}

// TestSQLiteConcurrentWriters is the contention regression for issue #370.
//
// It reproduces the shape of the fleet workload that dropped ~1,486 spans in
// a single 512-agent run: many goroutines writing eval runs (EndSpan ->
// CreateEvalRun is the hot path) while readers aggregate over the same table
// (CalculateEvalMetrics does a full scan). In rollback-journal mode the
// readers block the writers and the writers block each other on the
// whole-file lock, so writes exhaust the 5s busy_timeout and are lost. Under
// WAL readers never block the writer, so every write must land inside the
// budget with no lock errors at all.
//
// Measured on this test with the store's pool unchanged (issue #370):
//   - rollback journal (pre-fix): 480/480 writes failed with "database is
//     locked", 0 rows landed, burst took 10.4s.
//   - WAL (post-fix): 0 lock errors, 480/480 rows landed, burst took 112ms.
func TestSQLiteConcurrentWriters(t *testing.T) {
	const (
		writers   = 8
		perWriter = 60
		readers   = 2
		seedRows  = 5000

		// The burst must finish well inside this. It is deliberately shorter
		// than writeDeadline so a writer that ends up waiting on the store's
		// 5s busy_timeout shows up as a lock error, not as a hang.
		totalBudget   = 5 * time.Second
		writeDeadline = 8 * time.Second
	)

	dbPath := filepath.Join(t.TempDir(), "contention.db")
	storage, err := NewSQLiteStorage(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	const evalID = "eval-contention"
	require.NoError(t, storage.CreateEval(context.Background(), &Eval{
		ID:     evalID,
		Name:   "contention",
		Suite:  "regression",
		Status: "running",
	}))

	// Seed history so the readers are aggregating over real rows.
	seedContentionRows(t, storage, evalID, seedRows)

	ctx, cancel := context.WithTimeout(context.Background(), writeDeadline)
	defer cancel()

	// Readers aggregate over eval_runs and hold the read open for as long as
	// the writers are working. Under a rollback journal these are what starve
	// the writers; under WAL they are invisible to them.
	readerCtx, stopReaders := context.WithCancel(context.Background())
	defer stopReaders()
	var readerWG sync.WaitGroup
	var readErrs atomic.Int64
	readersUp := make(chan struct{}, readers)
	for r := 0; r < readers; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			if err := holdingRead(readerCtx, storage, evalID, readersUp); err != nil {
				readErrs.Add(1)
			}
		}()
	}
	// Do not start writing until the readers actually hold their read locks,
	// or the burst can finish before contention exists.
	for r := 0; r < readers; r++ {
		select {
		case <-readersUp:
		case <-time.After(totalBudget):
			t.Fatal("reader did not acquire its read transaction")
		}
	}

	start := time.Now()

	var writerWG sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(writer int) {
			defer writerWG.Done()
			for seq := 0; seq < perWriter; seq++ {
				run := &EvalRun{
					ID:                fmt.Sprintf("span-%d-%d", writer, seq),
					EvalID:            evalID,
					Query:             "select 1",
					Model:             "test-model",
					ConfigurationJSON: `{"writer":true}`,
					Response:          "ok",
					ExecutionTimeMS:   int64(seq),
					TokenCount:        int32(seq),
					Success:           true,
					SessionID:         fmt.Sprintf("session-%d", writer),
					Timestamp:         time.Now().Unix(),
				}
				if err := storage.CreateEvalRun(ctx, run); err != nil {
					errCh <- fmt.Errorf("writer %d seq %d: %w", writer, seq, err)
				}
			}
		}(w)
	}
	writerWG.Wait()
	elapsed := time.Since(start)

	stopReaders()
	readerWG.Wait()
	close(errCh)

	var lockErrs, otherErrs int
	for err := range errCh {
		if isLockError(err) {
			lockErrs++
			if lockErrs <= 3 {
				t.Logf("lock error: %v", err)
			}
			continue
		}
		otherErrs++
		t.Logf("write error: %v", err)
	}

	assert.Zero(t, lockErrs, "SQLITE_BUSY / lock-timeout writes must not happen under WAL (issue #370)")
	assert.Zero(t, otherErrs, "no write may fail")
	assert.Zero(t, readErrs.Load(), "concurrent readers must not fail either")

	var stored int
	require.NoError(t, storage.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM eval_runs WHERE eval_id = ? AND id LIKE 'span-%'", evalID).Scan(&stored))
	assert.Equal(t, writers*perWriter, stored, "every eval run must land")

	assert.Less(t, elapsed, totalBudget,
		"writer burst must finish inside the budget (took %s)", elapsed)
	t.Logf("%d writers x %d runs + %d readers: %d rows in %s", writers, perWriter, readers, stored, elapsed)
}
