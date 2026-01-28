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
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/config"
)

// SQLResultStore stores SQL query results in queryable SQLite tables.
// This allows the LLM to filter/analyze large result sets without loading
// everything into context.
//
// CRITICAL FIX: Uses regular tables (not TEMP) and persists metadata in database
// to ensure results are accessible across all database connections and survive restarts.
type SQLResultStore struct {
	mu  sync.RWMutex
	db  *sql.DB
	ttl time.Duration
}

// SQLResultMetadata tracks metadata for stored SQL results.
type SQLResultMetadata struct {
	ID          string
	TableName   string
	RowCount    int64
	ColumnCount int
	Columns     []string
	Preview     *PreviewData // Preview data (first 5 + last 5 rows)
	StoredAt    time.Time
	AccessedAt  time.Time
	SizeBytes   int64
}

// SQLResultStoreConfig configures the SQL result store.
type SQLResultStoreConfig struct {
	DBPath     string // Path to SQLite database (defaults to $LOOM_DATA_DIR/loom.db)
	TTLSeconds int64
}

// NewSQLResultStore creates a new SQL result store.
func NewSQLResultStore(config *SQLResultStoreConfig) (*SQLResultStore, error) {
	if config == nil {
		config = &SQLResultStoreConfig{}
	}

	dbPath := config.DBPath
	if dbPath == "" {
		dbPath = GetDefaultLoomDBPath() // Reuse existing loom.db
	}

	ttl := time.Duration(config.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = DefaultTTLSeconds * time.Second
	}

	// Open database using same pattern as SessionStore for compatibility
	// All stores share the same loom.db file and must use consistent connection parameters
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency (matches SessionStore/ErrorStore/ArtifactStore pattern)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout for lock contention
	if _, err := db.Exec("PRAGMA busy_timeout = 10000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	store := &SQLResultStore{
		db:  db,
		ttl: ttl,
	}

	// Initialize metadata table
	if err := store.initMetadataTable(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize metadata table: %w", err)
	}

	// Start cleanup goroutine
	go store.cleanupLoop()

	return store, nil
}

// initMetadataTable creates the metadata table if it doesn't exist.
func (s *SQLResultStore) initMetadataTable() error {
	createSQL := `
		CREATE TABLE IF NOT EXISTS sql_result_metadata (
			id TEXT PRIMARY KEY,
			table_name TEXT NOT NULL,
			row_count INTEGER NOT NULL,
			column_count INTEGER NOT NULL,
			columns_json TEXT NOT NULL,
			stored_at INTEGER NOT NULL,
			accessed_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL
		)
	`
	_, err := s.db.Exec(createSQL)
	if err != nil {
		return fmt.Errorf("failed to create metadata table: %w", err)
	}

	// Create index on stored_at for efficient TTL cleanup
	indexSQL := `
		CREATE INDEX IF NOT EXISTS idx_sql_result_metadata_stored_at
		ON sql_result_metadata(stored_at)
	`
	_, err = s.db.Exec(indexSQL)
	return err
}

// IsSQLResult checks if data looks like a SQL result (has rows and columns).
func IsSQLResult(data interface{}) bool {
	m, ok := data.(map[string]interface{})
	if !ok {
		return false
	}

	// Check for direct SQL result structures
	// Format 1: {"columns": [...], "rows": [[...], [...]]}
	if _, hasColumns := m["columns"]; hasColumns {
		if _, hasRows := m["rows"]; hasRows {
			return true
		}
	}

	// Format 2: {"Columns": [...], "Rows": [[...], [...]]} (capitalized)
	if _, hasColumns := m["Columns"]; hasColumns {
		if _, hasRows := m["Rows"]; hasRows {
			return true
		}
	}

	return false
}

