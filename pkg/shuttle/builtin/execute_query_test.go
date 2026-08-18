// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/fabric/factory"
)

// fakeSQLBackend returns canned rows and records the last query.
type fakeSQLBackend struct {
	fabric.ExecutionBackend
	lastQuery string
	rows      []map[string]interface{}
	cols      []fabric.Column
	err       error
}

func (f *fakeSQLBackend) ExecuteQuery(ctx context.Context, q string) (*fabric.QueryResult, error) {
	f.lastQuery = q
	if f.err != nil {
		return nil, f.err
	}
	return &fabric.QueryResult{Type: "rows", Rows: f.rows, Columns: f.cols, RowCount: len(f.rows)}, nil
}

// The read-only gate admits probes and rejects every mutation shape.
func TestExecuteQueryReadOnlyGate(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"select * from t where x = 1",
		"WITH a AS (SELECT 1) SELECT * FROM a",
		"EXPLAIN SELECT 1",
		"DESCRIBE t",
		"SHOW TABLES",
		"SELECT 1;", // trailing semicolon is fine
	}
	for _, q := range allowed {
		if err := checkReadOnly(q); err != nil {
			t.Errorf("should be allowed: %q → %v", q, err)
		}
	}
	rejected := []string{
		"INSERT INTO t VALUES (1)",
		"update t set x=1",
		"DELETE FROM t",
		"DROP TABLE t",
		"CREATE TABLE t (x int)",
		"WITH a AS (SELECT 1) DELETE FROM t",
		"WITH a AS (SELECT 1) INSERT INTO t SELECT * FROM a",
		"SELECT 1; DROP TABLE t",
	}
	for _, q := range rejected {
		if err := checkReadOnly(q); err == nil {
			t.Errorf("should be rejected: %q", q)
		}
	}
}

// The tool renders aligned rows with NULLs and delegates to the backend.
func TestExecuteQueryRendersRows(t *testing.T) {
	be := &fakeSQLBackend{
		cols: []fabric.Column{{Name: "id"}, {Name: "name"}},
		rows: []map[string]interface{}{
			{"id": "1", "name": "a"},
			{"id": "2", "name": nil},
		},
	}
	tool := NewExecuteQueryTool(be)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT id, name FROM hosts",
	})
	if err != nil || !res.Success {
		t.Fatalf("execute failed: %v %+v", err, res)
	}
	out := res.Data.(string)
	for _, want := range []string{"id", "name", "NULL", "(2 rows)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if be.lastQuery != "SELECT id, name FROM hosts" {
		t.Fatalf("query not delegated verbatim: %q", be.lastQuery)
	}
}

// row_limit truncates with a marker.
func TestExecuteQueryRowLimit(t *testing.T) {
	rows := make([]map[string]interface{}, 10)
	for i := range rows {
		rows[i] = map[string]interface{}{"id": fmt.Sprint(i)}
	}
	be := &fakeSQLBackend{cols: []fabric.Column{{Name: "id"}}, rows: rows}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT id FROM t", "row_limit": float64(3),
	})
	out := res.Data.(string)
	if !strings.Contains(out, "(3 rows)") || !strings.Contains(out, "+7 more not shown") {
		t.Fatalf("expected truncated output: %s", out)
	}
}

// A batch of labeled statements renders one section per label; one failing
// statement reports inline without failing the batch.
func TestExecuteQueryBatchSectionsAndIsolation(t *testing.T) {
	be := &fakeSQLBackend{
		cols: []fabric.Column{{Name: "n"}},
		rows: []map[string]interface{}{{"n": "7"}},
	}
	tool := NewExecuteQueryTool(be)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"statements": []interface{}{
			map[string]interface{}{"label": "row count", "sql": "SELECT count(*) AS n FROM t"},
			map[string]interface{}{"label": "bad", "sql": "DELETE FROM t"},
			map[string]interface{}{"sql": "SELECT count(*) AS n FROM u"},
		},
	})
	if err != nil || !res.Success {
		t.Fatalf("batch with one bad statement must still succeed: %v %+v", err, res)
	}
	out := res.Data.(string)
	for _, want := range []string{"== row count ==", "== bad ==", "ERROR:", "== statement 3 ==", "(1 rows)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// An entirely failed batch carries the first failure's typed error.
func TestExecuteQueryAllFailedCarriesError(t *testing.T) {
	be := &fakeSQLBackend{}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"statements": []interface{}{
			map[string]interface{}{"sql": "DROP TABLE a"},
			map[string]interface{}{"sql": "DELETE FROM b"},
		},
	})
	if res.Success || res.Error == nil || res.Error.Code != "READ_ONLY" {
		t.Fatalf("all-failed batch must carry typed error: %+v", res)
	}
}

// The unadvertised conventional shapes are absorbed: a bare sql parameter and
// bare-string array entries both run as statements.
func TestExecuteQueryCoercesConventionalForms(t *testing.T) {
	be := &fakeSQLBackend{cols: []fabric.Column{{Name: "x"}}, rows: []map[string]interface{}{{"x": "1"}}}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"statements": []interface{}{"SELECT 1 AS x"},
	})
	if !res.Success || !strings.Contains(res.Data.(string), "(1 rows)") {
		t.Fatalf("bare-string entry not coerced: %+v", res)
	}
	res, _ = tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT 1 AS x",
	})
	if !res.Success || strings.Contains(res.Data.(string), "== statement") {
		t.Fatalf("bare sql param must run unlabeled and unsectioned: %+v", res)
	}
}

