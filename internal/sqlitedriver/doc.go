// Package sqlitedriver registers a SQLite database/sql driver under the name
// "sqlite3". When built with CGO (the default on macOS/Linux) it uses
// go-sqlcipher which provides SQLCipher encryption. When CGO is unavailable
// (typical on Windows without GCC) it falls back to the pure-Go
// modernc.org/sqlite driver — functional but without encryption support.
//
// Import this package for its side effects only:
//
//	import _ "github.com/teradata-labs/loom/internal/sqlitedriver"
package sqlitedriver
