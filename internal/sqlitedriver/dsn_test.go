package sqlitedriver

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDSNRendering checks the driver-agnostic properties of DSN: identity
// with zero options, existing query parameters preserved, and the timeout
// value present when requested.
func TestDSNRendering(t *testing.T) {
	t.Run("zero options is identity", func(t *testing.T) {
		assert.Equal(t, "/tmp/x.db", DSN("/tmp/x.db", Options{}))
		assert.Equal(t, ":memory:", DSN(":memory:", Options{}))
	})

	t.Run("empty path stays empty", func(t *testing.T) {
		// "" means a private temporary database to SQLite; appending params
		// would create a literal file named "?_busy_timeout=..." in the cwd.
		assert.Equal(t, "", DSN("", Options{BusyTimeoutMS: 5000, WAL: true, ForeignKeys: true}))
	})

	t.Run("existing query params preserved", func(t *testing.T) {
		got := DSN("file:/tmp/x.db?cache=shared&mode=rwc", Options{BusyTimeoutMS: 5000, WAL: true})
		assert.True(t, strings.HasPrefix(got, "file:/tmp/x.db?cache=shared&mode=rwc&"),
			"caller params must survive: %s", got)
		assert.Contains(t, got, "busy_timeout")
		assert.NotContains(t, got[len("file:/tmp/x.db"):], "?cache=shared?", "must not add a second '?'")
	})

	t.Run("plain path gains query separator", func(t *testing.T) {
		got := DSN("/tmp/x.db", Options{BusyTimeoutMS: 5000})
		assert.True(t, strings.HasPrefix(got, "/tmp/x.db?"), got)
		assert.Contains(t, got, "5000")
	})
}

// pinConns acquires n distinct physical connections from db. Holding every
// *sql.Conn while acquiring the next forces the pool to open fresh
// connections rather than reuse one.
func pinConns(t *testing.T, db *sql.DB, n int) []*sql.Conn {
	t.Helper()
	conns := make([]*sql.Conn, n)
	for i := range conns {
		c, err := db.Conn(context.Background())
		require.NoError(t, err, "pin connection %d", i)
		conns[i] = c
	}
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return conns
}

// TestDSNBusyTimeoutOnEveryPooledConn is the regression test for the fleet
// SQLITE_BUSY incident: PRAGMA busy_timeout set via db.Exec on a pooled
// *sql.DB configures only the one connection that runs the statement; every
// other pooled connection keeps the driver default and fails instantly under
// write contention on nocgo builds (modernc's default is 0; the CGO mattn
// fork defaults to 5000, which masked the bug in CGO builds). The tests use
// a non-default value (7500) so the Exec-once approach demonstrably fails
// under BOTH drivers, while the DSN approach configures EVERY connection.
func TestDSNBusyTimeoutOnEveryPooledConn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool.db")

	t.Run("old Exec-once approach leaves pooled connections unconfigured", func(t *testing.T) {
		db, err := sql.Open("sqlite3", dbPath)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)

		// The buggy pattern this fix removes from production code.
		_, err = db.Exec("PRAGMA busy_timeout = 7500")
		require.NoError(t, err)

		configured := 0
		for i, c := range pinConns(t, db, 4) {
			var v int
			require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&v))
			t.Logf("exec-once: conn %d busy_timeout=%d", i, v)
			if v == 7500 {
				configured++
			}
		}
		assert.Less(t, configured, 4,
			"db.Exec(PRAGMA busy_timeout) must not configure all pooled connections; "+
				"if it does, this regression test no longer proves anything")
	})

	t.Run("DSN configures every pooled connection", func(t *testing.T) {
		db, err := sql.Open("sqlite3", DSN(dbPath, Options{BusyTimeoutMS: 7500, WAL: true, ForeignKeys: true}))
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)

		for i, c := range pinConns(t, db, 4) {
			var busy int
			require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy))
			assert.Equal(t, 7500, busy, "conn %d: busy_timeout must be 7500 on every pooled connection", i)

			var fk int
			require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk))
			assert.Equal(t, 1, fk, "conn %d: foreign_keys must be ON on every pooled connection", i)

			var jm string
			require.NoError(t, c.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&jm))
			assert.Equal(t, "wal", strings.ToLower(jm), "conn %d: journal_mode must be WAL", i)
		}
	})
}

// TestDSNMemoryPath ensures ":memory:" DSNs built by the helper open and work.
func TestDSNMemoryPath(t *testing.T) {
	db, err := sql.Open("sqlite3", DSN(":memory:", Options{BusyTimeoutMS: 5000, WAL: true, ForeignKeys: true}))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	// Single connection: a pooled :memory: DB is a different database per
	// connection, which is not what this test is probing.
	db.SetMaxOpenConns(1)

	_, err = db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO t (v) VALUES ('x')")
	require.NoError(t, err)

	var busy int
	require.NoError(t, db.QueryRow("PRAGMA busy_timeout").Scan(&busy))
	assert.Equal(t, 5000, busy)

	var jm string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&jm))
	assert.Equal(t, "memory", strings.ToLower(jm), ":memory: databases report journal_mode=memory")
}

// TestDSNConcurrentWritersSurviveContention proves the production fix:
// concurrent writers on a WAL database opened through DSN with a busy
// timeout survive a write-contention burst that produced SQLITE_BUSY under
// the old Exec-once approach (busy_timeout=0 on most pooled connections).
func TestDSNConcurrentWritersSurviveContention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "contention.db")
	db, err := sql.Open("sqlite3", DSN(dbPath, Options{BusyTimeoutMS: 5000, WAL: true}))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	_, err = db.Exec("CREATE TABLE burst (id INTEGER PRIMARY KEY AUTOINCREMENT, writer INT, seq INT)")
	require.NoError(t, err)

	const (
		writers = 4
		perGoro = 50
	)
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for seq := 0; seq < perGoro; seq++ {
				if _, err := db.Exec("INSERT INTO burst (writer, seq) VALUES (?, ?)", writer, seq); err != nil {
					errCh <- fmt.Errorf("writer %d seq %d: %w", writer, seq, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent write failed (SQLITE_BUSY regression): %v", err)
	}

	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM burst").Scan(&n))
	assert.Equal(t, writers*perGoro, n, "every write must land")
}
