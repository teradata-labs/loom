package sqlitedriver

import "strings"

// Options selects per-connection SQLite settings rendered into a DSN by DSN.
//
// PRAGMA busy_timeout and PRAGMA foreign_keys are per-connection settings:
// executing them with db.Exec on a pooled *sql.DB configures only the single
// connection that happens to run the statement, leaving every other pooled
// connection at the SQLite defaults (busy_timeout=0, so writers fail
// instantly with SQLITE_BUSY under contention; foreign_keys=OFF). Rendering
// them into the DSN is the only way to guarantee every connection
// database/sql opens gets them.
type Options struct {
	// BusyTimeoutMS sets PRAGMA busy_timeout in milliseconds on every
	// connection. 0 leaves the driver default (no busy handler: lock
	// contention fails immediately with SQLITE_BUSY).
	BusyTimeoutMS int

	// WAL sets PRAGMA journal_mode=WAL. journal_mode is a persistent,
	// database-level setting (stored in the file header), so unlike the
	// other options a one-time post-open Exec also works; carrying it in
	// the DSN keeps the intent with the open call and covers fresh files
	// without a follow-up statement. Ignored by ":memory:" databases
	// (the PRAGMA reports "memory" and does not error).
	WAL bool

	// ForeignKeys sets PRAGMA foreign_keys=ON on every connection
	// (per-connection setting, default OFF).
	ForeignKeys bool
}

// DSN renders path plus opts into a DSN for the registered "sqlite3" driver.
//
// The query-parameter syntax differs between the two drivers this package can
// register (mattn-style "_busy_timeout=5000" for go-sqlcipher under CGO,
// "_pragma=busy_timeout(5000)" for modernc.org/sqlite without CGO); the
// build-tagged dsnParams in driver_cgo.go / driver_nocgo.go supplies the
// correct one, so callers never spell driver-specific parameters themselves.
//
// Query parameters already present in path (e.g. "file:x.db?cache=shared")
// are preserved. ":memory:" paths work with both drivers.
//
// An empty path is returned unchanged: to SQLite it means a private temporary
// database, and appending parameters would turn it into a literal file named
// "?_busy_timeout=..." in the working directory.
func DSN(path string, opts Options) string {
	if path == "" {
		return path
	}
	params := dsnParams(opts)
	if len(params) == 0 {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(params, "&")
}
