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

// Command supabase-write-mcp is a small, deliberately-narrow stdio MCP server
// that lets an agent persist a result set (e.g. an OpenData join) into a
// dedicated Supabase schema, where a BI tool (Dreambase) can read it.
//
// SAFETY: this is NOT a general SQL tool. It can only:
//   - create the configured schema (default "dreambase") and tables within it,
//   - replace/append rows in those tables (parameterized inserts),
//   - grant SELECT on them to the Supabase API roles so dashboards can read.
//
// Table names are strictly validated; the schema is fixed (env, not arg); there
// is no arbitrary SQL, no DROP, and no access to any other schema. It connects
// via LOOM_STORAGE_POSTGRES_DSN (the same Supabase the lab already uses). Logs
// go to STDERR (stdout is the MCP channel).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

const (
	serverName      = "supabase-write-mcp"
	serverVersionID = "0.1.0"
	maxRows         = 5000
	odBaseDefault   = "https://api.tryopendata.ai"
)

// identRe restricts table names to safe SQL identifiers (lowercase, starts with
// a letter). Prevents injection and keeps Dreambase-friendly names.
var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func main() {
	logger := newStderrLogger()
	defer func() { _ = logger.Sync() }()

	dsn := os.Getenv("LOOM_STORAGE_POSTGRES_DSN")
	if dsn == "" {
		logger.Fatal("LOOM_STORAGE_POSTGRES_DSN is not set; cannot connect to Supabase")
	}
	schema := os.Getenv("SUPABASE_WRITE_SCHEMA")
	if schema == "" {
		schema = "dreambase"
	}
	if !identRe.MatchString(schema) {
		logger.Fatal("invalid SUPABASE_WRITE_SCHEMA", zap.String("schema", schema))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Fatal("connect failed", zap.Error(err))
	}
	defer pool.Close()

	// OpenData client for the server-side ETL primitive (query_to_table). Reads
	// the same OPENDATA_API_KEY the opendata-mcp shim uses (a Fly secret in the
	// machine env). When unset, query_to_table is simply not offered.
	odBase := os.Getenv("OPENDATA_API_BASE")
	if odBase == "" {
		odBase = odBaseDefault
	}
	p := &provider{
		pool:   pool,
		schema: schema,
		logger: logger,
		odKey:  os.Getenv("OPENDATA_API_KEY"),
		odBase: strings.TrimRight(odBase, "/"),
		hc:     &http.Client{Timeout: 90 * time.Second},
	}
	mcpServer := server.NewMCPServer(serverName, serverVersionID, logger, server.WithToolProvider(p))
	logger.Info("supabase-write-mcp stdio server starting",
		zap.String("schema", schema), zap.Bool("etl_enabled", p.odKey != ""))
	if err := mcpServer.Serve(ctx, transport.NewStdioServerTransport(os.Stdin, os.Stdout)); err != nil {
		logger.Fatal("serve failed", zap.Error(err))
	}
}

func newStderrLogger() *zap.Logger {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(cfg), zapcore.Lock(os.Stderr), zap.InfoLevel))
}

type provider struct {
	pool   *pgxpool.Pool
	schema string
	logger *zap.Logger

	// OpenData client (for query_to_table). Empty odKey disables that tool.
	odKey  string
	odBase string
	hc     *http.Client
}

