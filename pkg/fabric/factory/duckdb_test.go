// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func havePythonDuckDB() bool {
	if _, err := exec.LookPath("python3"); err != nil {
		return false
	}
	return exec.Command("python3", "-c", "import duckdb").Run() == nil
}

func seedDuckDB(t *testing.T, dbPath string) {
	t.Helper()
	seed := `
import duckdb
con = duckdb.connect("` + dbPath + `")
con.execute("create table hosts (id int, name varchar)")
con.execute("insert into hosts values (1,'a'),(2,NULL)")
con.close()
`
	if out, err := exec.Command("python3", "-c", seed).CombinedOutput(); err != nil {
		t.Fatalf("seed failed: %v %s", err, out)
	}
}

// The backend serves queries, schema, and listings from a duckdb file.
func TestDuckDBBackend(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dbPath := filepath.Join(t.TempDir(), "f.duckdb")
	seedDuckDB(t, dbPath)

	b, err := NewDuckDBBackend("test", dbPath)
	if err != nil {
		t.Fatalf("backend construction failed: %v", err)
	}
	ctx := context.Background()

	res, err := b.ExecuteQuery(ctx, "SELECT id, name FROM hosts ORDER BY id")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if res.RowCount != 2 || len(res.Columns) != 2 {
		t.Fatalf("unexpected result shape: %+v", res)
	}
	if res.Rows[1]["name"] != nil {
		t.Fatalf("NULL must arrive as nil, got %v", res.Rows[1]["name"])
	}

	schema, err := b.GetSchema(ctx, "hosts")
	if err != nil || len(schema.Fields) != 2 {
		t.Fatalf("schema failed: %v %+v", err, schema)
	}

	resources, err := b.ListResources(ctx, nil)
	if err != nil || len(resources) == 0 {
		t.Fatalf("list failed: %v", err)
	}

	// The read-only connection cannot mutate.
	if _, err := b.ExecuteQuery(ctx, "DROP TABLE hosts"); err == nil {
		t.Fatal("mutation must fail on a read-only connection")
	}
}

// Construction fails cleanly for a missing file — callers then skip the tool.
func TestDuckDBBackendMissingFile(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	if _, err := NewDuckDBBackend("test", filepath.Join(t.TempDir(), "absent.duckdb")); err == nil {
		t.Fatal("must fail for a missing database file")
	}
}

// The factory routes type=duckdb to the backend.
func TestFactoryDuckDB(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dbPath := filepath.Join(t.TempDir(), "f.duckdb")
	seedDuckDB(t, dbPath)
	backend, err := NewBackend(&loomv1.BackendConfig{
		Name: "bench",
		Type: "duckdb",
		Connection: &loomv1.BackendConfig_Database{
			Database: &loomv1.DatabaseConnection{Dsn: dbPath},
		},
	})
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if err := backend.Ping(context.Background()); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}

// Several files, no default: an in-memory session attaches each read-only
// under its filename stem; references are fully qualified.
func TestDuckDBBackendMultiAttach(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "alpha.duckdb")
	z := filepath.Join(dir, "zeta.duckdb")
	seedDuckDB(t, a)
	seed2 := `
import duckdb
con = duckdb.connect("` + z + `")
con.execute("create table orders (id int)")
con.execute("insert into orders values (10),(20)")
con.close()
`
	if out, err := exec.Command("python3", "-c", seed2).CombinedOutput(); err != nil {
		t.Fatalf("seed failed: %v %s", err, out)
	}

	b, err := NewDuckDBBackend("multi", a+","+z)
	if err != nil {
		t.Fatalf("backend failed: %v", err)
	}
	ctx := context.Background()

	// qualified references reach both databases
	res, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM alpha.main.hosts")
	if err != nil || res.Rows[0]["n"] != "2" {
		t.Fatalf("alpha query failed: %v %+v", err, res)
	}
	res, err = b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM zeta.main.orders")
	if err != nil || res.Rows[0]["n"] != "2" {
		t.Fatalf("zeta query failed: %v %+v", err, res)
	}

	// unqualified reference has no default database to land in
	if _, err := b.ExecuteQuery(ctx, "SELECT count(*) FROM hosts"); err == nil {
		t.Fatal("unqualified name must not resolve when several files are attached")
	}

	// schema lookup works catalog-qualified
	schema, err := b.GetSchema(ctx, "zeta.main.orders")
	if err != nil || len(schema.Fields) != 1 {
		t.Fatalf("qualified schema failed: %v %+v", err, schema)
	}
}
