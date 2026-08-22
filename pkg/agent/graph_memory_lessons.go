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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Lesson mining (the ledger-grounded lane). Prose mid-conversation is where
// beliefs live: a fleet study measured per-turn extraction minting the WRONG
// error theory 58 times against the correct fix 8 — agents memorizing
// unverified hypotheses at high salience, then recalling themselves into the
// failure loop. Lessons therefore come from the one record beliefs cannot
// reach: the conversation's own tool ledger. A lesson candidate exists only
// where the ledger shows an error followed by a succeeding retry of the same
// kind of call — the fix demonstrably worked before the extractor ever sees
// it. This pass runs ONCE, at conversation end, over the full transcript.
const (
	// maxLessonPairs bounds the mining prompt; the highest-value transitions
	// are the earliest distinct ones.
	maxLessonPairs = 6
	// lessonInputPreview / lessonErrorPreview bound quoted ledger excerpts.
	lessonInputPreview = 400
	lessonErrorPreview = 300
	// lessonExtractionTimeout bounds the single mining LLM call.
	lessonExtractionTimeout = 30 * time.Second
	// lessonSalienceFloor: a verified lesson is always high-salience.
	lessonSalienceFloor = 0.8
)

// minedEvent is one tool execution as the miner sees it. Class key and
// previews are computed AT EXECUTION TIME: the compiled message view evicts
// old turns (measured: the long early overflow fight produced 3 lessons from
// the board's most common error while short late skirmishes produced 55), so
// the miner keeps its own ledger instead of trusting messages at the end.
type minedEvent struct {
	name    string
	key     string
	input   string
	ok      bool
	errText string
}

// maxLedgerEvents caps a session's mining ledger.
const maxLedgerEvents = 300

// recordToolLedger appends one execution to the session's mining ledger.
// Called from the conversation loop when the tool result lands. Previews are
// captured immediately — the params map can be mutated by later machinery.
func (a *Agent) recordToolLedger(sessionID string, tc types.ToolCall, result *shuttle.Result) {
	if !a.enableGraphMemoryExtraction || result == nil {
		return
	}
	ev := minedEvent{
		name:  tc.Name,
		key:   eventClass(tc),
		input: eventInputPreview(tc),
		ok:    result.Success && result.Error == nil,
	}
	if result.Error != nil {
		ev.errText = truncate(result.Error.Message, lessonErrorPreview)
	}
	a.toolLedgerMu.Lock()
	if a.toolLedgers == nil {
		a.toolLedgers = map[string][]minedEvent{}
	}
	if len(a.toolLedgers[sessionID]) < maxLedgerEvents {
		a.toolLedgers[sessionID] = append(a.toolLedgers[sessionID], ev)
	}
	a.toolLedgerMu.Unlock()
}

// takeToolLedger removes and returns the session's ledger (mining consumes
// it exactly once; sessions that never mine leak nothing past the map entry,
// which take also clears).
func (a *Agent) takeToolLedger(sessionID string) []minedEvent {
	a.toolLedgerMu.Lock()
	defer a.toolLedgerMu.Unlock()
	evs := a.toolLedgers[sessionID]
	delete(a.toolLedgers, sessionID)
	return evs
}

func eventClass(tc types.ToolCall) string {
	if sqlRaw, ok := tc.Input["sql"].(string); ok {
		return tc.Name + ":" + sqlClass(sqlRaw)
	}
	return tc.Name
}

func eventInputPreview(tc types.ToolCall) string {
	b, _ := json.Marshal(tc.Input)
	return truncate(string(b), lessonInputPreview)
}

// lessonPair is one error→verified-fix transition from the tool ledger.
type lessonPair struct {
	Tool       string
	FailingIn  string
	ErrorText  string
	SucceedsIn string
	// Intervening holds the successful calls made between the failure and
	// the success (most recent last, capped). When the failing and
	// succeeding inputs are near-identical, the fix lives HERE — e.g. an
	// INSERT that overflows, a DROP+CREATE with a widened column type, then
	// the same INSERT succeeding. Without these, cross-statement fixes are
	// invisible (measured: zero BIGINT lessons minted while the fix was
	// applied dozens of times, always via a re-CREATE).
	Intervening []string
}

