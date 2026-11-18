// Copyright 2026 Teradata

package sqlitedriver

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestDB opens a file database with a busy timeout and one pooled
// connection, so a test can see whether that connection was left inside a
// transaction.
func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.db")
	db, err := sql.Open("sqlite3", DSN(path, Options{BusyTimeoutMS: 5000}))
	require.NoError(t, err, "open test database")
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n),
		"count table %s", name)
	return n == 1
}

func TestExecImmediate(t *testing.T) {
	t.Parallel()

	t.Run("commits the whole script", func(t *testing.T) {
		t.Parallel()
		db, _ := openTestDB(t)
		err := ExecImmediate(context.Background(), db,
			`CREATE TABLE IF NOT EXISTS a (id INTEGER); CREATE INDEX IF NOT EXISTS a_id ON a(id);`)
		require.NoError(t, err, "script must apply")
		assert.True(t, tableExists(t, db, "a"), "table a must exist after commit")

		err = ExecImmediate(context.Background(), db, `CREATE TABLE IF NOT EXISTS a (id INTEGER);`)
		assert.NoError(t, err, "running the same IF NOT EXISTS script again must succeed")
	})

	t.Run("a failed statement rolls back the earlier ones", func(t *testing.T) {
		t.Parallel()
		db, _ := openTestDB(t)
		err := ExecImmediate(context.Background(), db,
			`CREATE TABLE partial (id INTEGER); THIS IS NOT SQL;`)
		require.Error(t, err, "a bad statement must fail the script")
		assert.Contains(t, err.Error(), "exec script", "the error must name the failed step")
		assert.False(t, tableExists(t, db, "partial"), "the table created before the failure must be rolled back")

		// The pool has one connection. If the failure left it inside a
		// transaction, this BEGIN IMMEDIATE would fail.
		err = ExecImmediate(context.Background(), db, `CREATE TABLE after_failure (id INTEGER);`)
		require.NoError(t, err, "the connection must be usable after a rollback")
		assert.True(t, tableExists(t, db, "after_failure"), "the next script must commit")
	})

	t.Run("a cancelled context runs nothing", func(t *testing.T) {
		t.Parallel()
		db, _ := openTestDB(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := ExecImmediate(ctx, db, `CREATE TABLE never (id INTEGER);`)
		assert.ErrorIs(t, err, context.Canceled, "a cancelled ctx must stop before BEGIN")
		assert.False(t, tableExists(t, db, "never"), "nothing may run under a cancelled ctx")
	})
}

func TestExecSchema_SerializesOneFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "serial.db")
	const callers = 16

	var inside, maxInside atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			unlock, err := lockSchemaPath(context.Background(), path)
			if err != nil {
				errs[i] = err
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inside.Add(-1)
			unlock()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "caller %d must get the lock", i)
	}
	assert.Equal(t, int32(1), maxInside.Load(), "only one caller may hold the lock for a path at a time")

	schemaLocksMu.Lock()
	key, _ := schemaLockKey(path)
	_, left := schemaLocks[key]
	schemaLocksMu.Unlock()
	assert.False(t, left, "the lock entry must be removed when the last caller releases it")
}

func TestLockSchemaPath_StopsWaitingWhenCtxEnds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wait.db")
	unlock, err := lockSchemaPath(context.Background(), path)
	require.NoError(t, err, "first caller gets the lock")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = lockSchemaPath(ctx, path)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "a waiter must return ctx.Err when ctx ends, got %v", err)

	unlock()
	unlock2, err := lockSchemaPath(context.Background(), path)
	require.NoError(t, err, "the lock must be free after the holder releases it")
	unlock2()

	schemaLocksMu.Lock()
	key, _ := schemaLockKey(path)
	_, left := schemaLocks[key]
	schemaLocksMu.Unlock()
	assert.False(t, left, "a waiter that gave up must not leave an entry behind")
}

func TestSchemaLockKey(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs("x.db")
	require.NoError(t, err, "resolve x.db")

	tests := []struct {
		name    string
		path    string
		wantKey string
		wantOK  bool
	}{
		{name: "empty is private", path: "", wantOK: false},
		{name: "memory is private", path: ":memory:", wantOK: false},
		{name: "memory URI is private", path: "file:x?mode=memory&cache=shared", wantOK: false},
		{name: "file URI memory is private", path: "file::memory:", wantOK: false},
		{name: "relative path is made absolute", path: "x.db", wantKey: abs, wantOK: true},
		{name: "dot prefix shares the key", path: "./x.db", wantKey: abs, wantOK: true},
		{name: "query string is dropped", path: "x.db?_busy_timeout=5000", wantKey: abs, wantOK: true},
		{name: "file URI prefix is dropped", path: "file:x.db?cache=shared", wantKey: abs, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			key, ok := schemaLockKey(tc.path)
			assert.Equal(t, tc.wantOK, ok, "lockable flag for %q", tc.path)
			if tc.wantOK {
				assert.Equal(t, tc.wantKey, key, "lock key for %q", tc.path)
			}
		})
	}
}

// FuzzSchemaLockKey checks that any DSN path maps to a key without a panic,
// and that a lockable key is always an absolute path.
func FuzzSchemaLockKey(f *testing.F) {
	for _, seed := range []string{"", ":memory:", "x.db", "file:x.db?cache=shared", "?", "file:", "a/../b.db"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		key, ok := schemaLockKey(path)
		if ok && !filepath.IsAbs(key) {
			t.Fatalf("schemaLockKey(%q) = %q, want an absolute path", path, key)
		}
	})
}
