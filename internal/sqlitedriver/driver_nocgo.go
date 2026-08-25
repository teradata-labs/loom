//go:build !cgo

package sqlitedriver

import (
	"database/sql"
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
