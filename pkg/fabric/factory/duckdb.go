// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
)

// DuckDBBackend executes queries against duckdb database files. duckdb is an
// embedded library, not a server: every consumer in a dbt-duckdb environment
// (dbt itself included) opens the file from a process that carries the duckdb
// library. Go has no pure-Go binding, so this backend delegates to python3 —
// the process that already serves every duckdb access in those environments.
// The engine is an implementation detail behind fabric.ExecutionBackend; a
// CGO driver can replace it without touching any caller.
//
// One path: direct read-only connection, unqualified table names. Several
// paths (comma-separated DSN): there is no default — an in-memory session
// ATTACHes every file read-only under its filename stem, and all references
// are qualified (stem.schema.table). SHOW DATABASES lists the attachments.
type DuckDBBackend struct {
	paths []string // database file paths
	name  string
}

// NewDuckDBBackend creates a backend for one or more duckdb files. dsn is a
// single path or a comma-separated list.
func NewDuckDBBackend(name, dsn string) (*DuckDBBackend, error) {
	var paths []string
	for _, p := range strings.Split(dsn, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("duckdb backend requires a database file path")
	}
	b := &DuckDBBackend{paths: paths, name: name}
	if err := b.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("duckdb backend unavailable: %w", err)
	}
	return b, nil
}

// Name returns the backend identifier.
func (b *DuckDBBackend) Name() string {
	if b.name != "" {
		return b.name
	}
	return "duckdb"
}

