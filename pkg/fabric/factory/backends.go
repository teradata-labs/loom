// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/fabric"
)

const (
	// DefaultRESTTimeout is the default timeout for REST API requests
	DefaultRESTTimeout = 30 * time.Second
)

// GenericSQLBackend provides a generic SQL database backend implementation
type GenericSQLBackend struct {
	db   *sql.DB
	name string
	typ  string // postgres, mysql, sqlite
}

func (b *GenericSQLBackend) Name() string {
	return b.name
}

func (b *GenericSQLBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()
	query = strings.TrimSpace(query)

	// Check if it's a SELECT query
	isSelect := strings.HasPrefix(strings.ToUpper(query), "SELECT") ||
		strings.HasPrefix(strings.ToUpper(query), "SHOW") ||
		strings.HasPrefix(strings.ToUpper(query), "DESCRIBE")

	if isSelect {
		return b.executeSelect(ctx, query, start)
	}
	return b.executeModify(ctx, query, start)
}

func (b *GenericSQLBackend) executeSelect(ctx context.Context, query string, start time.Time) (*fabric.QueryResult, error) {
	rows, err := b.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Get column names and types
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}

	// Build column info
	cols := make([]fabric.Column, len(columns))
	for i, col := range columns {
		nullable, _ := columnTypes[i].Nullable()
		cols[i] = fabric.Column{
			Name:     col,
			Type:     columnTypes[i].DatabaseTypeName(),
			Nullable: nullable,
		}
	}

	// Read all rows
	var resultRows []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			// Convert []byte to string for text columns
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		resultRows = append(resultRows, row)
	}

	return &fabric.QueryResult{
		Type:     "rows",
		Rows:     resultRows,
		Columns:  cols,
		RowCount: len(resultRows),
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *GenericSQLBackend) executeModify(ctx context.Context, query string, start time.Time) (*fabric.QueryResult, error) {
	result, err := b.db.ExecContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()

	return &fabric.QueryResult{
		Type: "modify",
		Data: fmt.Sprintf("Query executed successfully. Rows affected: %d", rowsAffected),
		ExecutionStats: fabric.ExecutionStats{
			DurationMs:   time.Since(start).Milliseconds(),
			RowsAffected: rowsAffected,
		},
	}, nil
}

func (b *GenericSQLBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	// Generic schema query - works for most SQL databases
	var query string
	switch b.typ {
	case "postgres":
		query = fmt.Sprintf(`
			SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_name = '%s'
			ORDER BY ordinal_position`, resource)
	case "mysql":
		query = fmt.Sprintf(`
			SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT
			FROM information_schema.COLUMNS
			WHERE TABLE_NAME = '%s'
			ORDER BY ORDINAL_POSITION`, resource)
	case "sqlite":
		query = fmt.Sprintf("PRAGMA table_info(%s)", resource)
	default:
		return nil, fmt.Errorf("schema discovery not supported for %s", b.typ)
	}

	rows, err := b.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var fields []fabric.Field
	if b.typ == "sqlite" {
		// SQLite PRAGMA format: cid, name, type, notnull, dflt_value, pk
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dfltValue sql.NullString

			if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
				return nil, err
			}

			field := fabric.Field{
				Name:       name,
				Type:       typ,
				Nullable:   notnull == 0,
				PrimaryKey: pk == 1,
			}
			if dfltValue.Valid {
				field.Default = dfltValue.String
			}
			fields = append(fields, field)
		}
	} else {
		// Postgres/MySQL format
		for rows.Next() {
			var name, dataType, isNullable string
			var columnDefault sql.NullString

			if err := rows.Scan(&name, &dataType, &isNullable, &columnDefault); err != nil {
				return nil, err
			}

			field := fabric.Field{
				Name:     name,
				Type:     dataType,
				Nullable: strings.ToUpper(isNullable) == "YES",
			}
			if columnDefault.Valid {
				field.Default = columnDefault.String
			}
			fields = append(fields, field)
		}
	}

	return &fabric.Schema{
		Name:   resource,
		Type:   "table",
		Fields: fields,
	}, nil
}

