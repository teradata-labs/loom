// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TableProfileTool returns a table's shape in one call: row count, per-column
// profile (type, nulls, distinct, min/max, value frequencies for
// low-cardinality columns) and sample rows. Exploration is doubt-driven — an
// agent probes what it already suspects; the profile is the opposite
// instrument: it surfaces the facts nobody thought to ask about (frozen
// timestamps, sentinel values, formats that don't match the column name).
// Backend-agnostic: composed entirely from ExecutionBackend primitives.
type TableProfileTool struct {
	backend fabric.ExecutionBackend
}

const (
	// profileMaxColumns bounds the profile for very wide tables.
	profileMaxColumns = 40
	// profileLowCardinality is the distinct-count threshold under which a
	// column's value frequencies are shown — flag/status/sentinel territory.
	profileLowCardinality = 10
	// profileMaxFreqColumns bounds how many columns get a frequency query.
	profileMaxFreqColumns = 6
	// profileSampleRows is the number of sample rows appended.
	profileSampleRows = 5
)

// NewTableProfileTool wraps the given backend.
func NewTableProfileTool(backend fabric.ExecutionBackend) *TableProfileTool {
	return &TableProfileTool{backend: backend}
}

// Name returns the tool name.
func (t *TableProfileTool) Name() string { return "table_profile" }

// Backend returns "" — the concrete backend is injected, not looked up.
func (t *TableProfileTool) Backend() string { return "" }

// Description returns the tool description.
func (t *TableProfileTool) Description() string {
	return `One call returns a table's shape: row count, per-column profile — type, nulls, distinct, min/max, value frequencies for low-cardinality columns — and 5 sample rows. Profile every table you will read from or build on, at survey time, before trusting any column's meaning. The profile shows what you would not think to ask: frozen timestamps, sentinel values, formats that don't match the column name. Follow up anything odd with execute_query.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *TableProfileTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for profiling a table",
		map[string]*shuttle.JSONSchema{
			"table": shuttle.NewStringSchema("Table or view name to profile."),
		},
		[]string{"table"},
	)
}

// safeIdentifier admits plain identifiers, schema-qualified (main.t) and
// catalog-qualified (db.main.t — attached-database form) names only;
// everything else is rejected rather than quoted into a query.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*){0,2}$`)

