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

// D-2 (Contract 1 core: closed-world assembler + deletion manifest 1 + tail note)
// acceptance tests. These drive the real pkg/agent runtime — SegmentedMemory.
// GetMessagesForLLM (the sole assembler), SegmentedMemory's token-accounting sum, and (for
// the full-loop cases) Agent.Chat through an injectable LLMProvider, never a Claude SDK
// mock. They assert:
//   - assembler-rom-residue-l1-order: GetMessagesForLLM returns exactly
//     ROM(if present) + fold residue(if present) + L1, in that order, no other message.
//     ROM is the only system-role message; residue is a Role:"user" message carrying
//     the L2 summary — anything per-iteration-dynamic is a message, never system.
//   - system-byte-stability-l1-append-only: the system prefix (ROM only) is
//     byte-identical across every beat, including folds; L1 only grows.
//   - token-accounting-compiled-output-plus-tool-schema: reported budget usage is
//     ROM+residue+L1 tokens plus a tool-schema cost measured from the registered tool set
//     (SegmentedMemory.RecomputeToolSchema), cached until the set changes; no kernel-cache
//     (tool-result/schema-cache) tokens are counted; adding messages never touches the
//     tool-schema component.
//   - tail-note-single-user-role-local-only: a beat needing a soft reminder carries it as
//     one Role:"user" note appended to the loop's LOCAL messages slice for that beat only —
//     never via session.AddMessage, never system-role, never repeated on other beats.
//
// These tests bind to the seam this story defines (LLD "LLD" section):
//   - SegmentedMemory.GetMessagesForLLM() []Message — signature unchanged, body rewritten
//     to the three-part read (Seam 1).
//   - SegmentedMemory.RecomputeToolSchema(tools []shuttle.Tool) — D-2 stub/seam: measures
//     Σ CountTokens(json(tool.InputSchema())) over the live set into cachedToolSchemaTokens,
//     called only when the effective tool set changes (Seam 4).
//   - Agent.buildBeatTailNote(ctx, session, turnCount, toolExecutionCount) string — builds
//     the per-beat tail note from buildSoftReminder/buildTurnReminder (and, from D-2, any
//     graph-memory recall; D-6 adds the discovery menu) (Seam 3).

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// --- assembler-rom-residue-l1-order ---

func TestGetMessagesForLLM_AssemblerOrder(t *testing.T) {
	tests := []struct {
		name      string
		rom       string
		l2Summary string
		l1        []Message
	}{
		{name: "empty memory"},
		{name: "rom only", rom: "rom content"},
		{
			name: "rom + l1",
			rom:  "rom content",
			l1: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
		},
		{
			name:      "rom + fold residue + l1",
			rom:       "rom content",
			l2Summary: "prior conversation summary",
			l1:        []Message{{Role: "user", Content: "continue"}},
		},
		{
			name:      "fold residue + l1, no rom",
			l2Summary: "prior conversation summary",
			l1:        []Message{{Role: "user", Content: "continue"}},
		},
		{
			name: "l1 only, no rom, no residue (before any fold)",
			l1:   []Message{{Role: "user", Content: "hi"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSegmentedMemory(tt.rom, 200000, 20000)
			ctx := context.Background()
			for _, m := range tt.l1 {
				sm.AddMessage(ctx, m)
			}
			if tt.l2Summary != "" {
				sm.mu.Lock()
				sm.l2Summary = tt.l2Summary
				sm.mu.Unlock()
			}

			got := sm.GetMessagesForLLM()

			var want []Message
			if tt.rom != "" {
				want = append(want, Message{Role: "system", Content: tt.rom})
			}
			if tt.l2Summary != "" {
				want = append(want, Message{Role: "user", Content: "[Prior conversation summary]\n" + tt.l2Summary})
			}
			want = append(want, tt.l1...)

			require.Len(t, got, len(want),
				"GetMessagesForLLM must return exactly ROM(if present) + fold residue(if present) + L1, in that order, no other message")
			for i := range want {
				assert.Equal(t, want[i].Role, got[i].Role, "message %d role", i)
				assert.Equal(t, want[i].Content, got[i].Content, "message %d content", i)
			}
		})
	}
}

// --- system-byte-stability-l1-append-only ---

// systemPrefix returns the leading run of system-role messages (ROM only —
// residue is a user-role message, not system).
func systemPrefix(messages []Message) []Message {
	var i int
	for i < len(messages) && messages[i].Role == "system" {
		i++
	}
	return messages[:i]
}

func TestGetMessagesForLLM_ByteStability_ConsecutiveNonFoldBeats(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("stable rom content", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "beat one"})

	beat1 := sm.GetMessagesForLLM()
	require.NotEmpty(t, beat1)
	systemBeat1 := systemPrefix(beat1)
	require.NotEmpty(t, systemBeat1, "test setup: ROM must produce a system prefix")

	sm.AddMessage(ctx, Message{Role: "assistant", Content: "beat one reply"})
	sm.AddMessage(ctx, Message{Role: "user", Content: "beat two"})

	beat2 := sm.GetMessagesForLLM()
	systemBeat2 := systemPrefix(beat2)

	assert.Equal(t, systemBeat1, systemBeat2,
		"system bytes (ROM only) must be byte-identical across every beat, including folds")

	l1Beat1 := beat1[len(systemBeat1):]
	l1Beat2 := beat2[len(systemBeat2):]
	require.GreaterOrEqual(t, len(l1Beat2), len(l1Beat1), "L1 must only grow across non-fold beats")
	for i := range l1Beat1 {
		assert.Equal(t, l1Beat1[i], l1Beat2[i],
			"L1 message %d must be unchanged between beats — L1 is append-only outside a valve stub-in-place or fold restart", i)
	}
}