func (b *GenericSQLBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	var query string
	switch b.typ {
	case "postgres":
		query = `
			SELECT table_name, table_type
			FROM information_schema.tables
			WHERE table_schema = 'public'
			ORDER BY table_name`
	case "mysql":
		query = `
			SELECT TABLE_NAME, TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE()
			ORDER BY TABLE_NAME`
	case "sqlite":
		query = `
			SELECT name, type
			FROM sqlite_master
			WHERE type IN ('table', 'view')
			ORDER BY name`
	default:
		return nil, fmt.Errorf("list resources not supported for %s", b.typ)
	}

	rows, err := b.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var resources []fabric.Resource
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}

		resources = append(resources, fabric.Resource{
			Name: name,
			Type: typ,
		})
	}

	return resources, nil
}

func (b *GenericSQLBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	schema, err := b.GetSchema(ctx, resource)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"name":        schema.Name,
		"type":        schema.Type,
		"field_count": len(schema.Fields),
		"fields":      schema.Fields,
	}, nil
}

func (b *GenericSQLBackend) Ping(ctx context.Context) error {
	return b.db.PingContext(ctx)
}

func (b *GenericSQLBackend) Capabilities() *fabric.Capabilities {
	return &fabric.Capabilities{
		SupportsTransactions: true,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     10,
		SupportedOperations:  []string{"query", "schema", "list"},
		Features: map[string]bool{
			"sql":     true,
			"schemas": true,
		},
	}
}

func (b *GenericSQLBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("custom operation not supported: %s", op)
}

func (b *GenericSQLBackend) Close() error {
	return b.db.Close()
}

// FileBackend provides a simple file-based backend
type FileBackend struct {
	baseDir string
	name    string
}

func (b *FileBackend) Name() string {
	return b.name
}

func (b *FileBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()
	parts := strings.SplitN(query, " ", 3)

	if len(parts) == 0 {
		return nil, fmt.Errorf("empty query")
	}

	operation := strings.ToLower(parts[0])

	switch operation {
	case "read":
		if len(parts) < 2 {
			return nil, fmt.Errorf("read requires filename")
		}
		return b.readFile(parts[1], start)

	case "write":
		if len(parts) < 3 {
			return nil, fmt.Errorf("write requires filename and content")
		}
		return b.writeFile(parts[1], parts[2], start)

	case "list":
		return b.listFiles(start)

	default:
		return nil, fmt.Errorf("unknown operation: %s (supported: read, write, list)", operation)
	}
}

