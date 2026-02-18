//go:build !cgo

package sqlitedriver

import (
	"database/sql"

	"modernc.org/sqlite"
)

func init() {
	sql.Register("sqlite3", &sqlite.Driver{})
}

// EncryptionSupported indicates whether the active SQLite driver supports
// SQLCipher encryption (PRAGMA key). False when built without CGO.
const EncryptionSupported = false