// A mutation never reaches the backend.
func TestExecuteQueryMutationNeverDelegated(t *testing.T) {
	be := &fakeSQLBackend{}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"sql": "DROP TABLE hosts",
	})
	if res.Success || res.Error.Code != "READ_ONLY" {
		t.Fatalf("mutation must be gated: %+v", res)
	}
	if be.lastQuery != "" {
		t.Fatalf("mutation reached the backend: %q", be.lastQuery)
	}
}

// Backend errors surface as QUERY_FAILED.
func TestExecuteQueryBackendError(t *testing.T) {
	be := &fakeSQLBackend{err: fmt.Errorf("no such table: hosts")}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT * FROM hosts",
	})
	if res.Success || res.Error.Code != "QUERY_FAILED" {
		t.Fatalf("expected QUERY_FAILED: %+v", res)
	}
	if !strings.Contains(res.Error.Message, "no such table") {
		t.Fatalf("backend error not surfaced: %v", res.Error.Message)
	}
}

// End-to-end: the tool over a real DuckDBBackend against a real duckdb file.
func TestExecuteQueryLiveDuckDB(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if exec.Command("python3", "-c", "import duckdb").Run() != nil {
		t.Skip("python3-duckdb not available")
	}
	dbPath := filepath.Join(t.TempDir(), "live.duckdb")
	seed := `
import duckdb
con = duckdb.connect("` + dbPath + `")
con.execute("create table reviews (listing_id int, sentiment varchar)")
con.execute("insert into reviews values (1,'positive'),(1,'negative'),(2,NULL)")
con.close()
`
	if out, err := exec.Command("python3", "-c", seed).CombinedOutput(); err != nil {
		t.Fatalf("seed failed: %v %s", err, out)
	}
	backend, err := factory.NewDuckDBBackend("live", dbPath)
	if err != nil {
		t.Fatalf("backend failed: %v", err)
	}
	tool := NewExecuteQueryTool(backend)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT listing_id, count(*) AS n, count(sentiment) AS with_sentiment FROM reviews GROUP BY 1 ORDER BY 1",
	})
	if err != nil || !res.Success {
		t.Fatalf("live probe failed: %v %+v", err, res)
	}
	out := res.Data.(string)
	for _, want := range []string{"listing_id", "with_sentiment", "(2 rows)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	// Mutation: gated by the tool before the backend, and the backend's
	// read-only connection is the second wall.
	res, _ = tool.Execute(context.Background(), map[string]interface{}{
		"sql": "DELETE FROM reviews",
	})
	if res.Success || res.Error.Code != "READ_ONLY" {
		t.Fatalf("mutation must be gated: %+v", res)
	}

	// A wrong-table error surfaces the engine's message to the agent.
	res, _ = tool.Execute(context.Background(), map[string]interface{}{
		"sql": "SELECT * FROM nonexistent",
	})
	if res.Success || !strings.Contains(res.Error.Message, "nonexistent") {
		t.Fatalf("engine error not surfaced: %+v", res)
	}
}

// Quoted data values never trip the gate; real mutations still do.
func TestExecuteQueryGateIgnoresStringLiterals(t *testing.T) {
	allowed := []string{
		"WITH x AS (SELECT 1) SELECT * FROM t WHERE action = 'DELETE'",
		"SELECT 'a;b' AS v",
		"WITH x AS (SELECT 1) SELECT * FROM logs WHERE msg = 'DROP TABLE users'",
		"SELECT * FROM t WHERE note = 'it''s a DELETE; really'",
	}
	for _, q := range allowed {
		if err := checkReadOnly(q); err != nil {
			t.Errorf("literal tripped the gate: %q → %v", q, err)
		}
	}
	rejected := []string{
		"WITH x AS (SELECT 1) DELETE FROM t",
		"SELECT 1; DROP TABLE t",
	}
	for _, q := range rejected {
		if err := checkReadOnly(q); err == nil {
			t.Errorf("mutation passed the gate: %q", q)
		}
	}
}

// A statement whose rendered table would dominate the call is shrunk to its
// byte budget with the cut stated in the footer; small results are untouched.
func TestExecuteQueryRenderBudget(t *testing.T) {
	wide := strings.Repeat("x", 200)
	cols := make([]fabric.Column, 12)
	row := map[string]interface{}{}
	for i := range cols {
		cols[i] = fabric.Column{Name: fmt.Sprintf("c%d", i)}
		row[cols[i].Name] = wide
	}
	rows := make([]map[string]interface{}, 50)
	for i := range rows {
		rows[i] = row
	}
	be := &fakeSQLBackend{cols: cols, rows: rows}
	tool := NewExecuteQueryTool(be)
	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"statements": []interface{}{
			map[string]interface{}{"label": "wide", "sql": "SELECT * FROM w"},
			map[string]interface{}{"label": "wide2", "sql": "SELECT * FROM w"},
		},
	})
	out := res.Data.(string)
	if len(out) > 16*1024 {
		t.Fatalf("batch render exceeds offload-safe size: %d bytes", len(out))
	}
	if !strings.Contains(out, "more not shown") {
		t.Fatalf("budget cut not stated in footer:\n%s", out[:200])
	}
}
