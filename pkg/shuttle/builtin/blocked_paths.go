// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"path/filepath"
	"runtime"
	"strings"
)

// matchBlockedPath reports the first entry in blocked that path equals or sits
// under. Both sides are compared in slash-normalized form, because
// filepath.Clean renders the OS-native separator and the entries are written
// with forward slashes. Windows filesystems are case-insensitive, so matching
// folds case there; elsewhere it is exact.
func matchBlockedPath(path string, blocked []string) (string, bool) {
	return matchBlockedPathFold(path, blocked, runtime.GOOS == "windows")
}

func matchBlockedPathFold(path string, blocked []string, foldCase bool) (string, bool) {
	p := filepath.ToSlash(path)
	if foldCase {
		p = strings.ToLower(p)
	}
	for _, entry := range blocked {
		e := filepath.ToSlash(entry)
		if foldCase {
			e = strings.ToLower(e)
		}
		if p == e || strings.HasPrefix(p, e+"/") {
			return entry, true
		}
	}
	return "", false
}
