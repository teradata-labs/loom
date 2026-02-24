// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// FTS5 (Full-Text Search version 5) Support
//
// FTS5 is ENABLED BY DEFAULT in all Loom builds via the Justfile.
// All go build and go test commands automatically include -tags fts5.
//
// This provides semantic message search capabilities in SessionStore
// via the SearchMessages() method with BM25 ranking.
//
// If building manually without the Justfile, use:
//   go build -tags fts5 ./...
//   go test -tags fts5 ./...
//
// Without the fts5 tag, SessionStore will still work but SearchMessages()
// will fail with "no such module: fts5" errors.
