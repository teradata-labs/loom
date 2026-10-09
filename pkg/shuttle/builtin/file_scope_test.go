// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The file tools reach the workspace, the loom dirs and temp — and nowhere
// else. A deny list of well-known files is not a boundary: "../../.." and a
// symlinked directory both walk straight out of one.
func TestResolveInScopeRefusesOutsideTheWorkspace(t *testing.T) {
	t.Setenv("LOOM_FILE_SAFE_MODE", "1")
	base := t.TempDir()

	if _, err := resolveInScope(base, "sub/file.txt"); err != nil {
		t.Errorf("a path inside the workspace was refused: %v", err)
	}
	if _, err := resolveInScope(base, filepath.Join(base, "x.txt")); err != nil {
		t.Errorf("an absolute path inside the workspace was refused: %v", err)
	}
	if _, err := resolveInScope(base, "../../../../../../etc/hosts"); err == nil {
		t.Error("a traversal out of the workspace was allowed")
	}
	if _, err := resolveInScope(base, "/etc/hosts"); err == nil {
		t.Error("an absolute path outside the workspace was allowed")
	}
	if _, err := resolveInScope(base, ""); err == nil {
		t.Error("an empty path was allowed")
	}
}

// A symlink inside the workspace cannot be used to step outside it: the test
// runs on the resolved path, not the one the caller wrote.
func TestResolveInScopeFollowsSymlinksBeforeDeciding(t *testing.T) {
	t.Setenv("LOOM_FILE_SAFE_MODE", "1")
	base := t.TempDir()
	outside := t.TempDir() // a sibling temp dir, inside TempDir's root
	link := filepath.Join(base, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := resolveInScope(base, "escape/hosts"); err == nil {
		t.Error("a symlink to /etc was traversed")
	}
	// A link to somewhere still in scope stays allowed.
	inScope := filepath.Join(base, "ok")
	if err := os.Symlink(outside, inScope); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := resolveInScope(base, "ok/file.txt"); err != nil {
		t.Errorf("a link to an in-scope directory was refused: %v", err)
	}
}

// Two edits to one file serialise, so neither read-modify-write cycle loses
// the other's change.
func TestEditFilesConcurrentEditsDoNotLose(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(f, []byte("alpha beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFilesTool(dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = tool.applyOne(f, "alpha", "ALPHA") }()
	go func() { defer wg.Done(); _ = tool.applyOne(f, "beta", "BETA") }()
	wg.Wait()

	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "ALPHA") || !strings.Contains(got, "BETA") {
		t.Errorf("an update was lost: %q", got)
	}
}

// Off by default: the boundary exists only in safe mode, because the product
// runs agents against their own working tree and an absolute path a caller
// holds is not an escape. Normalisation and the sensitive-path deny list still
// apply.
func TestResolveInScopeOffByDefault(t *testing.T) {
	t.Setenv("LOOM_FILE_SAFE_MODE", "")
	base := t.TempDir()

	abs, err := resolveInScope(base, "/etc/hosts")
	if err != nil {
		t.Errorf("default mode must not confine: %v", err)
	}
	if abs != "/etc/hosts" {
		t.Errorf("path should pass through normalised, got %q", abs)
	}
	if _, err := resolveInScope(base, ""); err == nil {
		t.Error("an empty path is still refused in either mode")
	}
	if !pathWithinScope(base, "/etc/hosts") {
		t.Error("the sweep test must not confine in default mode")
	}
}
