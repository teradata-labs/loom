// Copyright 2026 Teradata

package evals

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concurrentOpenBusyTimeoutMS is the busy timeout the concurrent-open test
// gives each store. NewStore waits up to storeBusyTimeoutMS (5 s). The slow
// Windows runner in issue #604 used up that budget. A short budget on a fast
// disk puts the same pressure on schema init, so the test fails in seconds
// when initSchema takes the write lock more than once.
const concurrentOpenBusyTimeoutMS = 100

// TestOpenStore_ExistingFile covers opens of a results file that already
// holds the schema. `looms eval list` and `looms eval show --store` read such
// files, and `looms serve` opens the store on the shared main database. None
// of them may need the write lock just to open.
func TestOpenStore_ExistingFile(t *testing.T) {
	t.Parallel()

	newFile := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "results.db")
		store, err := NewStore(path)
		require.NoError(t, err, "create the store")
		_, err = store.Save(context.Background(), CreateMockResult("ro-suite", "agent1", true))
		require.NoError(t, err, "save one result")
		require.NoError(t, store.Close(), "close the writer")
		return path
	}

	t.Run("read-only file opens and reads", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "windows" && os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		path := newFile(t)
		require.NoError(t, os.Chmod(path, 0o444), "make the results file read-only")
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		store, err := NewStore(path)
		require.NoError(t, err, "a read-only results file must open")
		t.Cleanup(func() { _ = store.Close() })
		results, err := store.ListBySuite(context.Background(), "ro-suite", 10)
		require.NoError(t, err, "list the saved results")
		assert.Len(t, results, 1, "the saved result must be readable")
	})

	t.Run("opens while another connection writes", func(t *testing.T) {
		t.Parallel()
		path := newFile(t)
		writer, err := openStore(path, storeBusyTimeoutMS)
		require.NoError(t, err, "open the writer")
		t.Cleanup(func() { _ = writer.Close() })
		conn, err := writer.db.Conn(context.Background())
		require.NoError(t, err, "get the writer's connection")
		t.Cleanup(func() { _ = conn.Close() })
		_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		require.NoError(t, err, "the writer takes the write lock")
		t.Cleanup(func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") })

		start := time.Now()
		store, err := openStore(path, concurrentOpenBusyTimeoutMS)
		require.NoError(t, err, "the store must open while another connection holds the write lock")
		t.Cleanup(func() { _ = store.Close() })
		assert.Less(t, time.Since(start), time.Duration(concurrentOpenBusyTimeoutMS)*time.Millisecond,
			"the open must not wait for the write lock")
	})
}

// TestOpenStore_ConcurrentOpenSameFile reproduces issue #604. Many callers
// open one new database file at the same moment, so every opener runs
// initSchema against the same file under lock contention. Each one must
// succeed, and the schema must exist exactly once afterwards.
// A barrier holds every goroutine until all are ready, and each round uses a
// new file, so the schema is created under contention in every round.
func TestOpenStore_ConcurrentOpenSameFile(t *testing.T) {
	const (
		rounds  = 10
		openers = 64
	)
	for round := 0; round < rounds; round++ {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("open-%d.db", round))

		start := make(chan struct{})
		var ready, done sync.WaitGroup
		ready.Add(openers)
		done.Add(openers)
		errs := make([]error, openers)
		for i := 0; i < openers; i++ {
			go func(i int) {
				defer done.Done()
				ready.Done()
				<-start
				store, err := openStore(path, concurrentOpenBusyTimeoutMS)
				if err != nil {
					errs[i] = err
					return
				}
				errs[i] = store.Close()
			}(i)
		}
		ready.Wait()
		close(start)
		done.Wait()

		for i, err := range errs {
			assert.NoError(t, err, "round %d: opener %d must open and close the store", round, i)
		}

		store, err := NewStore(path)
		require.NoError(t, err, "round %d: reopen after the concurrent opens", round)
		var tables int
		require.NoError(t, store.db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN "+
				"('eval_results','test_case_results','ab_test_results','ab_test_responses')").Scan(&tables),
			"round %d: count schema tables", round)
		assert.Equal(t, 4, tables, "round %d: every schema table must exist once", round)
		require.NoError(t, store.Close(), "round %d: close the reopened store", round)
	}
}
