// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/fabric/factory"
)

// Unsafe identifiers are rejected before any query is composed.
func TestTableProfileRejectsUnsafeNames(t *testing.T) {
	tool := NewTableProfileTool(nil)
	for _, bad := range []string{"t; DROP TABLE x", `a"b`, "t--", "a.b.c.d", ""} {
		res, _ := tool.Execute(context.Background(), map[string]interface{}{"table": bad})
		if res.Success {
			t.Errorf("unsafe name accepted: %q", bad)
		}
	}
}

// Live profile against a duckdb table seeded with the dirt classes the tool
// exists to surface: a frozen timestamp, a sentinel empty string, NULLs, and
// a low-cardinality flag.
func TestTableProfileLiveDuckDB(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if exec.Command("python3", "-c", "import duckdb").Run() != nil {
		t.Skip("python3-duckdb not available")
	}
	dbPath := filepath.Join(t.TempDir(), "p.duckdb")
	seed := `
import duckdb
con = duckdb.connect("` + dbPath + `")
con.execute("""create table hosts (
  id integer, name varchar, is_superhost varchar,
  created_at timestamp, updated_at timestamp)""")
con.execute("""insert into hosts values
  (1,'a','t',  '2019-01-01', '2025-06-01'),
  (2,'b','f',  '2020-05-10', '2025-06-01'),
  (3,'', 't',  '2021-07-20', '2025-06-01'),
  (4,NULL,'f', '2022-02-02', '2025-06-01')""")
con.close()
`
	if out, err := exec.Command("python3", "-c", seed).CombinedOutput(); err != nil {
		t.Fatalf("seed failed: %v %s", err, out)
	}
	backend, err := factory.NewDuckDBBackend("p", dbPath)
	if err != nil {
		t.Fatalf("backend failed: %v", err)
	}
	tool := NewTableProfileTool(backend)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"table": "hosts"})
	if err != nil || !res.Success {
		t.Fatalf("profile failed: %v %+v", err, res)
	}
	out := res.Data.(string)

	checks := map[string]string{
		"row count in header":            "TABLE hosts — 4 rows",
		"frozen timestamp visible":       "1",  // updated_at distinct=1 appears in its row
		"flag frequencies":               "'t' (2)",
		"sentinel empty string in freqs": "'' (1)",
		"sample section":                 "SAMPLE (4 rows)",
	}
	for label, want := range checks {
		if !strings.Contains(out, want) {
			t.Fatalf("%s: output missing %q\n%s", label, want, out)
		}
	}
	// updated_at's line must carry distinct=1 — the frozen-timestamp signal.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "updated_at") {
			if !strings.Contains(line, " 1 ") {
				t.Fatalf("updated_at line missing distinct=1: %q", line)
			}
		}
	}
	// name has one NULL — nulls column shows it.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "name") && !strings.Contains(line, "1") {
			t.Fatalf("name line missing null count: %q", line)
		}
	}

	// Missing table errors cleanly.
	res, _ = tool.Execute(context.Background(), map[string]interface{}{"table": "nope"})
	if res.Success {
		t.Fatal("missing table must fail")
	}

	// Batch: every named table profiles in one call; a bad name reports its
	// own section without failing the rest.
	if _, err := backend.ExecuteQuery(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("backend gone: %v", err)
	}
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"tables": []interface{}{"hosts", "nope", "hosts"},
	})
	if err != nil || !res.Success {
		t.Fatalf("batch with one bad table must still succeed: %v %+v", err, res)
	}
	out = res.Data.(string)
	if strings.Count(out, "TABLE hosts — 4 rows") != 2 {
		t.Fatalf("expected two hosts profiles:\n%s", out)
	}
	if !strings.Contains(out, "TABLE nope — PROFILE FAILED") {
		t.Fatalf("bad table missing its failure section:\n%s", out)
	}
}
