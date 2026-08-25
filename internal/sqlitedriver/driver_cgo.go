//go:build cgo

package sqlitedriver

import (
	"strconv"

	_ "github.com/mutecomm/go-sqlcipher/v4" // registers "sqlite3" driver with encryption
)

// EncryptionSupported indicates whether the active SQLite driver supports
// SQLCipher encryption (PRAGMA key). True when built with CGO.
const EncryptionSupported = true

// dsnParams renders opts in mattn/go-sqlite3 DSN syntax, which go-sqlcipher
// (a mattn fork) applies to every new connection at open time.
func dsnParams(opts Options) []string {
	var p []string
	if opts.BusyTimeoutMS > 0 {
		p = append(p, "_busy_timeout="+strconv.Itoa(opts.BusyTimeoutMS))
	}
	if opts.WAL {
		p = append(p, "_journal_mode=WAL")
	}
	if opts.ForeignKeys {
		p = append(p, "_foreign_keys=on")
	}
	return p
}