// Store stores SQL result data in a queryable table.
func (s *SQLResultStore) Store(id string, data interface{}) (*loomv1.DataReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Extract columns and rows
	m := data.(map[string]interface{})

	var columns []string
	var rows [][]interface{}

	// Handle both lowercase and capitalized keys
	if colsRaw, ok := m["columns"]; ok {
		if colsSlice, ok := colsRaw.([]interface{}); ok {
			for _, col := range colsSlice {
				columns = append(columns, fmt.Sprintf("%v", col))
			}
		}
	} else if colsRaw, ok := m["Columns"]; ok {
		if colsSlice, ok := colsRaw.([]interface{}); ok {
			for _, col := range colsSlice {
				columns = append(columns, fmt.Sprintf("%v", col))
			}
		}
	}

	if rowsRaw, ok := m["rows"]; ok {
		if rowsSlice, ok := rowsRaw.([]interface{}); ok {
			for _, row := range rowsSlice {
				if rowSlice, ok := row.([]interface{}); ok {
					rows = append(rows, rowSlice)
				}
			}
		}
	} else if rowsRaw, ok := m["Rows"]; ok {
		if rowsSlice, ok := rowsRaw.([]interface{}); ok {
			for _, row := range rowsSlice {
				if rowSlice, ok := row.([]interface{}); ok {
					rows = append(rows, rowSlice)
				}
			}
		}
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("no columns found in SQL result")
	}

	// Create table name
	tableName := fmt.Sprintf("tool_result_%s", id)

	// Create table (REGULAR table, not TEMP - critical fix!)
	columnDefs := make([]string, len(columns))
	for i, col := range columns {
		// Sanitize column name
		safeName := sanitizeIdentifier(col)
		columnDefs[i] = fmt.Sprintf("%s TEXT", safeName)
	}

	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", tableName, strings.Join(columnDefs, ", "))
	if _, err := s.db.Exec(createSQL); err != nil {
		return nil, fmt.Errorf("failed to create result table: %w", err)
	}

	// Insert rows
	if len(rows) > 0 {
		placeholders := make([]string, len(columns))
		for i := range placeholders {
			placeholders[i] = "?"
		}

		insertSQL := fmt.Sprintf("INSERT INTO %s VALUES (%s)", tableName, strings.Join(placeholders, ", "))
		stmt, err := s.db.Prepare(insertSQL)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare insert: %w", err)
		}
		defer stmt.Close()

		for _, row := range rows {
			if _, err := stmt.Exec(row...); err != nil {
				return nil, fmt.Errorf("failed to insert row: %w", err)
			}
		}
	}

	// Calculate size
	dataJSON, _ := json.Marshal(m)
	sizeBytes := int64(len(dataJSON))

	// Store metadata in database (not in-memory map - critical fix!)
	now := time.Now()
	columnsJSON, _ := json.Marshal(columns)

	insertMetaSQL := `
		INSERT OR REPLACE INTO sql_result_metadata
		(id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.Exec(insertMetaSQL,
		id, tableName, len(rows), len(columns), string(columnsJSON),
		now.Unix(), now.Unix(), sizeBytes)
	if err != nil {
		// Cleanup table if metadata insert fails
		_, _ = s.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
		return nil, fmt.Errorf("failed to store metadata: %w", err)
	}

	// Create data reference
	ref := &loomv1.DataReference{
		Id:          id,
		Location:    loomv1.StorageLocation_STORAGE_LOCATION_DATABASE,
		SizeBytes:   sizeBytes,
		ContentType: "application/sql",
		StoredAt:    now.UnixMilli(),
	}

	return ref, nil
}

// Query executes a SQL query against a stored result.
func (s *SQLResultStore) Query(id, query string) (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Load metadata from database (not in-memory map)
	var tableName string
	var storedAt int64
	err := s.db.QueryRow(`
		SELECT table_name, stored_at
		FROM sql_result_metadata
		WHERE id = ?
	`, id).Scan(&tableName, &storedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("result %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load metadata: %w", err)
	}

	// Update access time
	_, _ = s.db.Exec(`
		UPDATE sql_result_metadata
		SET accessed_at = ?
		WHERE id = ?
	`, time.Now().Unix(), id)

	// Execute query
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	// Collect results
	var results [][]interface{}
	for rows.Next() {
		// Create slice of interface{} for scanning
		scanArgs := make([]interface{}, len(columns))
		scanDest := make([]interface{}, len(columns))
		for i := range scanArgs {
			scanArgs[i] = &scanDest[i]
		}

		if err := rows.Scan(scanArgs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert scanned values
		row := make([]interface{}, len(columns))
		for i, val := range scanDest {
			// Handle nil and []byte
			if val == nil {
				row[i] = nil
			} else if b, ok := val.([]byte); ok {
				row[i] = string(b)
			} else {
				row[i] = val
			}
		}

		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	// Return in same format as original
	return map[string]interface{}{
		"columns": columns,
		"rows":    results,
	}, nil
}

// GetMetadata returns metadata about a stored result.
func (s *SQLResultStore) GetMetadata(id string) (*SQLResultMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var meta SQLResultMetadata
	var columnsJSON string
	var storedAt, accessedAt int64

	err := s.db.QueryRow(`
		SELECT id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes
		FROM sql_result_metadata
		WHERE id = ?
	`, id).Scan(&meta.ID, &meta.TableName, &meta.RowCount, &meta.ColumnCount,
		&columnsJSON, &storedAt, &accessedAt, &meta.SizeBytes)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("result %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load metadata: %w", err)
	}

	// Parse columns JSON
	if err := json.Unmarshal([]byte(columnsJSON), &meta.Columns); err != nil {
		return nil, fmt.Errorf("failed to parse columns: %w", err)
	}

	meta.StoredAt = time.Unix(storedAt, 0)
	meta.AccessedAt = time.Unix(accessedAt, 0)

	// Generate preview data (first 5 + last 5 rows)
	meta.Preview = s.generatePreview(meta.TableName, meta.Columns, meta.RowCount)

	// Update access time
	_, _ = s.db.Exec(`UPDATE sql_result_metadata SET accessed_at = ? WHERE id = ?`,
		time.Now().Unix(), id)

	return &meta, nil
}

// generatePreview fetches first 5 and last 5 rows for preview.
func (s *SQLResultStore) generatePreview(tableName string, columns []string, rowCount int64) *PreviewData {
	preview := &PreviewData{}

	// Sanitize table name to prevent SQL injection (gosec G201)
	// Note: tableName is already validated as "tool_result_{id}" format, but we sanitize for defense-in-depth
	safeTableName := sanitizeIdentifier(tableName)

	// Get first 5 rows
	// #nosec G201 -- tableName is sanitized via sanitizeIdentifier() above
	first5Query := fmt.Sprintf("SELECT * FROM %s LIMIT 5", safeTableName)
	rows, err := s.db.Query(first5Query)
	if err == nil {
		preview.First5 = s.rowsToArray(rows, columns)
		if closeErr := rows.Close(); closeErr != nil {
			// Log error but don't fail - preview is best-effort
			_ = closeErr
		}
	}

	// Get last 5 rows (skip if less than 10 total rows to avoid overlap)
	if rowCount > 10 {
		// #nosec G201 -- tableName is sanitized via sanitizeIdentifier() above
		last5Query := fmt.Sprintf("SELECT * FROM %s LIMIT 5 OFFSET %d", safeTableName, rowCount-5)
		rows, err := s.db.Query(last5Query)
		if err == nil {
			preview.Last5 = s.rowsToArray(rows, columns)
			if closeErr := rows.Close(); closeErr != nil {
				// Log error but don't fail - preview is best-effort
				_ = closeErr
			}
		}
	}

	return preview
}

// rowsToArray converts SQL rows to []any for JSON serialization.
func (s *SQLResultStore) rowsToArray(rows *sql.Rows, columns []string) []any {
	result := []any{}

	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		result = append(result, values)
	}

	return result
}

// Delete removes a stored result.
func (s *SQLResultStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get table name from metadata
	var tableName string
	err := s.db.QueryRow(`
		SELECT table_name
		FROM sql_result_metadata
		WHERE id = ?
	`, id).Scan(&tableName)

	if err == sql.ErrNoRows {
		return nil // Already deleted
	}
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	// Drop table
	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)
	if _, err := s.db.Exec(dropSQL); err != nil {
		return fmt.Errorf("failed to drop table: %w", err)
	}

	// Delete metadata
	_, err = s.db.Exec(`DELETE FROM sql_result_metadata WHERE id = ?`, id)
	return err
}

// cleanupLoop periodically removes expired results.
func (s *SQLResultStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanupExpired()
	}
}

// cleanupExpired removes results that have exceeded TTL.
func (s *SQLResultStore) cleanupExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find expired results
	cutoff := time.Now().Add(-s.ttl).Unix()
	rows, err := s.db.Query(`
		SELECT id, table_name
		FROM sql_result_metadata
		WHERE stored_at < ?
	`, cutoff)
	if err != nil {
		return // Ignore errors during cleanup
	}
	defer rows.Close()

	// Collect IDs and table names to delete
	type expiredResult struct {
		id        string
		tableName string
	}
	var toDelete []expiredResult

	for rows.Next() {
		var result expiredResult
		if err := rows.Scan(&result.id, &result.tableName); err != nil {
			continue
		}
		toDelete = append(toDelete, result)
	}

	// Delete expired results
	for _, result := range toDelete {
		// Drop table
		dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s", result.tableName)
		_, _ = s.db.Exec(dropSQL) // Ignore errors

		// Delete metadata
		_, _ = s.db.Exec(`DELETE FROM sql_result_metadata WHERE id = ?`, result.id)
	}
}

// Close closes the database connection.
func (s *SQLResultStore) Close() error {
	return s.db.Close()
}

// sanitizeIdentifier sanitizes a SQL identifier to prevent injection.
func sanitizeIdentifier(name string) string {
	// Remove/replace unsafe characters
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)

	// Ensure it doesn't start with a number
	if len(safe) > 0 && safe[0] >= '0' && safe[0] <= '9' {
		safe = "col_" + safe
	}

	// Ensure it's not empty
	if safe == "" {
		safe = "col"
	}

	return safe
}

// GetDefaultLoomDBPath returns the default path to loom.db
func GetDefaultLoomDBPath() string {
	return filepath.Join(config.GetLoomDataDir(), "loom.db")
}
