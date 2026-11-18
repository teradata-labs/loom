// Copyright 2026 Teradata

package sqlitedriver

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestDB opens path with the given busy timeout and one pooled
// connection, so a test can see whether that connection was left inside a
// transaction.
func openTestDB(t *testing.T, path string, busyTimeoutMS int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", DSN(path, Options{BusyTimeoutMS: busyTimeoutMS}))
	require.NoError(t, err, "open test database %s", path)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n),
		"count table %s", name)
	return n == 1
}

// holdWriteLock takes the write lock on path from a separate connection (as
// another process would) and returns the function that releases it.
func holdWriteLock(t *testing.T, path string) func() {
	t.Helper()
	other := openTestDB(t, path, 5000)
	conn, err := other.Conn(context.Background())
	require.NoError(t, err, "get a connection for the lock holder")
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err, "the lock holder takes the write lock")
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func TestExecSchema(t *testing.T) {
	t.Parallel()

	t.Run("commits the whole script", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "commit.db")
		db := openTestDB(t, path, 5000)
		err := ExecSchema(context.Background(), db, path,
			`CREATE TABLE IF NOT EXISTS a (id INTEGER); CREATE INDEX IF NOT EXISTS a_id ON a(id);`)
		require.NoError(t, err, "script must apply")
		assert.True(t, tableExists(t, db, "a"), "table a must exist after commit")

		err = ExecSchema(context.Background(), db, path, `CREATE TABLE IF NOT EXISTS a (id INTEGER);`)
		assert.NoError(t, err, "running the same IF NOT EXISTS script again must succeed")
	})

	t.Run("a failed statement rolls back the earlier ones", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rollback.db")
		db := openTestDB(t, path, 5000)
		err := ExecSchema(context.Background(), db, path,
			`CREATE TABLE partial (id INTEGER); THIS IS NOT SQL;`)
		require.Error(t, err, "a bad statement must fail the script")
		assert.Contains(t, err.Error(), "exec script", "the error must name the failed step")
		assert.False(t, tableExists(t, db, "partial"), "the table created before the failure must be rolled back")

		// The pool has one connection. If the failure left it inside a
		// transaction, this BEGIN would fail.
		err = ExecSchema(context.Background(), db, path, `CREATE TABLE after_failure (id INTEGER);`)
		require.NoError(t, err, "the connection must be usable after a rollback")
		assert.True(t, tableExists(t, db, "after_failure"), "the next script must commit")
	})

	t.Run("a cancelled context runs nothing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "cancel.db")
		db := openTestDB(t, path, 5000)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := ExecSchema(ctx, db, path, `CREATE TABLE never (id INTEGER);`)
		assert.ErrorIs(t, err, context.Canceled, "a cancelled ctx must stop before any statement")
		assert.False(t, tableExists(t, db, "never"), "nothing may run under a cancelled ctx")
	})
}

// An existing schema needs no write: the script must not wait for, or fail
// on, a write lock that another connection holds.
func TestExecSchema_ExistingSchemaTakesNoWriteLock(t *testing.T) {
	t.Parallel()
	const script = `CREATE TABLE IF NOT EXISTS a (id INTEGER); CREATE INDEX IF NOT EXISTS a_id ON a(id);`
	path := filepath.Join(t.TempDir(), "held.db")
	db := openTestDB(t, path, 100)
	require.NoError(t, ExecSchema(context.Background(), db, path, script), "create the schema")

	holdWriteLock(t, path)

	start := time.Now()
	err := ExecSchema(context.Background(), db, path, script)
	elapsed := time.Since(start)
	require.NoError(t, err, "an existing schema must open while another connection holds the write lock")
	assert.Less(t, elapsed, 100*time.Millisecond, "the open must not wait for the busy timeout")
}

// A read-only database with the schema in place must open.
func TestExecSchema_ReadOnlyDatabaseWithSchema(t *testing.T) {
	t.Parallel()
	const script = `CREATE TABLE IF NOT EXISTS a (id INTEGER); CREATE INDEX IF NOT EXISTS a_id ON a(id);`

	t.Run("mode=ro URI", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "ro-uri.db")
		rw := openTestDB(t, path, 5000)
		require.NoError(t, ExecSchema(context.Background(), rw, path, script), "create the schema")
		require.NoError(t, rw.Close(), "close the writer")

		uri := "file:" + filepath.ToSlash(path) + "?mode=ro"
		ro := openTestDB(t, uri, 5000)
		assert.NoError(t, ExecSchema(context.Background(), ro, uri, script),
			"a read-only open of an existing schema must succeed")
	})

	t.Run("file without write permission", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "windows" && os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		path := filepath.Join(t.TempDir(), "ro-file.db")
		rw := openTestDB(t, path, 5000)
		require.NoError(t, ExecSchema(context.Background(), rw, path, script), "create the schema")
		require.NoError(t, rw.Close(), "close the writer")
		require.NoError(t, os.Chmod(path, 0o444), "make the file read-only")
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		ro := openTestDB(t, path, 5000)
		assert.NoError(t, ExecSchema(context.Background(), ro, path, script),
			"an existing schema in a read-only file must open")
	})
}

// A script that reads first (a table that exists) and then must write (a
// table that does not) gets SQLITE_BUSY at once in a deferred transaction
// while another connection holds the write lock. ExecSchema must retry
// under BEGIN IMMEDIATE, wait for the lock, and apply the script.
func TestExecSchema_RetriesUnderImmediateWhenTheUpgradeIsBusy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db := openTestDB(t, path, 5000)
	require.NoError(t, ExecSchema(context.Background(), db, path, `CREATE TABLE a (id INTEGER);`), "create table a")

	release := holdWriteLock(t, path)
	const hold = 300 * time.Millisecond
	timer := time.AfterFunc(hold, release)
	t.Cleanup(func() { timer.Stop() })

	start := time.Now()
	err := ExecSchema(context.Background(), db, path,
		`CREATE TABLE IF NOT EXISTS a (id INTEGER); CREATE TABLE IF NOT EXISTS b (id INTEGER);`)
	elapsed := time.Since(start)
	require.NoError(t, err, "the script must wait for the write lock and apply")
	assert.True(t, tableExists(t, db, "b"), "table b must exist after the retry")
	assert.GreaterOrEqual(t, elapsed, hold-50*time.Millisecond, "the retry must have waited for the holder")
}

func TestLockSchemaPath_SerializesOnePath(t *testing.T) {
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
	memNamed, err := filepath.Abs("mode=memory.db")
	require.NoError(t, err, "resolve mode=memory.db")

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
		{name: "mode=memory in the file name is a file", path: "mode=memory.db", wantKey: memNamed, wantOK: true},
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
	for _, seed := range []string{"", ":memory:", "x.db", "file:x.db?cache=shared", "?", "file:", "a/../b.db", "x?mode=memory"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		key, ok := schemaLockKey(path)
		if ok && !filepath.IsAbs(key) {
			t.Fatalf("schemaLockKey(%q) = %q, want an absolute path", path, key)
		}
	})
}