func (b *FileBackend) readFile(filename string, start time.Time) (*fabric.QueryResult, error) {
	path := filepath.Join(b.baseDir, filename)
	// #nosec G304 -- path constructed from validated baseDir and cleaned filename
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return &fabric.QueryResult{
		Type: "text",
		Data: string(content),
		Metadata: map[string]interface{}{
			"filename": filename,
			"size":     len(content),
		},
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *FileBackend) writeFile(filename, content string, start time.Time) (*fabric.QueryResult, error) {
	path := filepath.Join(b.baseDir, filename)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &fabric.QueryResult{
		Type: "text",
		Data: fmt.Sprintf("Wrote %d bytes to %s", len(content), filename),
		Metadata: map[string]interface{}{
			"filename": filename,
			"size":     len(content),
		},
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *FileBackend) listFiles(start time.Time) (*fabric.QueryResult, error) {
	entries, err := os.ReadDir(b.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	files := make([]map[string]interface{}, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]interface{}{
			"name":     entry.Name(),
			"size":     info.Size(),
			"modified": info.ModTime().Format(time.RFC3339),
		})
	}

	return &fabric.QueryResult{
		Type:     "json",
		Rows:     files,
		RowCount: len(files),
		Metadata: map[string]interface{}{
			"count": len(files),
		},
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *FileBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	path := filepath.Join(b.baseDir, resource)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	return &fabric.Schema{
		Name: resource,
		Type: "file",
		Fields: []fabric.Field{
			{Name: "name", Type: "string"},
			{Name: "size", Type: "integer"},
			{Name: "modified", Type: "timestamp"},
		},
		Metadata: map[string]interface{}{
			"size":     info.Size(),
			"modified": info.ModTime().Unix(),
		},
	}, nil
}

func (b *FileBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	entries, err := os.ReadDir(b.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	resources := make([]fabric.Resource, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		resources = append(resources, fabric.Resource{
			Name: entry.Name(),
			Type: "file",
			Metadata: map[string]interface{}{
				"size":     info.Size(),
				"modified": info.ModTime().Unix(),
			},
		})
	}

	return resources, nil
}

func (b *FileBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	path := filepath.Join(b.baseDir, resource)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	return map[string]interface{}{
		"name":     info.Name(),
		"size":     info.Size(),
		"modified": info.ModTime().Unix(),
		"mode":     info.Mode().String(),
	}, nil
}

func (b *FileBackend) Ping(ctx context.Context) error {
	_, err := os.Stat(b.baseDir)
	return err
}

func (b *FileBackend) Capabilities() *fabric.Capabilities {
	return &fabric.Capabilities{
		SupportsTransactions: false,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     10,
		SupportedOperations:  []string{"read", "write", "list"},
		Features: map[string]bool{
			"filesystem": true,
		},
	}
}

func (b *FileBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	switch op {
	case "delete":
		filename, ok := params["filename"].(string)
		if !ok {
			return nil, fmt.Errorf("filename parameter required")
		}
		path := filepath.Join(b.baseDir, filename)
		return nil, os.Remove(path)

	default:
		return nil, fmt.Errorf("unsupported operation: %s", op)
	}
}

func (b *FileBackend) Close() error {
	return nil
}

// RESTBackend provides a REST API backend
type RESTBackend struct {
	name    string
	baseURL string
	headers map[string]string
	auth    *loomv1.AuthConfig
	timeout int
	client  *http.Client
}

func (b *RESTBackend) Name() string {
	return b.name
}

func (b *RESTBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()

	// Parse query as: METHOD path [body]
	parts := strings.SplitN(query, " ", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("query must be: METHOD path [body]")
	}

	method := strings.ToUpper(parts[0])
	path := parts[1]
	var body io.Reader
	if len(parts) > 2 {
		body = strings.NewReader(parts[2])
	}

	// Create request
	url := b.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}

	// Add auth
	if b.auth != nil {
		switch strings.ToLower(b.auth.Type) {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+b.auth.Token)
		case "basic":
			req.SetBasicAuth(b.auth.Username, b.auth.Password)
		case "apikey":
			headerName := b.auth.HeaderName
			if headerName == "" {
				headerName = "X-API-Key"
			}
			req.Header.Set(headerName, b.auth.Token)
		}
	}

	// Send request
	if b.client == nil {
		timeout := DefaultRESTTimeout
		if b.timeout > 0 {
			timeout = time.Duration(b.timeout) * time.Second
		}
		b.client = &http.Client{Timeout: timeout}
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Try to parse as JSON
	var jsonData interface{}
	if err := json.Unmarshal(respBody, &jsonData); err == nil {
		return &fabric.QueryResult{
			Type: "json",
			Data: jsonData,
			Metadata: map[string]interface{}{
				"status_code": resp.StatusCode,
				"headers":     resp.Header,
			},
			ExecutionStats: fabric.ExecutionStats{
				DurationMs: time.Since(start).Milliseconds(),
			},
		}, nil
	}

	// Return as text if not JSON
	return &fabric.QueryResult{
		Type: "text",
		Data: string(respBody),
		Metadata: map[string]interface{}{
			"status_code": resp.StatusCode,
			"headers":     resp.Header,
		},
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

func (b *RESTBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return nil, fmt.Errorf("schema discovery not supported for REST backend")
}

func (b *RESTBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return nil, fmt.Errorf("list resources not supported for REST backend")
}

func (b *RESTBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{
		"base_url": b.baseURL,
		"has_auth": b.auth != nil,
	}, nil
}

func (b *RESTBackend) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", b.baseURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// #nosec G104 -- best-effort cleanup of response body
	_ = resp.Body.Close()

	return nil
}

func (b *RESTBackend) Capabilities() *fabric.Capabilities {
	return &fabric.Capabilities{
		SupportsTransactions: false,
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     5,
		SupportedOperations:  []string{"GET", "POST", "PUT", "DELETE", "PATCH"},
		Features: map[string]bool{
			"rest": true,
		},
	}
}

func (b *RESTBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("custom operations not supported for REST backend")
}

func (b *RESTBackend) Close() error {
	if b.client != nil {
		b.client.CloseIdleConnections()
	}
	return nil
}
