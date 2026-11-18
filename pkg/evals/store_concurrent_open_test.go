// Copyright 2026 Teradata

package evals

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concurrentOpenBusyTimeoutMS is the busy timeout the concurrent-open test
// gives each store. NewStore waits up to storeBusyTimeoutMS (5 s). The slow
// Windows runner in issue #604 used up that budget. A short budget on a fast
// disk puts the same pressure on schema init, so the test fails in seconds
// when initSchema takes the write lock more than once.
const concurrentOpenBusyTimeoutMS = 100

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
