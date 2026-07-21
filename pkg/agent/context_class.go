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
	// ClassNarrative is the default class (the Go zero value): assistant
	// messages and synthetic user-role content (empty-response nudges,
	// max-turn synthesis, tail notes).
	ClassNarrative ContextClass = shuttle.ClassNarrative

	// ClassCharter marks messages that install standing capability — loader
	// tool results (manage_skills, manage_patterns).
	ClassCharter ContextClass = shuttle.ClassCharter

	// ClassLedger marks messages that must survive verbatim across a fold:
	// genuine user messages and every tool result that is neither charter
	// nor an opt-in ballast whitelist match (mutating tools, contact_human,
	// and any tool without a read-only hint all fail safe to ledger).
	ClassLedger ContextClass = shuttle.ClassLedger

	// ClassBallast marks whitelisted read-only tool results. A tool must
	// opt in via shuttle.ContextClassHinter; never assigned by default.
	ClassBallast ContextClass = shuttle.ClassBallast
)

// loaderTools is the charter-class whitelist: tools that install standing
// capability (skills/patterns) rather than returning data. Structural, not
// configurable — the opposite of the ballast whitelist, which tools opt
// into individually via shuttle.ContextClassHinter.
var loaderTools = map[string]bool{
	"manage_skills":   true,
	"manage_patterns": true,
}

// toolResultClass classifies a tool-result message by tool name and, if a
// live tool handle is available and implements shuttle.ContextClassHinter,
// its opt-in read-only hint. Whitelist only, never a blacklist: absent a
// charter name or a ballast hint, the result classifies ledger — fail-safe
// retention for mutating tools, contact_human, and any tool this process
// cannot look up (e.g. skipped/deduplicated calls, where the call never
// reached tool resolution, or legacy-row restore, where no tool instance
// is available and only the name is recoverable).
func toolResultClass(toolName string, tool shuttle.Tool) ContextClass {
	if loaderTools[toolName] {
		return ClassCharter
	}
	if hinter, ok := tool.(shuttle.ContextClassHinter); ok {
		if hinter.ContextClassHint() == shuttle.ClassBallast {
			return ClassBallast
		}
	}
	return ClassLedger
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
// Returns "" if no matching call is found (classifies ledger, fail-safe).
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
