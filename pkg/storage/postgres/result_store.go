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
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
)

// ResultStore implements storage.ResultStore using PostgreSQL.
type ResultStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewResultStore creates a new PostgreSQL-backed result store.
func NewResultStore(pool *pgxpool.Pool, tracer observability.Tracer) *ResultStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &ResultStore{
		pool:   pool,
		tracer: tracer,
	}
}

// Store persists a result set in a dynamic table and records metadata.
// All database operations (create table, insert rows, store metadata) execute
// within a single transaction with RLS tenant isolation via execInTx.
func (s *ResultStore) Store(ctx context.Context, id string, data interface{}) (*loomv1.DataReference, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.store")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	// Convert data to rows and columns
	rows, columns, err := extractRowsAndColumns(data)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to extract data: %w", err)
	}

	tableName := sanitizeTableName("tool_result_" + id)
	columnsJSON, err := json.Marshal(columns)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal columns: %w", err)
	}
	sizeBytes := estimateSize(rows, columns)

	var ref *loomv1.DataReference

	if err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Create the result table
		createSQL := buildCreateTableSQL(tableName, columns)
		if _, err := tx.Exec(ctx, createSQL); err != nil {
			return fmt.Errorf("failed to create result table: %w", err)
		}

		// Insert rows
		if len(rows) > 0 {
			if err := s.insertRows(ctx, tx, tableName, columns, rows); err != nil {
				return fmt.Errorf("failed to insert rows: %w", err)
			}
		}

		// Store metadata
		userID := UserIDFromContext(ctx)
		if _, err := tx.Exec(ctx, `
			INSERT INTO sql_result_metadata (id, user_id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO UPDATE SET
				table_name = EXCLUDED.table_name,
				row_count = EXCLUDED.row_count,
				column_count = EXCLUDED.column_count,
				columns_json = EXCLUDED.columns_json,
				stored_at = EXCLUDED.stored_at,
				accessed_at = EXCLUDED.accessed_at,
				size_bytes = EXCLUDED.size_bytes`,
			id, userID, tableName, len(rows), len(columns), columnsJSON,
			time.Now().UTC(), time.Now().UTC(), sizeBytes,
		); err != nil {
			return fmt.Errorf("failed to store metadata: %w", err)
		}

		ref = &loomv1.DataReference{
			Id:        id,
			SizeBytes: sizeBytes,
		}
		return nil
	}); err != nil {
		span.RecordError(err)
		return nil, err
	}

	return ref, nil
}

// validTableNameRe matches safe table names: alphanumeric characters and underscores only.
var validTableNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Query executes a query against a stored result table.
// If query is empty, returns all rows from the result table.
// Caller-supplied SQL is NOT allowed; only an empty query string is accepted.
// This prevents SQL injection. The operation runs in a transaction for RLS
// tenant isolation.
func (s *ResultStore) Query(ctx context.Context, id, query string) (interface{}, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.query")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	// Only allow empty query (SELECT * from the result table).
	// Caller-supplied SQL is rejected to prevent SQL injection.
	if strings.TrimSpace(query) != "" {
		return nil, fmt.Errorf("custom SQL queries are not supported; use an empty query to retrieve all rows")
	}

	// Validate the table name component derived from id to prevent injection
	// through crafted IDs. The raw name (before pgx quoting) must be safe.
	rawTableName := "tool_result_" + id
	if !validTableNameRe.MatchString(rawTableName) {
		return nil, fmt.Errorf("invalid result ID: contains disallowed characters")
	}
	tableName := sanitizeTableName(rawTableName)

	sqlQuery := fmt.Sprintf("SELECT * FROM %s", tableName)

	var results []map[string]interface{}

	if err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Update access time (scoped to user via user_id)
		userID := UserIDFromContext(ctx)
		if _, err := tx.Exec(ctx,
			"UPDATE sql_result_metadata SET accessed_at = $1 WHERE id = $2 AND user_id = $3",
			time.Now().UTC(), id, userID,
		); err != nil {
			return fmt.Errorf("failed to update access time: %w", err)
		}

		rows, err := tx.Query(ctx, sqlQuery)
		if err != nil {
			return fmt.Errorf("failed to query result: %w", err)
		}
		defer rows.Close()

		// Read results into generic structure
		fieldDescs := rows.FieldDescriptions()

		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return fmt.Errorf("failed to read row values: %w", err)
			}

			row := make(map[string]interface{})
			for i, fd := range fieldDescs {
				if i < len(values) {
					row[string(fd.Name)] = values[i]
				}
			}
			results = append(results, row)
		}

		return rows.Err()
	}); err != nil {
		span.RecordError(err)
		return nil, err
	}

	return results, nil
}

