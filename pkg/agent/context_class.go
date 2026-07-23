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

import "github.com/teradata-labs/loom/pkg/shuttle"

// ContextClass is the structural retention class for a message under context
// pressure. Admission (wrapping), the valve (yellow-zone eviction), and fold
// (red-zone partitioning) key off this value rather than message age or role.
//
// ContextClass is an alias for string (not a defined type) so a class
// constant assigns directly into Message.ContextClass — which must stay a
// plain string in pkg/types to avoid a types->agent import cycle.
type ContextClass = string

// Class values mirror the canonical strings declared in pkg/shuttle, so the
// tagger here and the admission gate in pkg/shuttle/executor.go agree by
// construction (see shuttle.ContextClassHinter).
const (
	// ClassNarrative is the Go zero value: assistant messages, synthetic
	// user-role content (empty-response nudges, max-turn synthesis, tail
	// notes), and loader tool results (skill/pattern bodies — fold
	// summarizes them into residue).
	ClassNarrative ContextClass = shuttle.ClassNarrative

	// ClassCharter marks messages pinned as standing capability. No tool
	// classifies charter by default; a tool opts in via
	// shuttle.ContextClassHinter.
	ClassCharter ContextClass = shuttle.ClassCharter

	// ClassLedger marks messages that survive verbatim across a fold:
	// genuine user messages and contact_human results (out-of-band user
	// consent). A tool can opt in via shuttle.ContextClassHinter.
	ClassLedger ContextClass = shuttle.ClassLedger

	// ClassBallast is the default for tool results: data the LLM consumed
	// once, evictable by the valve and droppable by fold, recoverable via
	// recall_context.
	ClassBallast ContextClass = shuttle.ClassBallast
)

// loaderTools names the tools whose results carry skill/pattern bodies —
// classified narrative so fold's compressor rolls them into residue.
var loaderTools = map[string]bool{
	"manage_skills":   true,
	"manage_patterns": true,
}

// toolResultClass classifies a tool-result message: loaders → narrative,
// contact_human → ledger, everything else → ballast unless the live tool
// handle opts out via shuttle.ContextClassHinter (ledger/charter). A nil
// tool handle (skipped/deduplicated calls, legacy-row restore) classifies
// by name alone, so a hinter's opt-out is not recovered there.
func toolResultClass(toolName string, tool shuttle.Tool) ContextClass {
	// Skill/pattern loads carry executable instructions the LLM is following.
	// Narrative-classed so fold's LLM compressor summarizes them into residue
	// when pressure hits red (rather than pinning permanently as charter and
	// silently accumulating across load/unload cycles the LLM never triggers).
	// The compressor is designed to capture "state reached, decisions, open
	// commitments" — enough to resume a mid-workflow after fold.
	if loaderTools[toolName] {
		return ClassNarrative
	}
	// contact_human carries the user's out-of-band consent/answer — the same
	// forward-correctness weight as a user turn. Ledger, permanent.
	if toolName == "contact_human" {
		return ClassLedger
	}
	// All other tool results: information the LLM consumed at one point,
	// recoverable via the store if ever needed. Default ballast so valve can
	// reclaim under yellow-zone pressure. Explicit per-tool opt-out via
	// ContextClassHinter (retained but unused today; kept as an escape hatch
	// for a future tool that must be pinned as ledger).
	if hinter, ok := tool.(shuttle.ContextClassHinter); ok {
		hint := hinter.ContextClassHint()
		if hint == shuttle.ClassLedger || hint == shuttle.ClassCharter {
			return ContextClass(hint)
		}
	}
	return ClassBallast
}

// reclassifyMessages resolves every message with an empty (unpersisted or
// legacy) ContextClass to a concrete class, using the same structural rules
// applied at construction: user-role messages classify ledger (provenance
// isn't recoverable from a persisted row, so every empty user message is
// treated as the genuine, ledger-superset case — synthetic user-role rows
// over-retain as ledger instead of narrative, which costs tokens but never
// correctness); assistant messages classify narrative; tool messages recover
// their originating tool name by pairing ToolUseID to the preceding
// assistant message's ToolCalls, then classify via toolResultClass with no
// live tool handle (only the charter name rule can fire; ballast is never
// recovered this way — but a ballast result always persists its class as the
// explicit non-empty string "ballast", so it round-trips verbatim above this
// branch and never needs reclassification). Messages that already carry a
// persisted, non-empty class are returned unchanged.
func reclassifyMessages(messages []Message) []Message {
	for i := range messages {
		if messages[i].ContextClass != ClassNarrative {
			continue
		}
		switch messages[i].Role {
		case "user":
			messages[i].ContextClass = ClassLedger
		case "tool":
			toolName := precedingToolCallName(messages, i)
			messages[i].ContextClass = toolResultClass(toolName, nil)
		default:
			messages[i].ContextClass = ClassNarrative
		}
	}
	return messages
}

// precedingToolCallName walks backward from a tool-role message at index i to
// the assistant message that issued it, matching on ToolUseID against that
// assistant message's ToolCalls, and returns the originating tool's name.
// Returns "" if no matching call is found (toolResultClass then applies the
// ballast default).
func precedingToolCallName(messages []Message, i int) string {
	toolUseID := messages[i].ToolUseID
	if toolUseID == "" {
		return ""
	}
	for j := i - 1; j >= 0; j-- {
		if messages[j].Role != "assistant" {
			continue
		}
		for _, tc := range messages[j].ToolCalls {
			if tc.ID == toolUseID {
				return tc.Name
			}
		}
	}
	return ""
}
