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
	"unicode/utf8"
)

// truncationTailFormat is the normative truncation tail of write rule §4.1:
// the producing tool is the door to the rest. Token figures
// are ceil(bytes/3).
const truncationTailFormat = "[truncated: %d of %d bytes shown (~%d of ~%d tokens) to prevent context pollution. Narrow the tool call (range, filter, LIMIT / aggregation) and re-run to see more.]"

// pageableTailFormat is the §4.1 tail when query_tool_result can serve the
// whole result: it names the result by its tool_use_id, which exists before
// the row is written, so the stored row and every render carry the same bytes.
const pageableTailFormat = "[truncated: %d of %d bytes shown (~%d of ~%d tokens). The whole result is held for this turn only: page it with query_tool_result tool_use_id=%s (offset, limit). After this turn, narrow the tool call and re-run.]"

// tokenFigure is the byte→token figure used across the write rules and stubs:
// ceil(bytes/3) (HLD §4.1, §5.5).
func tokenFigure(bytes int) int {
	return (bytes + 2) / 3
}

// truncateToolRowContent applies write rule §4.1: every tool result row is
// bounded at threshold bytes stored, tail included. If the content exceeds the
// bound, keep the first threshold−len(tail) bytes of content (cut backward to a
// UTF-8 rune boundary) and append the tail, so stored = core + tail ≤ threshold
// — a truncated row must never itself exceed the threshold.
func truncateToolRowContent(content string, threshold int) string {
	return boundToolRow(content, threshold, "")
}

// boundToolRow is truncateToolRowContent with the tail that names
// query_tool_result when toolUseID is non-empty. A threshold too small to hold
// that tail falls back to the plain one, so the bound always holds.
func boundToolRow(content string, threshold int, toolUseID string) string {
	if threshold <= 0 || len(content) <= threshold {
		return content
	}
	total := len(content)
	tailOf := func(core int) string {
		if toolUseID != "" {
			return fmt.Sprintf(pageableTailFormat, core, total, tokenFigure(core), tokenFigure(total), toolUseID)
		}
		return fmt.Sprintf(truncationTailFormat, core, total, tokenFigure(core), tokenFigure(total))
	}
	if toolUseID != "" && len(tailOf(threshold)) >= threshold {
		return boundToolRow(content, threshold, "")
	}
	core := threshold
	for {
		tail := tailOf(core)
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
