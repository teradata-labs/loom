//go:build cgo

package sqlitedriver

import (
	_ "github.com/mutecomm/go-sqlcipher/v4" // registers "sqlite3" driver with encryption
)

// EncryptionSupported indicates whether the active SQLite driver supports
// SQLCipher encryption (PRAGMA key). True when built with CGO.
const EncryptionSupported = true
