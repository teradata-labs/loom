package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// sandboxHome points HOME at an empty directory inside the results tree, so
// a tool call that uses "~" cannot reach the operator's real home directory.
// The Go toolchain's cache and module paths are pinned to their real
// locations first, so the checker's `go test` keeps working. Both arms run
// under the same sandbox, so the setting cannot favour either arm.
//
// This is containment, not a security boundary: an agent can still read any
// absolute path the process can read. The results report this as a finding.
func sandboxHome(outDir string) error {
	real, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home directory: %w", err)
	}
	pins := map[string]string{
		"GOCACHE":    filepath.Join(real, "Library", "Caches", "go-build"),
		"GOMODCACHE": filepath.Join(real, "go", "pkg", "mod"),
		"GOPATH":     filepath.Join(real, "go"),
	}
	for k, v := range pins {
		if os.Getenv(k) == "" {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	home := filepath.Join(outDir, "sandbox-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	return os.Setenv("HOME", home)
}
