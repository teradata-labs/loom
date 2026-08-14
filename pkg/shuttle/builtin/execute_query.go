// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ExecuteQueryTool exposes the agent's fabric.ExecutionBackend as a read-only
// query tool. The tool is backend-agnostic: duckdb, postgres, or any other
// ExecutionBackend serves it identically. Register it only when the agent has
// a real backend — absent beats broken.
type ExecuteQueryTool struct {
	backend fabric.ExecutionBackend
}

// NewExecuteQueryTool wraps the given backend.
func NewExecuteQueryTool(backend fabric.ExecutionBackend) *ExecuteQueryTool {
	return &ExecuteQueryTool{backend: backend}
}

// Name returns the tool name (matches the prompts/tools/sql.yaml id).
func (t *ExecuteQueryTool) Name() string { return "execute_query" }

// Backend returns "" — the concrete backend is injected, not looked up.
func (t *ExecuteQueryTool) Backend() string { return "" }

// Description returns the tool description.
func (t *ExecuteQueryTool) Description() string {
	return `Run a read-only SQL query against the project's warehouse. Use it to probe data before building and to verify what you built. Mutations are rejected — schema changes and loads go through dbt.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *ExecuteQueryTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for the SQL probe",
		map[string]*shuttle.JSONSchema{
			"sql":       shuttle.NewStringSchema("The read-only query: SELECT / WITH / SHOW / DESCRIBE / EXPLAIN."),
			"row_limit": shuttle.NewNumberSchema("Maximum rows to return (default 200)."),
		},
		[]string{"sql"},
	)
}

// Execute gates for read-only-ness and delegates to the backend.
func (t *ExecuteQueryTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	fail := func(code, msg, suggestion string) (*shuttle.Result, error) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       code,
				Message:    msg,
				Suggestion: suggestion,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	sqlText, _ := params["sql"].(string)
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return fail("INVALID_PARAMS", "sql is required", "Provide a SELECT query")
	}
	if err := checkReadOnly(sqlText); err != nil {
		return fail("READ_ONLY", err.Error(), "read-only: mutations go through dbt or shell")
	}

	rowLimit := 200
	if rl, ok := params["row_limit"].(float64); ok && rl > 0 {
		rowLimit = int(rl)
	}

	res, err := t.backend.ExecuteQuery(ctx, sqlText)
	if err != nil {
		return fail("QUERY_FAILED", err.Error(), "")
	}

	truncated := false
	rows := res.Rows
	if len(rows) > rowLimit {
		rows = rows[:rowLimit]
		truncated = true
	}
	cols := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		cols = append(cols, c.Name)
	}
	return &shuttle.Result{
		Success:         true,
		Data:            renderQueryRows(cols, rows, truncated),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// checkReadOnly enforces the probe contract: single statement, first keyword
// in the read-only set, and a WITH must contain no mutating keyword. Scans
// run on structural text only — quoted string literals are stripped first,
// so data values like 'DELETE' or 'a;b' never trip the gate.
func checkReadOnly(sqlText string) error {
	structural := stripSQLStringLiterals(strings.TrimSpace(sqlText))
	trimmed := strings.TrimRight(structural, "; \n\t")
	if strings.Contains(trimmed, ";") {
		return fmt.Errorf("multi-statement queries are rejected")
	}
	upper := strings.ToUpper(trimmed)
	fields := strings.Fields(upper)
	if len(fields) == 0 {
		return fmt.Errorf("empty query")
	}
	switch fields[0] {
	case "SELECT", "SHOW", "DESCRIBE", "EXPLAIN", "PRAGMA":
		return nil
	case "WITH":
		for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "MERGE", "CREATE", "DROP", "ALTER", "TRUNCATE", "COPY", "ATTACH"} {
			if containsSQLKeyword(upper, kw) {
				return fmt.Errorf("WITH query contains %s — only SELECT is allowed", kw)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s is not allowed — only SELECT / WITH / SHOW / DESCRIBE / EXPLAIN", fields[0])
	}
}

// containsSQLKeyword reports whether kw appears as a standalone word.
func containsSQLKeyword(upperSQL, kw string) bool {
	idx := 0
	for {
		i := strings.Index(upperSQL[idx:], kw)
		if i < 0 {
			return false
		}
		i += idx
		before := i == 0 || !isSQLWordChar(upperSQL[i-1])
		after := i+len(kw) >= len(upperSQL) || !isSQLWordChar(upperSQL[i+len(kw)])
		if before && after {
			return true
		}
		idx = i + len(kw)
	}
}

func isSQLWordChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// renderQueryRows aligns columns for reading; NULL renders as NULL.
func renderQueryRows(cols []string, rows []map[string]interface{}, truncated bool) string {
	cell := func(row map[string]interface{}, col string) string {
		v, ok := row[col]
		if !ok || v == nil {
			return "NULL"
		}
		return fmt.Sprint(v)
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
	}
	for _, r := range rows {
		for i, c := range cols {
			if l := len(cell(r, c)); l > widths[i] {
				widths[i] = l
			}
		}
	}
	var b strings.Builder
	for i, c := range cols {
		b.WriteString(fmt.Sprintf("%-*s  ", widths[i], c))
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i, c := range cols {
			b.WriteString(fmt.Sprintf("%-*s  ", widths[i], cell(r, c)))
		}
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("(%d rows)", len(rows)))
	if truncated {
		b.WriteString(" [truncated at row_limit — narrow with WHERE or aggregation]")
	}
	return b.String()
}

// stripSQLStringLiterals blanks out single-quoted literals (” escaping
// honored) so keyword and separator scans see only query structure.
func stripSQLStringLiterals(sqlText string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]
		if inString {
			if c == '\'' {
				// doubled '' is an escaped quote inside the literal
				if i+1 < len(sqlText) && sqlText[i+1] == '\'' {
					i++
					continue
				}
				inString = false
				b.WriteByte('\'')
			}
			continue
		}
		if c == '\'' {
			inString = true
			b.WriteByte('\'')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