// Execute assembles the profile from backend primitives.
func (t *TableProfileTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	fail := func(code, msg string) (*shuttle.Result, error) {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: code, Message: msg},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	table, _ := params["table"].(string)
	table = strings.TrimSpace(table)
	if table == "" {
		return fail("INVALID_PARAMS", "table is required")
	}
	if !safeIdentifier.MatchString(table) {
		return fail("INVALID_PARAMS", fmt.Sprintf("%q is not a plain table name", table))
	}

	schema, err := t.backend.GetSchema(ctx, table)
	if err != nil {
		return fail("PROFILE_FAILED", err.Error())
	}
	fields := schema.Fields
	truncatedCols := false
	if len(fields) > profileMaxColumns {
		fields = fields[:profileMaxColumns]
		truncatedCols = true
	}

	// One aggregate query carries row count plus nulls/distinct/min/max for
	// every column. min/max only where the type supports ordering cheaply.
	type colStat struct {
		nulls, distinct string
		minVal, maxVal  string
		hasRange        bool
	}
	stats := make(map[string]*colStat, len(fields))
	var sel []string
	sel = append(sel, "count(*) AS __rows")
	for i, f := range fields {
		q := quoteIdent(f.Name)
		sel = append(sel,
			fmt.Sprintf("count(*) - count(%s) AS __n%d", q, i),
			fmt.Sprintf("count(DISTINCT %s) AS __d%d", q, i),
		)
		if typeHasRange(f.Type) {
			sel = append(sel,
				fmt.Sprintf("min(%s) AS __min%d", q, i),
				fmt.Sprintf("max(%s) AS __max%d", q, i),
			)
		}
	}
	aggRes, err := t.backend.ExecuteQuery(ctx,
		fmt.Sprintf("SELECT %s FROM %s", strings.Join(sel, ", "), quoteIdent(table)))
	if err != nil {
		return fail("PROFILE_FAILED", err.Error())
	}
	if len(aggRes.Rows) == 0 {
		return fail("PROFILE_FAILED", "profile query returned nothing")
	}
	agg := aggRes.Rows[0]
	rowCount := cellString(agg["__rows"])
	for i, f := range fields {
		st := &colStat{
			nulls:    cellString(agg[fmt.Sprintf("__n%d", i)]),
			distinct: cellString(agg[fmt.Sprintf("__d%d", i)]),
		}
		if typeHasRange(f.Type) {
			st.minVal = cellString(agg[fmt.Sprintf("__min%d", i)])
			st.maxVal = cellString(agg[fmt.Sprintf("__max%d", i)])
			st.hasRange = true
		}
		stats[f.Name] = st
	}

	// Value frequencies for low-cardinality columns — flags, statuses,
	// sentinels. Bounded in column count; ordered by frequency.
	freqs := make(map[string]string, profileMaxFreqColumns)
	freqCols := 0
	for _, f := range fields {
		if freqCols >= profileMaxFreqColumns {
			break
		}
		st := stats[f.Name]
		if !isSmallCount(st.distinct, profileLowCardinality) {
			continue
		}
		freqCols++
		q := quoteIdent(f.Name)
		fr, ferr := t.backend.ExecuteQuery(ctx, fmt.Sprintf(
			"SELECT %s AS v, count(*) AS n FROM %s GROUP BY 1 ORDER BY 2 DESC LIMIT %d",
			q, quoteIdent(table), profileLowCardinality))
		if ferr != nil {
			continue
		}
		parts := make([]string, 0, len(fr.Rows))
		for _, r := range fr.Rows {
			v := "NULL"
			if r["v"] != nil {
				v = fmt.Sprintf("'%v'", r["v"])
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", v, cellString(r["n"])))
		}
		freqs[f.Name] = strings.Join(parts, " · ")
	}

	sample, err := t.backend.ExecuteQuery(ctx,
		fmt.Sprintf("SELECT * FROM %s LIMIT %d", quoteIdent(table), profileSampleRows))
	if err != nil {
		return fail("PROFILE_FAILED", err.Error())
	}

	// Render.
	var b strings.Builder
	b.WriteString(fmt.Sprintf("TABLE %s — %s rows\n\n", table, rowCount))
	header := []string{"column", "type", "nulls", "distinct", "min", "max", "values"}
	rows := make([][]string, 0, len(fields))
	for _, f := range fields {
		st := stats[f.Name]
		minV, maxV := "", ""
		if st.hasRange {
			minV, maxV = st.minVal, st.maxVal
		}
		rows = append(rows, []string{f.Name, f.Type, st.nulls, st.distinct, minV, maxV, freqs[f.Name]})
	}
	writeAligned(&b, header, rows)
	if truncatedCols {
		b.WriteString(fmt.Sprintf("[%d more columns not shown]\n", len(schema.Fields)-profileMaxColumns))
	}

	b.WriteString(fmt.Sprintf("\nSAMPLE (%d rows)\n", len(sample.Rows)))
	sampleCols := make([]string, 0, len(sample.Columns))
	for _, c := range sample.Columns {
		sampleCols = append(sampleCols, c.Name)
	}
	b.WriteString(renderQueryRows(sampleCols, sample.Rows, false))

	return &shuttle.Result{
		Success:         true,
		Data:            b.String(),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// quoteIdent double-quotes an identifier (schema-qualified parts separately).
func quoteIdent(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = `"` + p + `"`
	}
	return strings.Join(parts, ".")
}

// typeHasRange reports whether min/max is meaningful and cheap for the type.
func typeHasRange(sqlType string) bool {
	tl := strings.ToLower(sqlType)
	for _, kw := range []string{"int", "decimal", "numeric", "double", "float", "real", "date", "time", "varchar", "char", "text", "string"} {
		if strings.Contains(tl, kw) {
			return true
		}
	}
	return false
}

// isSmallCount reports whether a numeric string is in (0, limit].
func isSmallCount(s string, limit int) bool {
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return false
	}
	return n > 0 && n <= limit
}

// cellString renders an aggregate cell, NULL-safe.
func cellString(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// writeAligned renders rows under a header with column alignment.
func writeAligned(b *strings.Builder, header []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i := range header {
			if i < len(r) && len(r[i]) > widths[i] {
				widths[i] = len(r[i])
			}
		}
	}
	for i, h := range header {
		b.WriteString(fmt.Sprintf("%-*s  ", widths[i], h))
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i := range header {
			v := ""
			if i < len(r) {
				v = r[i]
			}
			b.WriteString(fmt.Sprintf("%-*s  ", widths[i], v))
		}
		b.WriteString("\n")
	}
}
