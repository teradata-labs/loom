// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editParams(edits ...map[string]interface{}) map[string]interface{} {
	raw := make([]interface{}, len(edits))
	for i, e := range edits {
		raw[i] = e
	}
	return map[string]interface{}{"edits": raw}
}

// One exact match is replaced; the rest of the file is untouched.
func TestEditFilesSingleEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.sql")
	if err := os.WriteFile(path, []byte("select a,\n  b_old,\n  c\nfrom t\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool(dir)
	res, err := tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "model.sql", "find": "b_old", "replace": "b_new"},
	))
	if err != nil || !res.Success {
		t.Fatalf("edit failed: %v %+v", err, res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "select a,\n  b_new,\n  c\nfrom t\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

// Edits apply in declared order — the second edit sees the first's result.
func TestEditFilesOrderedSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool(dir)
	res, _ := tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "f.txt", "find": "alpha", "replace": "beta"},
		map[string]interface{}{"path": "f.txt", "find": "beta", "replace": "gamma"},
	))
	if !res.Success {
		t.Fatalf("expected success: %+v", res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "gamma\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

// Zero matches and ambiguous matches fail that edit loudly, with the count.
func TestEditFilesExactlyOnceContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x\nx\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool(dir)

	res, _ := tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "f.txt", "find": "absent", "replace": "y"},
	))
	if res.Success {
		t.Fatal("zero-match edit must fail")
	}
	if !strings.Contains(res.Data.(string), "not found") {
		t.Fatalf("expected not-found report: %v", res.Data)
	}

	res, _ = tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "f.txt", "find": "x", "replace": "y"},
	))
	if res.Success {
		t.Fatal("ambiguous edit must fail")
	}
	if !strings.Contains(res.Data.(string), "matches 2 times") {
		t.Fatalf("expected ambiguity count: %v", res.Data)
	}
	// file untouched on failure
	got, _ := os.ReadFile(path)
	if string(got) != "x\nx\n" {
		t.Fatalf("failed edit must not modify the file: %q", got)
	}
}

// A failing edit does not stop the rest; call succeeds if any edit lands.
func TestEditFilesMixedResults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool(dir)
	res, _ := tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "missing.txt", "find": "one", "replace": "two"},
		map[string]interface{}{"path": "a.txt", "find": "one", "replace": "two"},
	))
	if !res.Success {
		t.Fatalf("partial success must report Success: %+v", res)
	}
	out := res.Data.(string)
	if !strings.Contains(out, "FAILED missing.txt") || !strings.Contains(out, "edited a.txt") {
		t.Fatalf("per-edit lines missing: %q", out)
	}
}

// Sensitive locations are rejected.
func TestEditFilesSensitivePath(t *testing.T) {
	tool := NewEditFilesTool("")
	res, _ := tool.Execute(context.Background(), editParams(
		map[string]interface{}{"path": "/etc/passwd", "find": "root", "replace": "boot"},
	))
	if res.Success {
		t.Fatal("sensitive path must fail")
	}
}
