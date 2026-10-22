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

// An edit survives interruption: the replacement is written to a sibling temp
// file and renamed, so the source is never a half-written file. The guarantee
// yields only where it cannot hold — a writable file inside a directory that
// is not writable still edits in place, as it did before.
func TestEditFilesWriteIsAtomicAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "y.txt")
	if err := os.WriteFile(f, []byte("a b c\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool("")
	if err := tool.applyOne(f, "b", "B"); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		for _, e := range ents {
			t.Logf("left behind: %s", e.Name())
		}
		t.Fatalf("temp file leaked: %d entries", len(ents))
	}
	st, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("permissions not preserved: %v", st.Mode().Perm())
	}
}

func TestEditFilesWritableFileInLockedDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "locked")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "x.txt")
	if err := os.WriteFile(f, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(sub, 0o755) }()

	tool := NewEditFilesTool("")
	if err := tool.applyOne(f, "world", "there"); err != nil {
		t.Fatalf("edit failed where it used to succeed: %v", err)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello there\n" {
		t.Fatalf("content = %q", string(b))
	}
}

// Editing a symlink edits the file it points at, and leaves the link a link.
// The atomic write renames onto the path, which would otherwise replace the
// link with a regular file and report success having changed nothing.
func TestEditFilesFollowsSymlinkToTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	if err := NewEditFilesTool("").applyOne(link, "world", "there"); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello there\n" {
		t.Errorf("target not edited: %q", string(b))
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
}
