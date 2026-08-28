//go:build !cgo

package sqlitedriver

import (
	"database/sql"
	"errors"
	"strconv"

	"modernc.org/sqlite"
)

func init() {
	sql.Register("sqlite3", &sqlite.Driver{})
}

// EncryptionSupported indicates whether the active SQLite driver supports
// SQLCipher encryption (PRAGMA key). False when built without CGO.
const EncryptionSupported = false

// dsnParams renders opts in modernc.org/sqlite's native "_pragma=name(value)"
// DSN syntax, which the driver applies to every new connection at open time.
// (modernc v1.55.0+ also accepts the mattn shorthand keys, but the _pragma
// form is the documented contract on every modernc release.)
func dsnParams(opts Options) []string {
	var p []string
	if opts.BusyTimeoutMS > 0 {
		p = append(p, "_pragma=busy_timeout("+strconv.Itoa(opts.BusyTimeoutMS)+")")
	}
	if opts.WAL {
		p = append(p, "_pragma=journal_mode(WAL)")
	}
	if opts.ForeignKeys {
		p = append(p, "_pragma=foreign_keys(1)")
	}
	return p
}

// EncryptedDSN always fails without CGO: modernc.org/sqlite is plain SQLite
// with no SQLCipher support, so there is no key to render and no way to open
// an encrypted database at all. It returns an error rather than a silently
// unkeyed DSN, which would hand the caller a *sql.DB that writes plaintext.
// Callers gate on EncryptionSupported and report the missing capability with
// their own wording before reaching this.
func EncryptedDSN(string, string, Options) (string, error) {
	return "", errors.New("sqlitedriver: database encryption requires CGO (SQLCipher); not available in this build")
}