// --- token-accounting-compiled-output-plus-tool-schema ---

func TestSegmentedMemory_TokenAccounting_SumIsROMPlusL2PlusL1PlusToolSchema(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom content for accounting", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "hello there, this is a test message"})
	sm.RecomputeToolSchema([]shuttle.Tool{&mockCalculatorTool{}})

	used, _, _ := sm.GetTokenBudgetUsage()
	want := sm.cachedROMTokens + sm.cachedL2Tokens + sm.cachedL1Tokens + sm.cachedToolSchemaTokens

	assert.Equal(t, want, used,
		"reported usage must equal ROM+residue+L1 (the compiled output) plus the measured tool-schema cost — no other component")
	assert.Greater(t, sm.cachedToolSchemaTokens, 0,
		"the tool-schema cost must be measured from the registered tool set, not left at zero")
}

func TestSegmentedMemory_TokenAccounting_RecomputeToolSchema_MeasuresJSONSchemaTokens(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000)
	tool := &mockCalculatorTool{}

	sm.RecomputeToolSchema([]shuttle.Tool{tool})

	schemaJSON, err := json.Marshal(tool.InputSchema())
	require.NoError(t, err)
	want := GetTokenCounter().CountTokens(string(schemaJSON))

	assert.Equal(t, want, sm.cachedToolSchemaTokens,
		"tool-schema cost must be the sum of CountTokens(json(tool.InputSchema())) over the live set")
}

func TestSegmentedMemory_TokenAccounting_ToolSetChange_ChangesUsage(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000)

	sm.RecomputeToolSchema([]shuttle.Tool{&mockCalculatorTool{}, &mockFailingTool{}})
	withTwo := sm.cachedToolSchemaTokens
	require.Greater(t, withTwo, 0)

	sm.RecomputeToolSchema([]shuttle.Tool{&mockCalculatorTool{}})
	withOne := sm.cachedToolSchemaTokens
	assert.Less(t, withOne, withTwo, "excluding a tool from the registered set must lower the reported tool-schema cost")
	assert.Greater(t, withOne, 0)

	sm.RecomputeToolSchema(nil)
	assert.Equal(t, 0, sm.cachedToolSchemaTokens, "an empty registered tool set must cost zero")
}

func TestSegmentedMemory_TokenAccounting_AddingMessagesNeverChangesToolSchemaComponent(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 20000)
	sm.RecomputeToolSchema([]shuttle.Tool{&mockCalculatorTool{}})
	before := sm.cachedToolSchemaTokens
	require.Greater(t, before, 0)

	for i := 0; i < 20; i++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("message %d", i)})
	}

	assert.Equal(t, before, sm.cachedToolSchemaTokens,
		"adding messages must never recompute or change the tool-schema component — only register/exclude does")
}

func TestSegmentedMemory_TokenAccounting_NoKernelCacheTokensCounted(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000)
	before, _, _ := sm.GetTokenBudgetUsage()

	sm.AddToolResult(CachedToolResult{
		ToolName: "execute_sql",
		Args:     map[string]interface{}{"query": "SELECT 1"},
		Result:   "1 row returned",
	})
	sm.CacheSchema("users", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT)")

	after, _, _ := sm.GetTokenBudgetUsage()
	assert.Equal(t, before, after,
		"kernel-layer accounting (tool results / schema cache) must no longer contribute to reported budget usage — the LLM never receives these tokens")
}