// GetMetadata retrieves metadata about a stored result.
// The query runs in a transaction for RLS tenant isolation.
func (s *ResultStore) GetMetadata(ctx context.Context, id string) (*storage.SQLResultMetadata, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.get_metadata")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	var meta *storage.SQLResultMetadata

	if err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var (
			m           storage.SQLResultMetadata
			columnsJSON []byte
		)

		userID := UserIDFromContext(ctx)
		err := tx.QueryRow(ctx, `
			SELECT id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes
			FROM sql_result_metadata WHERE id = $1 AND user_id = $2`,
			id, userID,
		).Scan(&m.ID, &m.TableName, &m.RowCount, &m.ColumnCount, &columnsJSON, &m.StoredAt, &m.AccessedAt, &m.SizeBytes)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return fmt.Errorf("failed to get metadata: %w", err)
		}

		if len(columnsJSON) > 0 {
			if err := json.Unmarshal(columnsJSON, &m.Columns); err != nil {
				return fmt.Errorf("failed to unmarshal columns: %w", err)
			}
		}

		meta = &m
		return nil
	}); err != nil {
		span.RecordError(err)
		return nil, err
	}

	return meta, nil
}

// Delete removes a stored result and its table.
// Both the table drop and metadata delete execute within a single transaction
// with RLS tenant isolation via execInTx.
func (s *ResultStore) Delete(ctx context.Context, id string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.delete")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	tableName := sanitizeTableName("tool_result_" + id)

	if err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Drop the result table
		if _, err := tx.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)); err != nil {
			return fmt.Errorf("failed to drop result table: %w", err)
		}

		// Delete metadata (scoped to user via user_id)
		userID := UserIDFromContext(ctx)
		if _, err := tx.Exec(ctx, "DELETE FROM sql_result_metadata WHERE id = $1 AND user_id = $2", id, userID); err != nil {
			return fmt.Errorf("failed to delete metadata: %w", err)
		}

		return nil
	}); err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

// Close is a no-op; the pool is managed by the backend.
func (s *ResultStore) Close() error {
	return nil
}

// insertRows uses batch insert for efficiency within the given transaction.
func (s *ResultStore) insertRows(ctx context.Context, tx pgx.Tx, tableName string, columns []string, rows [][]interface{}) error {
	placeholders := make([]string, len(columns))
	for i := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		strings.Join(quotedColumns(columns), ", "),
		strings.Join(placeholders, ", "),
	)

	batch := &pgx.Batch{}
	for _, row := range rows {
		paddedRow := make([]interface{}, len(columns))
		for i := range paddedRow {
			if i < len(row) {
				if row[i] == nil {
					paddedRow[i] = nil // preserve SQL NULL
				} else {
					paddedRow[i] = fmt.Sprintf("%v", row[i])
				}
			} else {
				paddedRow[i] = nil
			}
		}
		batch.Queue(insertSQL, paddedRow...)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()

	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to insert row: %w", err)
		}
	}

	return nil
}

// sanitizeTableName ensures a table name is safe for SQL using pgx's
// identifier quoting, which properly handles special characters and
// prevents SQL injection.
func sanitizeTableName(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// buildCreateTableSQL generates a CREATE TABLE statement with TEXT columns.
func buildCreateTableSQL(tableName string, columns []string) string {
	var cols []string
	for _, col := range columns {
		cols = append(cols, fmt.Sprintf("%s TEXT", pgx.Identifier{col}.Sanitize()))
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", tableName, strings.Join(cols, ", "))
}

// quotedColumns returns column names quoted for PostgreSQL.
func quotedColumns(columns []string) []string {
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = pgx.Identifier{col}.Sanitize()
	}
	return quoted
}

// extractRowsAndColumns converts generic data to rows and column names.
func extractRowsAndColumns(data interface{}) ([][]interface{}, []string, error) {
	switch v := data.(type) {
	case []map[string]interface{}:
		if len(v) == 0 {
			return nil, nil, nil
		}
		var columns []string
		for k := range v[0] {
			columns = append(columns, k)
		}
		sort.Strings(columns) // deterministic column ordering
		rows := make([][]interface{}, len(v))
		for i, row := range v {
			vals := make([]interface{}, len(columns))
			for j, col := range columns {
				vals[j] = row[col]
			}
			rows[i] = vals
		}
		return rows, columns, nil

	case [][]interface{}:
		if len(v) == 0 {
			return nil, nil, nil
		}
		columns := make([]string, len(v[0]))
		for i := range columns {
			columns[i] = fmt.Sprintf("col%d", i+1)
		}
		return v, columns, nil

	default:
		return nil, nil, fmt.Errorf("unsupported data type: %T", data)
	}
}

// estimateSize estimates the storage size of the data in bytes.
func estimateSize(rows [][]interface{}, columns []string) int64 {
	size := int64(len(columns) * 20)
	for _, row := range rows {
		for _, val := range row {
			size += int64(len(fmt.Sprintf("%v", val)))
		}
	}
	return size
}

// Compile-time check: ResultStore implements storage.ResultStore.
var _ storage.ResultStore = (*ResultStore)(nil)
