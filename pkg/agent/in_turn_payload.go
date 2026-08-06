// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/teradata-labs/loom/pkg/storage"
)

// ErrNotThisTurn is the typed miss for in-turn payload lookups: the id does not
// resolve to a current-turn in-memory tool result. Callers convert it to their
// own loud error and never guess — there is no cross-turn data door except
// re-run (HLD §7.1, §7.2).
var ErrNotThisTurn = errors.New("not a current-turn in-memory payload")

// InTurnPayload returns the full in-memory content of message id iff the
// message belongs to the current turn (m.Turn == T) and holds a tool result.
// Any other case returns ErrNotThisTurn — a typed miss carrying the id. One
// reader path, no store, no copy: it resolves against L1 in memory.
func (a *Agent) InTurnPayload(sessionID string, messageID int64) (string, error) {
	session, ok := a.memory.GetSession(sessionID)
	if !ok || session == nil {
		return "", fmt.Errorf("%w: message %d", ErrNotThisTurn, messageID)
	}
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok || segMem == nil {
		return "", fmt.Errorf("%w: message %d", ErrNotThisTurn, messageID)
	}
	return segMem.inTurnPayload(messageID)
}

// inTurnPayload resolves messageID against L1: current-turn tool rows only.
func (sm *SegmentedMemory) inTurnPayload(messageID int64) (string, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	t := sm.currentTurnLocked()
	id := strconv.FormatInt(messageID, 10)
	for i := range sm.contextMessages {
		m := &sm.contextMessages[i]
		if m.Role == "tool" && m.ID == id && m.Turn == t {
			return m.Content, nil
		}
	}
	return "", fmt.Errorf("%w: message %d", ErrNotThisTurn, messageID)
}

// contextThreshold returns the one threshold value (HLD §5.1) in effect for
// this agent, in bytes.
func (a *Agent) contextThreshold() int {
	if a.sharedMemoryThreshold > 0 {
		if a.sharedMemoryThreshold < minThreshold {
			return minThreshold // §4.1 needs room for the tail; see minThreshold
		}
		return int(a.sharedMemoryThreshold)
	}
	return int(storage.DefaultSharedMemoryThreshold)
}

// inTurnSQLKey addresses one message's lazily-built in-turn SQLite database.
type inTurnSQLKey struct {
	sessionID string
	messageID int64
}

// inTurnSQLite lazily parses the payload (array of uniform objects, or
// {columns, rows}) into an in-turn :memory: SQLite database serving table
// `results`, dropped at turn end (HLD §7.1, §7.3). Non-rectangular payloads
// error — they are paging-only.
func (a *Agent) inTurnSQLite(sessionID string, messageID int64, payload string) (*sql.DB, error) {
	a.inTurnSQLMu.Lock()
	defer a.inTurnSQLMu.Unlock()

	if a.inTurnSQL == nil {
		a.inTurnSQL = make(map[inTurnSQLKey]*sql.DB)
	}
	key := inTurnSQLKey{sessionID: sessionID, messageID: messageID}
	if db, ok := a.inTurnSQL[key]; ok {
		return db, nil
	}

	columns, rows, err := tabularPayload(payload)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open in-turn sqlite: %w", err)
	}

	quoted := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		placeholders[i] = "?"
	}
	if _, err := db.Exec("CREATE TABLE results (" + strings.Join(quoted, ", ") + ")"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create results table: %w", err)
	}
	insert := "INSERT INTO results VALUES (" + strings.Join(placeholders, ", ") + ")"
	for _, row := range rows {
		vals := make([]interface{}, len(columns))
		for i := range columns {
			if i < len(row) {
				vals[i] = normalizeSQLValue(row[i])
			}
		}
		if _, err := db.Exec(insert, vals...); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("load results table: %w", err)
		}
	}

	a.inTurnSQL[key] = db
	return db, nil
}

// dropInTurnSQLite drops a session's in-turn SQLite databases (turn end, §7.3).
func (a *Agent) dropInTurnSQLite(sessionID string) {
	a.inTurnSQLMu.Lock()
	defer a.inTurnSQLMu.Unlock()
	for key, db := range a.inTurnSQL {
		if key.sessionID == sessionID {
			_ = db.Close()
			delete(a.inTurnSQL, key)
		}
	}
}

// dropAllInTurnSQLite drops every session's in-turn SQLite databases.
func (a *Agent) dropAllInTurnSQLite() {
	a.inTurnSQLMu.Lock()
	defer a.inTurnSQLMu.Unlock()
	for key, db := range a.inTurnSQL {
		_ = db.Close()
		delete(a.inTurnSQL, key)
	}
}

// tabularPayload parses a payload into columns + rows. Accepted shapes: an
// array of uniform objects, or {columns, rows}. Anything else errors —
// paging-only.
func tabularPayload(payload string) ([]string, [][]interface{}, error) {
	trimmed := strings.TrimSpace(payload)

	// {columns, rows} shape.
	var tabular struct {
		Columns []interface{}   `json:"columns"`
		Rows    [][]interface{} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(trimmed), &tabular); err == nil &&
		len(tabular.Columns) > 0 && tabular.Rows != nil {
		columns := make([]string, len(tabular.Columns))
		for i, c := range tabular.Columns {
			columns[i] = fmt.Sprintf("%v", c)
		}
		return columns, tabular.Rows, nil
	}

	// Array-of-uniform-objects shape.
	var objects []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &objects); err == nil && len(objects) > 0 {
		columns := make([]string, 0, len(objects[0]))
		for k := range objects[0] {
			columns = append(columns, k)
		}
		sort.Strings(columns)
		rows := make([][]interface{}, 0, len(objects))
		for _, obj := range objects {
			row := make([]interface{}, len(columns))
			for i, c := range columns {
				row[i] = obj[c]
			}
			rows = append(rows, row)
		}
		return columns, rows, nil
	}

	return nil, nil, errors.New("payload is not an array of uniform objects or {columns, rows}")
}

// normalizeSQLValue converts nested structures to JSON strings for storage.
func normalizeSQLValue(v interface{}) interface{} {
	switch v.(type) {
	case map[string]interface{}, []interface{}:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	default:
		return v
	}
}
