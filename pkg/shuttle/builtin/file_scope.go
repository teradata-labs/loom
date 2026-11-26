// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/teradata-labs/loom/pkg/config"
)

// resolveInScope turns a caller-supplied path into an absolute path and refuses
// it — IN SAFE MODE — unless it lands inside a root the file tools are allowed
// to reach: the tool's own baseDir, the agent's sandbox or data directory, or
// the system temp directory. isSensitivePath alone is a deny list, so
// "../../.." and a glob through a symlinked directory reach anything the
// process can read or write.
//
// The boundary is OFF unless LOOM_FILE_SAFE_MODE is set. The product runs
// agents autonomously against their own working tree — permissions default to
// YOLO for the same reason — and a boundary drawn around the process's cwd
// would refuse an absolute path a caller legitimately holds. Safe mode is for
// a deployment that does not extend the agent that trust. Normalisation
// happens either way, and isSensitivePath applies either way.
//
// Symlinks are resolved before the test, so a link inside the workspace cannot
// be used to step outside it. A path that does not exist yet resolves through
// its nearest existing parent, which is what a create needs.
//
// The allowed roots deliberately match the shell tool's: these tools reach the
// same places it does, no further.
func resolveInScope(baseDir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs := filepath.Clean(path)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, abs)
	}
	if !config.FileScopeEnforced() {
		return abs, nil
	}
	resolved := resolveExistingPrefix(abs)

	for _, root := range scopeRoots(baseDir) {
		if pathWithin(resolved, root) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path is outside the workspace (LOOM_FILE_SAFE_MODE is on)")
}

// scopeRoots lists the directories the file tools may reach, each resolved
// through symlinks so a root given as a link still matches its real target.
func scopeRoots(baseDir string) []string {
	candidates := []string{baseDir, config.GetLoomSandboxDir(), config.GetLoomDataDir()}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, os.Getenv("TEMP"))
	} else {
		candidates = append(candidates, "/tmp", os.TempDir())
	}
	roots := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		roots = append(roots, resolveExistingPrefix(abs))
	}
	return roots
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of
// path and re-appends the rest. EvalSymlinks fails outright on a path whose
// leaf does not exist, which every create would hit.
func resolveExistingPrefix(path string) string {
	rest := ""
	cur := filepath.Clean(path)
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// pathWithinScope reports whether an already-absolute path sits in scope. Used
// by the sweeps, which expand many paths and need the test without the
// normalisation.
func pathWithinScope(baseDir, abs string) bool {
	if !config.FileScopeEnforced() {
		return true
	}
	resolved := resolveExistingPrefix(abs)
	for _, root := range scopeRoots(baseDir) {
		if pathWithin(resolved, root) {
			return true
		}
	}
	return false
}
