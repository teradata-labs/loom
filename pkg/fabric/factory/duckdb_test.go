// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"context"
	"os"
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

// A SQL probe reaches the configured databases and nothing else: external
// access is sealed once the files are attached, so DuckDB's file readers
// cannot turn a query into an arbitrary filesystem read. Both connection
// shapes are covered — one path (read_only connect) and several (ATTACH).
func TestDuckDBBackendSealsFilesystem(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dir := t.TempDir()
	one := filepath.Join(dir, "alpha.duckdb")
	two := filepath.Join(dir, "zeta.duckdb")
	seedDuckDB(t, one)
	seedDuckDB(t, two)

	secret := filepath.Join(dir, "secret.csv")
	if err := os.WriteFile(secret, []byte("col\nleaked\n"), 0o600); err != nil {
		t.Fatalf("seed csv: %v", err)
	}
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		paths string
		live  string
	}{
		{"single path", one, "SELECT count(*) AS n FROM hosts"},
		{"multi path", one + "," + two, "SELECT count(*) AS n FROM alpha.main.hosts"},
	} {
		b, err := NewDuckDBBackend("seal", tc.paths)
		if err != nil {
			t.Fatalf("%s: backend failed: %v", tc.name, err)
		}
		// The configured databases stay queryable.
		if res, err := b.ExecuteQuery(ctx, tc.live); err != nil || res.Rows[0]["n"] != "2" {
			t.Fatalf("%s: configured database unreadable: %v %+v", tc.name, err, res)
		}
		// Everything else on disk is refused.
		for _, q := range []string{
			"SELECT * FROM read_csv('" + secret + "')",
			"SELECT * FROM read_csv_auto('" + secret + "')",
		} {
			if _, err := b.ExecuteQuery(ctx, q); err == nil {
				t.Errorf("%s: file read was allowed: %s", tc.name, q)
			}
		}
	}
}

// A catalog-qualified resource stays three identifiers, and two files sharing
// a stem each get their own alias.
func TestDuckDBBackendQualifiedMetadataAndAliasCollision(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dir := t.TempDir()
	one := filepath.Join(dir, "a", "shared.duckdb")
	two := filepath.Join(dir, "b", "shared.duckdb")
	for _, p := range []string{one, two} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		seedDuckDB(t, p)
	}

	b, err := NewDuckDBBackend("dup", one+","+two)
	if err != nil {
		t.Fatalf("backend failed: %v", err)
	}
	ctx := context.Background()

	// Both attachments are reachable: the second stem is suffixed.
	res, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM shared.main.hosts")
	if err != nil || res.Rows[0]["n"] != "2" {
		t.Fatalf("first attachment unreadable: %v %+v", err, res)
	}
	if res, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM shared_2.main.hosts"); err != nil ||
		res.Rows[0]["n"] != "2" {
		t.Fatalf("the colliding stem did not get its own alias: %v %+v", err, res)
	}

	// GetMetadata on a qualified name resolves rather than looking for one
	// identifier that happens to contain dots.
	meta, err := b.GetMetadata(ctx, "shared.main.hosts")
	if err != nil {
		t.Fatalf("qualified GetMetadata failed: %v", err)
	}
	if meta["row_count"] != "2" {
		t.Fatalf("row_count = %v, want 2", meta["row_count"])
	}
}

// DuckDB matches catalog names case-insensitively and reserves a few, so the
// alias de-dup folds case and a reserved stem takes a suffix.
func TestDuckDBBackendAliasCaseAndReservedNames(t *testing.T) {
	if !havePythonDuckDB() {
		t.Skip("python3-duckdb not available")
	}
	dir := t.TempDir()
	upper := filepath.Join(dir, "a", "Sales.duckdb")
	lower := filepath.Join(dir, "b", "sales.duckdb")
	reserved := filepath.Join(dir, "c", "main.duckdb")
	for _, p := range []string{upper, lower, reserved} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		seedDuckDB(t, p)
	}

	b, err := NewDuckDBBackend("aliases", upper+","+lower+","+reserved)
	if err != nil {
		t.Fatalf("backend failed: %v", err)
	}
	ctx := context.Background()

	// All three attach: the case-colliding stem and the reserved one are
	// suffixed rather than failing the whole session.
	res, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM Sales.main.hosts")
	if err != nil || res.Rows[0]["n"] != "2" {
		t.Fatalf("first attachment unreadable: %v %+v", err, res)
	}
	if _, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM sales_2.main.hosts"); err != nil {
		t.Fatalf("case-colliding stem did not get its own alias: %v", err)
	}
	if _, err := b.ExecuteQuery(ctx, "SELECT count(*) AS n FROM main_2.main.hosts"); err != nil {
		t.Fatalf("reserved stem was not suffixed: %v", err)
	}
}
