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
func (s *ResultStore) Store(id string, data interface{}) (*loomv1.DataReference, error) {
	ctx := context.Background()
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

	// Create the result table
	createSQL := buildCreateTableSQL(tableName, columns)
	if _, err := s.pool.Exec(ctx, createSQL); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create result table: %w", err)
	}

	// Insert rows
	if len(rows) > 0 {
		if err := s.insertRows(ctx, tableName, columns, rows); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to insert rows: %w", err)
		}
	}

	// Store metadata
	columnsJSON, _ := json.Marshal(columns)
	sizeBytes := estimateSize(rows, columns)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO sql_result_metadata (id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			table_name = EXCLUDED.table_name,
			row_count = EXCLUDED.row_count,
			column_count = EXCLUDED.column_count,
			columns_json = EXCLUDED.columns_json,
			stored_at = EXCLUDED.stored_at,
			accessed_at = EXCLUDED.accessed_at,
			size_bytes = EXCLUDED.size_bytes`,
		id, tableName, len(rows), len(columns), columnsJSON,
		time.Now().UTC(), time.Now().UTC(), sizeBytes,
	)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to store metadata: %w", err)
	}

	return &loomv1.DataReference{
		Id:        id,
		SizeBytes: sizeBytes,
	}, nil
}

// Query executes a query against a stored result table.
func (s *ResultStore) Query(id, query string) (interface{}, error) {
	ctx := context.Background()
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.query")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	tableName := sanitizeTableName("tool_result_" + id)

	// Update access time
	s.pool.Exec(ctx, "UPDATE sql_result_metadata SET accessed_at = $1 WHERE id = $2", time.Now().UTC(), id) //nolint:errcheck

	// Build query - if empty, select all
	var sqlQuery string
	if query == "" {
		sqlQuery = fmt.Sprintf("SELECT * FROM %s", tableName)
	} else {
		sqlQuery = fmt.Sprintf("SELECT * FROM %s WHERE %s", tableName, query)
	}

	rows, err := s.pool.Query(ctx, sqlQuery)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query result: %w", err)
	}
	defer rows.Close()

	// Read results into generic structure
	fieldDescs := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to read row values: %w", err)
		}

		row := make(map[string]interface{})
		for i, fd := range fieldDescs {
			if i < len(values) {
				row[string(fd.Name)] = values[i]
			}
		}
		results = append(results, row)
	}

	return results, rows.Err()
}

// GetMetadata retrieves metadata about a stored result.
func (s *ResultStore) GetMetadata(id string) (*storage.SQLResultMetadata, error) {
	ctx := context.Background()
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.get_metadata")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	var (
		meta        storage.SQLResultMetadata
		columnsJSON []byte
	)

	err := s.pool.QueryRow(ctx, `
		SELECT id, table_name, row_count, column_count, columns_json, stored_at, accessed_at, size_bytes
		FROM sql_result_metadata WHERE id = $1`,
		id,
	).Scan(&meta.ID, &meta.TableName, &meta.RowCount, &meta.ColumnCount, &columnsJSON, &meta.StoredAt, &meta.AccessedAt, &meta.SizeBytes)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get metadata: %w", err)
	}

	if len(columnsJSON) > 0 {
		json.Unmarshal(columnsJSON, &meta.Columns) //nolint:errcheck
	}

	return &meta, nil
}

// Delete removes a stored result and its table.
func (s *ResultStore) Delete(id string) error {
	ctx := context.Background()
	ctx, span := s.tracer.StartSpan(ctx, "pg_result_store.delete")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("result_id", id)

	tableName := sanitizeTableName("tool_result_" + id)

	// Drop the result table
	_, err := s.pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to drop result table: %w", err)
	}

	// Delete metadata
	_, err = s.pool.Exec(ctx, "DELETE FROM sql_result_metadata WHERE id = $1", id)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to delete metadata: %w", err)
	}

	return nil
}

// Close is a no-op; the pool is managed by the backend.
func (s *ResultStore) Close() error {
	return nil
}

// insertRows uses batch insert for efficiency.
func (s *ResultStore) insertRows(ctx context.Context, tableName string, columns []string, rows [][]interface{}) error {
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
				paddedRow[i] = fmt.Sprintf("%v", row[i])
			} else {
				paddedRow[i] = nil
			}
		}
		batch.Queue(insertSQL, paddedRow...)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to insert row: %w", err)
		}
	}

	return nil
}

// sanitizeTableName ensures a table name is safe for SQL.
func sanitizeTableName(name string) string {
	var safe strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			safe.WriteRune(r)
		}
	}
	return safe.String()
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
