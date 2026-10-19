// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// truncationTailFormat is the normative truncation tail of write rule §4.1
// when no snapshot exists: the producing tool is the only door. Token figures
// are ceil(bytes/3).
const truncationTailFormat = "[truncated: %d of %d bytes shown (~%d of ~%d tokens) to prevent context pollution. Narrow the tool call (range, filter, LIMIT / aggregation) and re-run to see more.]"

// snapshotTailFormat is the §4.1 tail when the full result was spilled to a
// workspace snapshot at persist: one door, the file the agent already slices
// with its own tools. "(temporary)" and the _tmp_file suffix are the entire
// staleness hedge — a snapshot can be gone or stale on a later resume, and the
// tail's bytes can never be rewritten, so the tail must promise nothing more.
const snapshotTailFormat = "[truncated: %d of %d bytes shown; full snapshot (temporary) at %s. Slice it with file_read/grep]"

// tokenFigure is the byte→token figure used across the write rules and stubs:
// ceil(bytes/3) (HLD §4.1, §5.5).
func tokenFigure(bytes int) int {
	return (bytes + 2) / 3
}

// ToolSnapshotPath returns the workspace-relative snapshot file for a tool
// result, or "" when snapshots are disabled (empty dir) or the row has no
// ToolUseID. Pure and deterministic: persist and every later render must
// derive byte-identical tails from it, so cache breakpoints hold.
func ToolSnapshotPath(dir, toolUseID string) string {
	if dir == "" || toolUseID == "" {
		return ""
	}
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, toolUseID)
	return filepath.Join(dir, id+"_tmp_file")
}

// writeToolSnapshot spills the full content to path, write-once and
// best-effort: an existing file is never rewritten (the cited bytes must stay
// what the call produced), and any failure is swallowed — the tail's
// "(temporary)" hedge covers an absent file, while a persist must never fail
// on snapshot I/O.
func writeToolSnapshot(path, content string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(content), 0o644)
}

// TruncateToolRowContent is the exported §4.1 truncation for embedders whose
// own storage applies the row bound (cloud's SessionStorage). One
// implementation on purpose: the stored row and every render of it must stay
// byte-identical, so a second copy of this logic is a cache-invalidation bug
// waiting to happen.
func TruncateToolRowContent(content string, threshold int, snapshotPath string) string {
	return truncateToolRowContent(content, threshold, snapshotPath)
}

// SpillAndTruncateToolRow is the persist-seam §4.1 operation, for loom's own
// PersistMessage and for embedders whose storage is the persist seam: derive
// the snapshot path, spill the full bytes once (persist is the last moment
// they exist), and truncate with the citing tail. Empty dir or id → plain
// truncation, no I/O.
func SpillAndTruncateToolRow(content string, threshold int, dir, toolUseID string) string {
	path := ToolSnapshotPath(dir, toolUseID)
	if path != "" && threshold > 0 && len(content) > threshold {
		writeToolSnapshot(path, content)
	}
	return truncateToolRowContent(content, threshold, path)
}

// truncateToolRowContent applies write rule §4.1: every tool result row is
// bounded at threshold bytes stored, tail included. If the content exceeds the
// bound, keep the first threshold−len(tail) bytes of content (cut backward to a
// UTF-8 rune boundary) and append the tail, so stored = core + tail ≤ threshold
// — a truncated row must never itself exceed the threshold. A non-empty
// snapshotPath switches the tail to the snapshot door; it never writes — the
// spill happens once at the persist seam, the last moment the full bytes exist.
func truncateToolRowContent(content string, threshold int, snapshotPath string) string {
	if threshold <= 0 || len(content) <= threshold {
		return content
	}
	total := len(content)
	core := threshold
	for {
		var tail string
		if snapshotPath != "" {
			tail = fmt.Sprintf(snapshotTailFormat, core, total, snapshotPath)
		} else {
			tail = fmt.Sprintf(truncationTailFormat, core, total, tokenFigure(core), tokenFigure(total))
		}
		budget := threshold - len(tail)
		if budget < 0 {
			budget = 0
		}
		if core <= budget {
			return content[:core] + tail
		}
		core = budget
		// Cut backward to a UTF-8 rune boundary.
		for core > 0 && !utf8.RuneStart(content[core]) {
			core--
		}
	}
}
