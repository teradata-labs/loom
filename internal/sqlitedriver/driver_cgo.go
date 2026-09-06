//go:build cgo

package sqlitedriver

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

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

// EncryptedDSN renders path plus opts into a DSN that also carries the
// SQLCipher key, so every connection database/sql opens is keyed.
//
// PRAGMA key has the same pooled-connection defect as PRAGMA busy_timeout,
// only louder: setting it with db.Exec after opening keys the single
// connection that happens to run the statement, and every other pooled
// connection then fails outright with "file is not a database" — so an
// encrypted store works only for as long as its pool never grows past one.
//
// go-sqlcipher applies _pragma_key by running PRAGMA key = "<value>" as each
// connection is opened. Note the DOUBLE quotes: a double quote inside the key
// is therefore escaped by doubling it, which is SQLite's rule for quoted
// tokens and round-trips to the same key SQLCipher derives from the
// single-quoted PRAGMA form. TestEncryptedDSNRoundTrip pins that against the
// driver, including the awkward key characters.
func EncryptedDSN(path, key string, opts Options) (string, error) {
	if path == "" {
		return "", errors.New("sqlitedriver: encrypted database path cannot be empty")
	}
	if key == "" {
		return "", errors.New("sqlitedriver: encryption key cannot be empty")
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	keyed := path + sep + "_pragma_key=" + url.QueryEscape(strings.ReplaceAll(key, `"`, `""`))
	return DSN(keyed, opts), nil
}
