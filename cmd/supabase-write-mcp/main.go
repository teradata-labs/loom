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
	"context"
	"encoding/json"
	"fmt"
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

	p := &provider{pool: pool, schema: schema, logger: logger}
	mcpServer := server.NewMCPServer(serverName, serverVersionID, logger, server.WithToolProvider(p))
	logger.Info("supabase-write-mcp stdio server starting", zap.String("schema", schema))
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
}

func (p *provider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	obj := func(props map[string]interface{}, required ...string) map[string]interface{} {
		m := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	return []protocol.Tool{
		{
			Name:        "write_table",
			Description: fmt.Sprintf("Persist a result set into the Supabase '%s' schema so a dashboard can read it. Provide rows as an array of flat JSON objects (same keys); columns and types are inferred. Use this to publish an analysis (e.g. an OpenData join) for Dreambase.", p.schema),
			InputSchema: obj(map[string]interface{}{
				"table": map[string]interface{}{"type": "string", "description": "Table name (lowercase letters/digits/underscore, e.g. 'unemployment_vs_co2')."},
				"rows":  map[string]interface{}{"type": "array", "description": "Array of flat objects (column -> value).", "items": map[string]interface{}{"type": "object"}},
				"mode":  map[string]interface{}{"type": "string", "description": "'replace' (default; truncate then insert) or 'append'.", "enum": []string{"replace", "append"}},
			}, "table", "rows"),
		},
		{
			Name:        "list_tables",
			Description: fmt.Sprintf("List the tables in the Supabase '%s' schema with their row counts.", p.schema),
			InputSchema: obj(map[string]interface{}{}),
		},
	}, nil
}

func (p *provider) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	switch name {
	case "write_table":
		return p.writeTable(ctx, args)
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
	rawRows, ok := args["rows"].([]interface{})
	if !ok || len(rawRows) == 0 {
		return errResult("'rows' must be a non-empty array of objects"), nil
	}
	if len(rawRows) > maxRows {
		return errResult(fmt.Sprintf("too many rows (%d); cap is %d — aggregate or LIMIT first", len(rawRows), maxRows)), nil
	}

	// Collect the column set (union of keys) and a sample value per column for typing.
	colSet := map[string]interface{}{}
	rowMaps := make([]map[string]interface{}, 0, len(rawRows))
	for _, r := range rawRows {
		m, ok := r.(map[string]interface{})
		if !ok {
			return errResult("each row must be a JSON object"), nil
		}
		rowMaps = append(rowMaps, m)
		for k, v := range m {
			if !identRe.MatchString(k) {
				return errResult(fmt.Sprintf("invalid column name %q: must match %s", k, identRe.String())), nil
			}
			if _, seen := colSet[k]; !seen || colSet[k] == nil {
				colSet[k] = v
			}
		}
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	qSchema := pgx.Identifier{p.schema}.Sanitize()
	qTable := pgx.Identifier{p.schema, table}.Sanitize()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return errResult("begin failed: " + err.Error()), nil
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+qSchema); err != nil {
		return errResult("create schema failed: " + err.Error()), nil
	}
	colDefs := make([]string, len(cols))
	for i, c := range cols {
		colDefs[i] = pgx.Identifier{c}.Sanitize() + " " + pgType(colSet[c])
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", qTable, strings.Join(colDefs, ", "))); err != nil {
		return errResult("create table failed: " + err.Error()), nil
	}
	if mode == "replace" {
		if _, err := tx.Exec(ctx, "TRUNCATE "+qTable); err != nil {
			return errResult("truncate failed: " + err.Error()), nil
		}
	}

	// Build a batched parameterized INSERT.
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
		return errResult("insert failed: " + err.Error()), nil
	}

	if err := tx.Commit(ctx); err != nil {
		return errResult("commit failed: " + err.Error()), nil
	}

	// Best-effort: let the Supabase API roles read it. Done OUTSIDE the tx (on
	// the pool, autocommit) so a missing role on plain Postgres can't abort the
	// write — a failed statement aborts its whole transaction in Postgres.
	for _, role := range []string{"authenticated", "anon"} {
		_, _ = p.pool.Exec(ctx, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", qSchema, role))
		_, _ = p.pool.Exec(ctx, fmt.Sprintf("GRANT SELECT ON %s TO %s", qTable, role))
	}

	b, _ := json.Marshal(map[string]interface{}{
		"table": p.schema + "." + table, "rows_written": len(rowMaps), "columns": cols, "mode": mode,
		"written_at": time.Now().UTC().Format(time.RFC3339),
	})
	return textResult(string(b)), nil
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