// pythonQuery runs one query in a python3 process embedding duckdb and
// returns columns plus stringified rows. Read-only connections only: the
// backend serves probes; mutations belong to the pipeline tooling (dbt).
// Multiple files attach into an in-memory session — no default database.
func (b *DuckDBBackend) pythonQuery(ctx context.Context, query string, maxRows int) ([]string, [][]*string, error) {
	script := `
import sys, json, os, duckdb
paths, limit = sys.argv[1].split(","), int(sys.argv[2])
q = sys.stdin.read()
if len(paths) == 1:
    con = duckdb.connect(paths[0], read_only=True)
else:
    con = duckdb.connect()
    for p in paths:
        stem = os.path.splitext(os.path.basename(p))[0]
        con.execute('ATTACH %s AS "%s" (READ_ONLY)' % ("'" + p.replace("'", "''") + "'", stem))
cur = con.execute(q)
cols = [d[0] for d in cur.description] if cur.description else []
rows = cur.fetchmany(limit)
print(json.dumps({"cols": cols, "rows": [[None if v is None else str(v) for v in r] for r in rows]}))
`
	cmd := exec.CommandContext(ctx, "python3", "-c", script, strings.Join(b.paths, ","), fmt.Sprint(maxRows))
	cmd.Stdin = strings.NewReader(query)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, nil, fmt.Errorf("%s", pythonErrorLine(string(ee.Stderr)))
		}
		return nil, nil, err
	}
	var payload struct {
		Cols []string    `json:"cols"`
		Rows [][]*string `json:"rows"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, nil, fmt.Errorf("duckdb result parse failed: %w", err)
	}
	return payload.Cols, payload.Rows, nil
}

// duckdbMaxRows bounds any single result; callers page or aggregate beyond it.
const duckdbMaxRows = 5000

// ExecuteQuery runs the query and returns rows/columns.
func (b *DuckDBBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()
	cols, raw, err := b.pythonQuery(ctx, query, duckdbMaxRows)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	rows := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			if i < len(r) {
				if r[i] == nil {
					m[c] = nil
				} else {
					m[c] = *r[i]
				}
			}
		}
		rows = append(rows, m)
	}
	columns := make([]fabric.Column, 0, len(cols))
	for _, c := range cols {
		columns = append(columns, fabric.Column{Name: c})
	}
	return &fabric.QueryResult{
		Type:     "rows",
		Rows:     rows,
		Columns:  columns,
		RowCount: len(rows),
		ExecutionStats: fabric.ExecutionStats{
			DurationMs: time.Since(start).Milliseconds(),
		},
	}, nil
}

// GetSchema returns column names/types for a table or view.
func (b *DuckDBBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	cols, raw, err := b.pythonQuery(ctx,
		schemaInfoQuery(resource),
		duckdbMaxRows)
	if err != nil {
		return nil, err
	}
	_ = cols
	schema := &fabric.Schema{Name: resource, Fields: make([]fabric.Field, 0, len(raw))}
	for _, r := range raw {
		f := fabric.Field{}
		if len(r) > 0 && r[0] != nil {
			f.Name = *r[0]
		}
		if len(r) > 1 && r[1] != nil {
			f.Type = *r[1]
		}
		if len(r) > 2 && r[2] != nil {
			f.Nullable = strings.EqualFold(*r[2], "yes")
		}
		schema.Fields = append(schema.Fields, f)
	}
	if len(schema.Fields) == 0 {
		return nil, fmt.Errorf("no such table or view: %s", resource)
	}
	return schema, nil
}

// ListResources lists tables and views.
func (b *DuckDBBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	_, raw, err := b.pythonQuery(ctx,
		"SELECT table_name, table_type FROM information_schema.tables ORDER BY table_name", duckdbMaxRows)
	if err != nil {
		return nil, err
	}
	out := make([]fabric.Resource, 0, len(raw))
	for _, r := range raw {
		res := fabric.Resource{}
		if len(r) > 0 && r[0] != nil {
			res.Name = *r[0]
		}
		if len(r) > 1 && r[1] != nil {
			res.Type = strings.ToLower(*r[1])
		}
		out = append(out, res)
	}
	return out, nil
}

// GetMetadata returns row count for a resource.
func (b *DuckDBBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	_, raw, err := b.pythonQuery(ctx,
		fmt.Sprintf("SELECT count(*) FROM %q", resource), 1)
	if err != nil {
		return nil, err
	}
	meta := map[string]interface{}{"resource": resource}
	if len(raw) > 0 && len(raw[0]) > 0 && raw[0][0] != nil {
		meta["row_count"] = *raw[0][0]
	}
	return meta, nil
}

// Ping proves the engine and the file are reachable.
func (b *DuckDBBackend) Ping(ctx context.Context) error {
	_, _, err := b.pythonQuery(ctx, "SELECT 1", 1)
	return err
}

// Capabilities describes the backend.
func (b *DuckDBBackend) Capabilities() *fabric.Capabilities {
	return &fabric.Capabilities{
		SupportsTransactions: false, // read-only connection
		SupportsConcurrency:  true,  // each query is its own process
		Limits:               map[string]int64{"max_rows": duckdbMaxRows},
	}
}

// ExecuteCustomOperation has no custom operations.
func (b *DuckDBBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("duckdb backend has no custom operation %q", op)
}

// Close releases nothing — each query runs in its own short-lived process.
func (b *DuckDBBackend) Close() error { return nil }

// pythonErrorLine extracts the exception message from a python traceback.
// Tracebacks can end with SQL position markers (LINE 1: ... / ^), so the
// last line is not reliably the error; the last line naming an Error or
// Exception is. Falls back to the traceback's tail.
func pythonErrorLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.Contains(l, "Error") || strings.Contains(l, "Exception") {
			return l
		}
	}
	return lines[len(lines)-1]
}

// schemaInfoQuery builds the information_schema lookup for a resource that
// may be bare (hosts), schema-qualified (main.hosts) or catalog-qualified
// (airbnb.main.hosts — attached-database form).
func schemaInfoQuery(resource string) string {
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	parts := strings.Split(resource, ".")
	cond := ""
	switch len(parts) {
	case 3:
		cond = fmt.Sprintf("table_catalog = '%s' AND table_schema = '%s' AND table_name = '%s'",
			esc(parts[0]), esc(parts[1]), esc(parts[2]))
	case 2:
		cond = fmt.Sprintf("table_schema = '%s' AND table_name = '%s'", esc(parts[0]), esc(parts[1]))
	default:
		cond = fmt.Sprintf("table_name = '%s'", esc(resource))
	}
	return "SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE " +
		cond + " ORDER BY ordinal_position"
}
