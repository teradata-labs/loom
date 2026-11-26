// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"os"
	"path/filepath"
	"runtime"
)

// outsideDir is an absolute system directory on the running OS that is outside
// every file-scope root and on the sensitive-path list. A literal "/etc" is not
// absolute on Windows, where it would be joined under the workspace instead.
func outsideDir() string {
	if runtime.GOOS != "windows" {
		return "/etc"
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32")
}

// outsidePath is a file under outsideDir.
func outsidePath(name string) string {
	return filepath.Join(outsideDir(), name)
}