func (p *provider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	obj := func(props map[string]interface{}, required ...string) map[string]interface{} {
		m := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	tools := []protocol.Tool{
		{
			Name:        "write_table",
			Description: fmt.Sprintf("Persist a result set into the Supabase '%s' schema so a dashboard can read it. TWO accepted shapes: (a) 'rows' as an array of flat JSON objects (column -> value); or (b) the DuckDB/OpenData result shape — pass 'columns' (array of names) plus 'rows' as an array of value-arrays (exactly what opendata_query returns). Columns and types are inferred. Use this to publish an analysis (e.g. an OpenData join) for Dreambase.", p.schema),
			InputSchema: obj(map[string]interface{}{
				"table":   map[string]interface{}{"type": "string", "description": "Table name (lowercase letters/digits/underscore, e.g. 'unemployment_vs_co2')."},
				"columns": map[string]interface{}{"type": "array", "description": "Optional column names. When set, 'rows' is interpreted as an array of value-arrays (DuckDB/OpenData shape) instead of objects.", "items": map[string]interface{}{"type": "string"}},
				"rows":    map[string]interface{}{"type": "array", "description": "Either an array of flat objects (column -> value), or — when 'columns' is given — an array of value-arrays aligned to 'columns'."},
				"mode":    map[string]interface{}{"type": "string", "description": "'replace' (default; truncate then insert) or 'append'.", "enum": []string{"replace", "append"}},
			}, "table", "rows"),
		},
		{
			Name:        "list_tables",
			Description: fmt.Sprintf("List the tables in the Supabase '%s' schema with their row counts.", p.schema),
			InputSchema: obj(map[string]interface{}{}),
		},
	}
	// Server-side ETL: run an OpenData DuckDB query and write its FULL result
	// straight into the schema, without the rows passing through the model. This
	// is how you move hundreds/thousands of joined rows — far beyond what fits in
	// a tool-result preview. Only offered when an OpenData key is configured.
	if p.odKey != "" {
		tools = append(tools, protocol.Tool{
			Name:        "query_to_table",
			Description: fmt.Sprintf("ETL in one step: run a DuckDB SQL query across OpenData datasets and write its FULL result set into the Supabase '%s' schema, server-side (rows do NOT pass through the model, so this scales to thousands of rows). Reference datasets in FROM as \"provider/dataset\" (quoted), e.g. SELECT ... FROM \"worldbank/population\" p JOIN \"worldbank/gdp\" g USING (country_code, year). Use this for a full table join + transfer.", p.schema),
			InputSchema: obj(map[string]interface{}{
				"sql":       map[string]interface{}{"type": "string", "description": "DuckDB SQL to run against OpenData (datasets quoted as \"provider/dataset\")."},
				"table":     map[string]interface{}{"type": "string", "description": "Destination table name (lowercase letters/digits/underscore)."},
				"mode":      map[string]interface{}{"type": "string", "description": "'replace' (default; truncate then insert) or 'append'.", "enum": []string{"replace", "append"}},
				"row_limit": map[string]interface{}{"type": "integer", "description": fmt.Sprintf("Max rows to fetch+write (default 1000, cap %d).", maxRows)},
			}, "sql", "table"),
		})
	}
	return tools, nil
}

func (p *provider) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	switch name {
	case "write_table":
		return p.writeTable(ctx, args)
	case "query_to_table":
		return p.queryToTable(ctx, args)
	case "list_tables":
		return p.listTables(ctx)
	default:
		return errResult("unknown tool: " + name), nil
	}
}

