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
	"unicode/utf8"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ExecuteQueryTool exposes the agent's fabric.ExecutionBackend as a SQL tool
// for local and benchmark work. The tool is backend-agnostic: duckdb,
// postgres, or any other ExecutionBackend serves it identically.
//
// The tool is constructed only by a caller that holds a backend: nothing
// builds it outside this package and the name is absent from ByName, so it
// reaches an agent only when a host wires it in deliberately. It serves
// benchmark harnesses against a local warehouse.
//
// Statements run as given, mutations included, because those harnesses build
// and change data. Its reach is one database; the shell tool's is the machine.
// Both are governed by which surfaces mount them.
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
	return `Run SQL batches against the project's warehouse. You are strongly advised to batch multiple statements together and run them in one call — every independent check (counts, distributions, verifications) in a single call to reduce cost.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *ExecuteQueryTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for the SQL probe batch",
		map[string]*shuttle.JSONSchema{
			"statements": {
				Type:        "array",
				Description: "Statements to run — batch every independent probe into one call. Each entry is {label, sql}.",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"label": shuttle.NewStringSchema("Short name for this check; heads its result section."),
						"sql":   shuttle.NewStringSchema("One SQL statement."),
					},
					Required: []string{"sql"},
				},
			},
			"row_limit": shuttle.NewNumberSchema("Maximum rows returned per statement (default 50). Prefer aggregation over raising it."),
		},
		[]string{"statements"},
	)
}

// queryStatement is one parsed entry of the statements batch.
type queryStatement struct {
	label string
	sql   string
}

// parseStatements extracts the batch, coercing the conventional shapes the
// schema does not advertise: a bare sql string parameter and bare-string
// array entries both become unlabeled statements. Trained habits get
// absorbed, not rejected.
func parseStatements(params map[string]interface{}) []queryStatement {
	var stmts []queryStatement
	if raw, ok := params["statements"].([]interface{}); ok {
		for _, r := range raw {
			switch v := r.(type) {
			case map[string]interface{}:
				label, _ := v["label"].(string)
				sqlText, _ := v["sql"].(string)
				stmts = append(stmts, queryStatement{label: label, sql: strings.TrimSpace(sqlText)})
			case string:
				stmts = append(stmts, queryStatement{sql: strings.TrimSpace(v)})
			}
		}
		return stmts
	}
	if sqlText, ok := params["sql"].(string); ok && strings.TrimSpace(sqlText) != "" {
		stmts = append(stmts, queryStatement{sql: strings.TrimSpace(sqlText)})
	}
	return stmts
}

// Execute parses the batch and delegates each statement to the backend. There
// is no read-only gate — see the type's doc comment.
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

	stmts := parseStatements(params)
	if len(stmts) == 0 {
		return fail("INVALID_PARAMS", "statements is required", "Provide statements: [{label, sql}, ...]")
	}
	if len(stmts) > maxQueryStatements {
		return fail("TOO_MANY_STATEMENTS",
			fmt.Sprintf("%d statements (max %d)", len(stmts), maxQueryStatements),
			"Split the batch — the per-statement render budget divides across the call")
	}

	rowLimit := 50
	if rl, ok := params["row_limit"].(float64); ok && rl > 0 {
		// A fractional limit floors to 0 and would render an empty result for
		// a statement that returned rows. One row is the smallest honest answer.
		rowLimit = int(rl)
		if rowLimit < 1 {
			rowLimit = 1
		}
	}

	// Per-statement render budget: the whole batch stays lean no matter how
	// many statements share the call. Explicit row_limit is respected — the
	// budget only bounds the default rendering.
	budget := stmtRenderBudget / len(stmts)
	if budget > stmtRenderBudgetMax {
		budget = stmtRenderBudgetMax
	}
	if budget < stmtRenderBudgetMin {
		budget = stmtRenderBudgetMin
	}

	// Each statement runs and reports independently: one bad statement costs
	// its own section, never the batch. Only an entirely failed batch carries
	// a typed Error (first failure's code) so retry logic keeps working.
	sections := make([]string, 0, len(stmts))
	failed := 0
	var firstErr *shuttle.Error
	for i, s := range stmts {
		head := s.label
		if head == "" {
			head = fmt.Sprintf("statement %d", i+1)
		}
		body, sErr := t.runOne(ctx, s.sql, rowLimit, budget)
		if sErr != nil {
			failed++
			if firstErr == nil {
				firstErr = sErr
			}
			body = fmt.Sprintf("ERROR: %s", sErr.Message)
		}
		if len(stmts) == 1 && s.label == "" {
			sections = append(sections, body)
		} else {
			sections = append(sections, fmt.Sprintf("== %s ==\n%s", head, body))
		}
	}
	res := &shuttle.Result{
		Success:         failed < len(stmts),
		Data:            strings.Join(sections, "\n\n"),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}
	if failed == len(stmts) {
		res.Error = firstErr
	}
	return res, nil
}

const (
	// stmtRenderBudget is the rendered-byte budget shared by a call's
	// statements; each statement gets an equal slice, clamped below.
	stmtRenderBudget = 14 * 1024
	// stmtRenderBudgetMax bounds a lone statement's slice.
	stmtRenderBudgetMax = 8 * 1024
	// stmtRenderBudgetMin keeps a slice useful in very large batches.
	stmtRenderBudgetMin = 2 * 1024
)

// runOne executes a single statement and renders its section body within the
// byte budget; a non-nil error marks a failed statement (rendered inline,
// isolated from the batch).
func (t *ExecuteQueryTool) runOne(ctx context.Context, sqlText string, rowLimit, budgetBytes int) (string, *shuttle.Error) {
	if sqlText == "" {
		return "", &shuttle.Error{Code: "INVALID_PARAMS", Message: "empty sql"}
	}
	res, err := t.backend.ExecuteQuery(ctx, sqlText)
	if err != nil {
		return "", &shuttle.Error{Code: "QUERY_FAILED", Message: err.Error()}
	}
	rows := res.Rows
	if len(rows) > rowLimit {
		rows = rows[:rowLimit]
	}
	cols := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		cols = append(cols, c.Name)
	}
	// Shrink shown rows until the rendered table fits the budget. Wide rows
	// converge in a few halvings; the footer always states what was cut.
	shown := rows
	for {
		out := renderQueryRows(cols, shown, len(res.Rows)-len(shown))
		if len(out) <= budgetBytes || len(shown) <= 5 {
			return out, nil
		}
		shown = shown[:len(shown)/2]
	}
}

// maxQueryStatements bounds one batch. Every statement runs, and the render
// budget divides across them, so an unbounded batch both takes unbounded time
// and squeezes every result to the budget floor.
const maxQueryStatements = 25

// queryCellMaxLen bounds a rendered cell — long text columns get elided, a
// probe needs the value's shape, not its entirety.
const queryCellMaxLen = 40

// renderQueryRows aligns columns for reading; NULL renders as NULL; omitted
// is the count of rows cut by the row limit (0 = complete result).
func renderQueryRows(cols []string, rows []map[string]interface{}, omitted int) string {
	cell := func(row map[string]interface{}, col string) string {
		v, ok := row[col]
		if !ok || v == nil {
			return "NULL"
		}
		s := fmt.Sprint(v)
		if len(s) > queryCellMaxLen {
			// Cut on a rune boundary: a byte-offset slice through a
			// multi-byte rune renders a broken character into the result.
			cut := queryCellMaxLen - 1
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			s = s[:cut] + "…"
		}
		return s
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
	if omitted > 0 {
		b.WriteString(fmt.Sprintf(" [+%d more not shown — narrow with WHERE or aggregate]", omitted))
	}
	return b.String()
}