// maxInterveningCalls bounds the between-failure-and-success excerpt; the
// fix is usually adjacent to the success, so the last few wins.
const maxInterveningCalls = 3

// mineLessonPairs walks the conversation's tool calls in order and pairs
// each failed call with the next SUCCEEDING call of the same kind (same
// tool; for SQL-bearing calls, same statement class — verb plus target).
// One pair per kind: the first failure against the fix that finally worked.
func mineLessonPairs(msgs []types.Message) []lessonPair {
	calls := map[string]types.ToolCall{} // ToolUseID → call
	var order []string                   // preserve call order for ID-less results
	var events []minedEvent
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if _, seen := calls[tc.ID]; !seen {
					order = append(order, tc.ID)
				}
				calls[tc.ID] = tc
			}
			continue
		}
		if m.Role != "tool" || m.ToolResult == nil {
			continue
		}
		tc, ok := calls[m.ToolUseID]
		if !ok {
			// ID-less transports: consume calls in order.
			if len(order) == 0 {
				continue
			}
			tc = calls[order[0]]
			order = order[1:]
		}
		errText := ""
		if m.ToolResult.Error != nil {
			errText = m.ToolResult.Error.Message
		}
		events = append(events, minedEvent{
			name:    tc.Name,
			key:     eventClass(tc),
			input:   eventInputPreview(tc),
			ok:      m.ToolResult.Success && m.ToolResult.Error == nil,
			errText: truncate(errText, lessonErrorPreview),
		})
	}
	return pairEvents(events)
}

// pairEvents pairs, per class, the earliest failure with the first later
// success, carrying the successful calls in between (the cross-statement fix
// path — e.g. a re-CREATE with a widened column type).
func pairEvents(events []minedEvent) []lessonPair {
	firstFailure := map[string]int{}
	emitted := map[string]bool{}
	var pairs []lessonPair
	for i, ev := range events {
		if !ev.ok {
			if _, have := firstFailure[ev.key]; !have {
				firstFailure[ev.key] = i
			}
			continue
		}
		fi, have := firstFailure[ev.key]
		if !have || emitted[ev.key] {
			continue
		}
		emitted[ev.key] = true
		var between []string
		for k := fi + 1; k < i; k++ {
			if events[k].ok {
				between = append(between, events[k].input)
			}
		}
		if len(between) > maxInterveningCalls {
			between = between[len(between)-maxInterveningCalls:]
		}
		pairs = append(pairs, lessonPair{
			Tool:        ev.name,
			FailingIn:   events[fi].input,
			ErrorText:   events[fi].errText,
			SucceedsIn:  ev.input,
			Intervening: between,
		})
		if len(pairs) >= maxLessonPairs {
			return pairs
		}
	}
	return pairs
}

// sqlClass reduces a SQL statement to verb + first target identifier, so a
// failing INSERT into a table pairs with the succeeding INSERT into the same
// table even when the statement text changed (that change IS the lesson).
func sqlClass(sql string) string {
	fields := strings.Fields(strings.ToUpper(sql))
	if len(fields) == 0 {
		return ""
	}
	verb := fields[0]
	for i, f := range fields {
		switch f {
		case "INTO", "TABLE", "FROM", "UPDATE":
			if i+1 < len(fields) {
				return verb + " " + strings.Trim(fields[i+1], "(,;")
			}
		}
	}
	return verb
}

