// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The snapshot path is pure and deterministic — persist and every later
// render must derive byte-identical tails — and hostile ToolUseIDs cannot
// escape the snapshot dir.
func TestToolSnapshotPath_DeterministicAndSanitized(t *testing.T) {
	assert.Equal(t, filepath.Join(".tool_results", "toolu_01AB_tmp_file"),
		ToolSnapshotPath(".tool_results", "toolu_01AB"))
	assert.Equal(t, ToolSnapshotPath("d", "x"), ToolSnapshotPath("d", "x"))
	assert.Equal(t, "", ToolSnapshotPath("", "toolu_01AB"), "empty dir disables")
	assert.Equal(t, "", ToolSnapshotPath(".tool_results", ""), "no id, no path")
	assert.Equal(t, filepath.Join("d", "------etc-passwd_tmp_file"),
		ToolSnapshotPath("d", "../../etc/passwd"), "path separators neutralized")
}

// With a snapshot path, the §4.1 tail cites the file as the one door; the
// bounded-row invariant (stored = core + tail ≤ threshold) still holds.
func TestTruncate_SnapshotTailCitesFileWithinBound(t *testing.T) {
	content := strings.Repeat("x", 5000)
	path := ToolSnapshotPath(".tool_results", "toolu_42")
	stored := truncateToolRowContent(content, 1024, path)
	require.LessOrEqual(t, len(stored), 1024)
	assert.Contains(t, stored, "full snapshot (temporary) at "+path)
	assert.True(t, strings.HasSuffix(stored, "Slice it with file_read/grep]"))
	assert.NotContains(t, stored, "re-run", "one door only — no re-run advice when the snapshot exists")
	// No path → the narrow-and-re-run tail, unchanged.
	assert.Contains(t, truncateToolRowContent(content, 1024, ""), "re-run to see more.]")
}

// writeToolSnapshot is write-once: the cited bytes stay what the call
// produced, even if a later caller tries to overwrite.
func TestWriteToolSnapshot_WriteOnce(t *testing.T) {
	dir := t.TempDir()
	path := ToolSnapshotPath(dir, "toolu_once")
	writeToolSnapshot(path, "first")
	writeToolSnapshot(path, "second")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))
}
