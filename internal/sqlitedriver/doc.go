// Package sqlitedriver registers a SQLite database/sql driver under the name
// "sqlite3". When built with CGO (the default on macOS/Linux) it uses
// go-sqlcipher which provides SQLCipher encryption. When CGO is unavailable
// (typical on Windows without GCC) it falls back to the pure-Go
// modernc.org/sqlite driver — functional but without encryption support.
//
// Import it for the driver registration side effect, and use DSN to render
// the per-connection settings that must be applied as each pooled connection
// is opened rather than with a post-open db.Exec:
//
//	import "github.com/teradata-labs/loom/internal/sqlitedriver"
//
//	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{
//		BusyTimeoutMS: 5000,
//		WAL:           true,
//		ForeignKeys:   true,
//	}))
//
// EncryptedDSN does the same for SQLCipher-encrypted databases, carrying the
// key so every pooled connection is keyed (CGO builds only).
package sqlitedriver