// buildLessonMiningPrompt asks for one reusable cause→fix memory per
// verified transition. The evidence is the ledger excerpt itself, so the
// model is describing an observed fix, never endorsing a theory.
func buildLessonMiningPrompt(pairs []lessonPair) string {
	var sb strings.Builder
	sb.WriteString("Each numbered item below is a VERIFIED fix from a tool-execution ledger: a tool call ")
	sb.WriteString("that failed, the error it produced, and the later call of the same kind that succeeded. ")
	sb.WriteString("The fix demonstrably worked — your job is only to name it.\n\n")
	for i, p := range pairs {
		fmt.Fprintf(&sb, "%d. tool: %s\n   FAILED INPUT: %s\n   ERROR: %s\n", i+1, p.Tool, p.FailingIn, p.ErrorText)
		for _, b := range p.Intervening {
			fmt.Fprintf(&sb, "   CALL BETWEEN FAILURE AND SUCCESS: %s\n", b)
		}
		fmt.Fprintf(&sb, "   SUCCEEDED INPUT: %s\n\n", p.SucceedsIn)
	}
	sb.WriteString(`For each item, extract ONE memory stating the lesson in reusable, situation-independent form — the cause and the fix, not the story. Compare the failed and succeeded inputs: the difference between them is the fix. When the failed and succeeded inputs are identical or nearly identical, the fix happened in the CALLS BETWEEN FAILURE AND SUCCESS — name THAT change as the lesson. Example form: "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT".

Skip an item if the succeeded input does not actually address the error (e.g. it succeeded by abandoning the approach).

Return ONLY a JSON object:
{"entities": [], "relationships": [], "memories": [{"content": "cause and fix, one sentence", "summary": "short summary", "memory_type": "lesson", "tags": ["lesson"], "salience": 0.9, "entities": [], "event_date": "", "event_date_confidence": ""}]}
`)
	return sb.String()
}

// extractLessonsAtEnd runs the ledger-grounded lesson pass over the finished
// conversation. Fire-and-forget from the chat teardown via graphExtractionWG.
func (a *Agent) extractLessonsAtEnd(ctx context.Context, sessionID string) {
	if !a.enableGraphMemoryExtraction || a.graphMemoryStore == nil ||
		a.graphMemoryConfig == nil || !a.graphMemoryConfig.Enabled {
		return
	}
	var pairs []lessonPair
	if events := a.takeToolLedger(sessionID); len(events) > 0 {
		pairs = pairEvents(events)
	} else if session, ok := a.memory.GetSession(sessionID); ok && session != nil {
		// Fallback (restored sessions, ledgerless paths): the compiled view.
		// It under-reports long conversations — compilation evicts early
		// turns — which is exactly why the ledger is primary.
		pairs = mineLessonPairs(session.GetMessages())
	}
	if len(pairs) == 0 {
		return
	}

	extractCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lessonExtractionTimeout)
	defer cancel()

	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}
	response, err := llmProvider.Chat(extractCtx, []types.Message{
		{Role: "user", Content: buildLessonMiningPrompt(pairs)},
	}, nil)
	if err != nil {
		zap.L().Debug("lesson mining: LLM call failed", zap.Error(err))
		return
	}
	data, ok := parseExtractionResponse(response.Content)
	if !ok {
		return
	}

	agentID := a.config.Name
	stored := 0
	for _, m := range data.Memories {
		if m.Content == "" {
			continue
		}
		salience := m.Salience
		if salience < lessonSalienceFloor || salience > 1 {
			salience = lessonSalienceFloor
		}
		mem := &memory.Memory{
			AgentID:       agentID,
			Content:       m.Content,
			Summary:       m.Summary,
			MemoryType:    memory.MemoryTypeLesson,
			Source:        "lesson_mined",
			MemoryAgentID: agentID,
			Tags:          append(m.Tags, "lesson"),
			Salience:      salience,
		}
		if _, err := a.graphMemoryStore.Remember(extractCtx, mem); err == nil {
			stored++
		}
	}
	if stored > 0 {
		zap.L().Info("lesson mining: stored verified lessons",
			zap.String("agent", agentID),
			zap.Int("pairs", len(pairs)),
			zap.Int("lessons", stored))
	}
}