func TestAgent_ToolRegistration_ChangesSessionToolSchemaTokens(t *testing.T) {
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	ctx := context.Background()

	_, err := ag.Chat(ctx, "tool-schema-session", "hello")
	require.NoError(t, err)
	session, ok := ag.memory.GetSession("tool-schema-session")
	require.True(t, ok)
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	assert.Zero(t, segMem.cachedToolSchemaTokens, "no tools registered yet — the tool-schema cost must be zero")

	ag.RegisterTool(&mockCalculatorTool{})
	_, err = ag.Chat(ctx, "tool-schema-session", "hello again")
	require.NoError(t, err)
	assert.Greater(t, segMem.cachedToolSchemaTokens, 0,
		"registering a tool must change the reported tool-schema cost — the recompute wiring reaches Agent.RegisterTool")

	ag.UnregisterTool("calculator")
	_, err = ag.Chat(ctx, "tool-schema-session", "hello once more")
	require.NoError(t, err)
	assert.Zero(t, segMem.cachedToolSchemaTokens,
		"unregistering the tool must lower the reported tool-schema cost back to zero")
}

// --- tail-note-single-user-role-local-only ---

// capturingLLM records the exact []Message slice each Chat call received, letting a test
// assert what the real LLM-bound call site actually received without ever persisting
// anything into the session itself — only agent.go's writers may do that.
type capturingLLM struct {
	mu              sync.Mutex
	calls           [][]Message
	alwaysCallTools bool
}

func (m *capturingLLM) Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	cp := make([]Message, len(messages))
	copy(cp, messages)
	m.calls = append(m.calls, cp)
	m.mu.Unlock()

	if m.alwaysCallTools {
		return &LLMResponse{
			ToolCalls: []ToolCall{{ID: "capturing", Name: "calculator", Input: map[string]interface{}{"expression": "1"}}},
			Usage:     Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	}
	return &LLMResponse{Content: "final response", Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (m *capturingLLM) Name() string  { return "capturing" }
func (m *capturingLLM) Model() string { return "capturing-v1" }

func (m *capturingLLM) getCalls() [][]Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]Message, len(m.calls))
	copy(out, m.calls)
	return out
}

// TestAgent_TailNote_TurnReminder_SingleUserRoleLocalOnly drives the real conversation loop
// end-to-end via Agent.Chat with an injectable LLMProvider (capturingLLM), never a Claude SDK
// mock. alwaysCallTools never emits end_turn, so the loop exhausts MaxTurns=10, guaranteeing
// turn 8 lands inside buildTurnReminder's [threshold=8, upperBound=9) window. That beat's
// call must carry the reminder as exactly one trailing Role:"user" message; no other
// captured call may carry it, and it must never be persisted into the session.
func TestAgent_TailNote_TurnReminder_SingleUserRoleLocalOnly(t *testing.T) {
	backend := &mockBackend{}
	llm := &capturingLLM{alwaysCallTools: true}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxTurns = 10
	cfg.MaxToolExecutions = 1000 // stay clear of the tool-execution reminder window

	ag := NewAgent(backend, llm, WithConfig(cfg))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "tail-note-session", "please help with something")
	require.NoError(t, err)

	calls := llm.getCalls()
	require.GreaterOrEqual(t, len(calls), 8, "the loop must reach turn 8 to exercise the turn-reminder window")

	const marker = "NOTICE: This conversation has progressed"

	turn8 := calls[7]
	require.NotEmpty(t, turn8)
	tail := turn8[len(turn8)-1]
	assert.Equal(t, "user", tail.Role, "the tail note must be user-role")
	assert.Contains(t, tail.Content, marker, "the turn-reminder content must reach the LLM as the beat's tail note")

	for i, call := range calls {
		if i == 7 {
			continue
		}
		for _, m := range call {
			assert.NotContains(t, m.Content, marker,
				"call %d must not carry turn 8's reminder — the tail note is local to its own beat, never accumulated or repeated", i)
		}
	}

	session, ok := ag.memory.GetSession("tail-note-session")
	require.True(t, ok)
	for _, m := range session.Messages {
		assert.NotContains(t, m.Content, marker, "the tail note must never be stored via session.AddMessage")
	}
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	for _, m := range segMem.GetMessages() {
		assert.NotContains(t, m.Content, marker, "the tail note must never land in L1 (segmented memory's persisted layer)")
	}
}