func (p *provider) listTables(ctx context.Context) (*protocol.CallToolResult, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'r' ORDER BY c.relname`, p.schema)
	if err != nil {
		return errResult("list failed: " + err.Error()), nil
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return errResult("scan failed: " + err.Error()), nil
		}
		names = append(names, name)
	}
	rows.Close()

	// Exact row counts (tables here are small dashboard result sets).
	out := []map[string]interface{}{}
	for _, name := range names {
		var n int64
		_ = p.pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{p.schema, name}.Sanitize()).Scan(&n)
		out = append(out, map[string]interface{}{"table": p.schema + "." + name, "rows": n})
	}
	b, _ := json.Marshal(map[string]interface{}{"schema": p.schema, "tables": out})
	return textResult(string(b)), nil
}

// buildRowMaps reshapes write_table input into ordered column names + row
// objects, accepting either (a) 'rows' as an array of objects, or (b) 'columns'
// plus 'rows' as value-arrays (the DuckDB/OpenData result shape, so an
// opendata_query result can be piped straight in). colSample carries a non-nil
// sample value per column for type inference. Returns a non-empty errMsg on bad
// input. Pure (no DB), so the reshape is unit-testable.
func buildRowMaps(args map[string]interface{}) (cols []string, rowMaps []map[string]interface{}, colSample map[string]interface{}, errMsg string) {
	rawRows, ok := args["rows"].([]interface{})
	if !ok || len(rawRows) == 0 {
		return nil, nil, nil, "'rows' must be a non-empty array"
	}
	if len(rawRows) > maxRows {
		return nil, nil, nil, fmt.Sprintf("too many rows (%d); cap is %d — aggregate or LIMIT first", len(rawRows), maxRows)
	}
	colSample = map[string]interface{}{}
	rowMaps = make([]map[string]interface{}, 0, len(rawRows))

	if rawCols, hasCols := args["columns"].([]interface{}); hasCols && len(rawCols) > 0 {
		// DuckDB/OpenData shape: explicit columns + rows as value-arrays.
		for _, c := range rawCols {
			name, _ := c.(string)
			if !identRe.MatchString(name) {
				return nil, nil, nil, fmt.Sprintf("invalid column name %q: must match %s", name, identRe.String())
			}
			cols = append(cols, name)
		}
		for _, r := range rawRows {
			arr, ok := r.([]interface{})
			if !ok {
				return nil, nil, nil, "when 'columns' is given, each row must be an array of values"
			}
			if len(arr) != len(cols) {
				return nil, nil, nil, fmt.Sprintf("row has %d values but %d columns", len(arr), len(cols))
			}
			m := make(map[string]interface{}, len(cols))
			for i, name := range cols {
				m[name] = arr[i]
				if _, seen := colSample[name]; !seen || colSample[name] == nil {
					colSample[name] = arr[i]
				}
			}
			rowMaps = append(rowMaps, m)
		}
		return cols, rowMaps, colSample, ""
	}

	// Array-of-objects shape: collect the union of keys, sample a value per column.
	for _, r := range rawRows {
		m, ok := r.(map[string]interface{})
		if !ok {
			return nil, nil, nil, "each row must be a JSON object (or pass 'columns' with array rows)"
		}
		rowMaps = append(rowMaps, m)
		for k, v := range m {
			if !identRe.MatchString(k) {
				return nil, nil, nil, fmt.Sprintf("invalid column name %q: must match %s", k, identRe.String())
			}
			if _, seen := colSample[k]; !seen || colSample[k] == nil {
				colSample[k] = v
			}
		}
	}
	for k := range colSample {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols, rowMaps, colSample, ""
}

func (p *provider) writeTable(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	table, _ := args["table"].(string)
	if !identRe.MatchString(table) {
		return errResult(fmt.Sprintf("invalid table name %q: must match %s", table, identRe.String())), nil
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		return errResult("mode must be 'replace' or 'append'"), nil
	}
	cols, rowMaps, colSet, errMsg := buildRowMaps(args)
	if errMsg != "" {
		return errResult(errMsg), nil
	}
	if errMsg := p.writeRows(ctx, table, mode, cols, rowMaps, colSet); errMsg != "" {
		return errResult(errMsg), nil
	}
	b, _ := json.Marshal(map[string]interface{}{
		"table": p.schema + "." + table, "rows_written": len(rowMaps), "columns": cols, "mode": mode,
		"written_at": time.Now().UTC().Format(time.RFC3339),
	})
	return textResult(string(b)), nil
}

// writeRows creates p.schema.table (if needed), truncates on replace, inserts
// rowMaps with a batched parameterized INSERT, and grants SELECT to the Supabase
// API roles. Returns a non-empty errMsg on failure. Shared by write_table and
// query_to_table.
func (p *provider) writeRows(ctx context.Context, table, mode string, cols []string, rowMaps []map[string]interface{}, colSample map[string]interface{}) string {
	qSchema := pgx.Identifier{p.schema}.Sanitize()
	qTable := pgx.Identifier{p.schema, table}.Sanitize()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return "begin failed: " + err.Error()
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+qSchema); err != nil {
		return "create schema failed: " + err.Error()
	}
	colDefs := make([]string, len(cols))
	for i, c := range cols {
		colDefs[i] = pgx.Identifier{c}.Sanitize() + " " + pgType(colSample[c])
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", qTable, strings.Join(colDefs, ", "))); err != nil {
		return "create table failed: " + err.Error()
	}
	if mode == "replace" {
		if _, err := tx.Exec(ctx, "TRUNCATE "+qTable); err != nil {
			return "truncate failed: " + err.Error()
		}
	}

	colIdents := make([]string, len(cols))
	for i, c := range cols {
		colIdents[i] = pgx.Identifier{c}.Sanitize()
	}
	placeholders := make([]string, 0, len(rowMaps))
	vals := make([]interface{}, 0, len(rowMaps)*len(cols))
	n := 0
	for _, rm := range rowMaps {
		ph := make([]string, len(cols))
		for i, c := range cols {
			n++
			ph[i] = fmt.Sprintf("$%d", n)
			vals = append(vals, rm[c])
		}
		placeholders = append(placeholders, "("+strings.Join(ph, ",")+")")
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", qTable, strings.Join(colIdents, ","), strings.Join(placeholders, ","))
	if _, err := tx.Exec(ctx, insert, vals...); err != nil {
		return "insert failed: " + err.Error()
	}
	if err := tx.Commit(ctx); err != nil {
		return "commit failed: " + err.Error()
	}

	// Best-effort: let the Supabase API roles read it. Done OUTSIDE the tx (on
	// the pool, autocommit) so a missing role on plain Postgres can't abort the
	// write — a failed statement aborts its whole transaction in Postgres.
	for _, role := range []string{"authenticated", "anon"} {
		_, _ = p.pool.Exec(ctx, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", qSchema, role))
		_, _ = p.pool.Exec(ctx, fmt.Sprintf("GRANT SELECT ON %s TO %s", qTable, role))
	}
	return ""
}

// queryToTable runs a DuckDB query against OpenData server-side and writes the
// FULL result into the schema — the rows never pass through the model, so this
// scales far past a tool-result preview. It reuses write_table's reshape
// (buildRowMaps) and writeRows.
func (p *provider) queryToTable(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if p.odKey == "" {
		return errResult("query_to_table unavailable: OPENDATA_API_KEY is not configured"), nil
	}
	sql, _ := args["sql"].(string)
	if strings.TrimSpace(sql) == "" {
		return errResult("'sql' is required"), nil
	}
	table, _ := args["table"].(string)
	if !identRe.MatchString(table) {
		return errResult(fmt.Sprintf("invalid table name %q: must match %s", table, identRe.String())), nil
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		return errResult("mode must be 'replace' or 'append'"), nil
	}
	rowLimit := 1000
	if v, ok := args["row_limit"].(float64); ok && v > 0 {
		rowLimit = int(v)
	}
	if rowLimit > maxRows {
		rowLimit = maxRows
	}

	columns, rows, errMsg := p.queryOpenData(ctx, sql, rowLimit)
	if errMsg != "" {
		return errResult(errMsg), nil
	}
	// Reuse the exact reshape write_table uses for the DuckDB/OpenData shape.
	cols, rowMaps, colSet, errMsg := buildRowMaps(map[string]interface{}{"columns": columns, "rows": rows})
	if errMsg != "" {
		return errResult("OpenData result not writable: " + errMsg), nil
	}
	if errMsg := p.writeRows(ctx, table, mode, cols, rowMaps, colSet); errMsg != "" {
		return errResult(errMsg), nil
	}
	b, _ := json.Marshal(map[string]interface{}{
		"table": p.schema + "." + table, "rows_written": len(rowMaps), "columns": cols, "mode": mode,
		"source": "opendata", "written_at": time.Now().UTC().Format(time.RFC3339),
	})
	return textResult(string(b)), nil
}

// queryOpenData POSTs a DuckDB query to OpenData's /v1/query and returns the raw
// columns + rows (the {"columns":[...],"rows":[[...]]} shape) for buildRowMaps.
func (p *provider) queryOpenData(ctx context.Context, sql string, rowLimit int) (columns []interface{}, rows []interface{}, errMsg string) {
	body, _ := json.Marshal(map[string]interface{}{"sql": sql, "row_limit": rowLimit})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.odBase+"/v1/query", bytes.NewReader(body))
	if err != nil {
		return nil, nil, "build OpenData request: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.odKey)
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, nil, "OpenData request failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		snippet := string(raw)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, nil, fmt.Sprintf("OpenData returned HTTP %d: %s", resp.StatusCode, snippet)
	}
	var parsed struct {
		Columns []interface{} `json:"columns"`
		Rows    []interface{} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, "parse OpenData response: " + err.Error()
	}
	if len(parsed.Columns) == 0 || len(parsed.Rows) == 0 {
		return nil, nil, "OpenData query returned no rows (check the SQL and dataset names)"
	}
	return parsed.Columns, parsed.Rows, ""
}

// pgType infers a Postgres column type from a sample JSON value.
func pgType(v interface{}) string {
	switch v.(type) {
	case float64, int, int64:
		return "double precision"
	case bool:
		return "boolean"
	default:
		return "text"
	}
}

func textResult(s string) *protocol.CallToolResult {
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: s}}}
}

func errResult(msg string) *protocol.CallToolResult {
	return &protocol.CallToolResult{IsError: true, Content: []protocol.Content{{Type: "text", Text: msg}}}
}
