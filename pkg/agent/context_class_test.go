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

// D-3 (Context classification & persistence, Part A) component acceptance tests.
//
// These tests drive the real pkg/agent runtime — Agent.Chat/ChatWithProgress through an
// injectable LLMProvider (never a Claude SDK mock), plus the pure functions
// reclassifyMessages and toolResultClass directly — and assert Message.ContextClass:
//   - AC-1: genuine user messages (Chat/ChatWithProgress) classify ledger; synthetic
//     user-role messages (empty-response nudge, max-turn synthesis) classify narrative;
//     assistant messages stay narrative.
//   - AC-2: tool-result class dispatch (toolResultClass) at all three Role:"tool"
//     construction sites — the real tool result, the per-turn-cap skip control message,
//     and the same-turn dedup control message.
//   - AC-4: reclassifyMessages applies the structural rules to column-less (legacy) rows
//     and leaves persisted non-empty classes untouched.
//   - AC-5: classification is a function of class, not age — reclassifyMessages yields the
//     same class for structurally identical rows regardless of Timestamp.
//
// These tests bind to the seam this story defines (LLD "Contracts" section):
//   - Message.ContextClass string — "" (narrative), "charter", "ledger", "ballast"
//   - Named constants ClassNarrative/ClassCharter/ClassLedger/ClassBallast
//   - func toolResultClass(toolName string, tool shuttle.Tool) ContextClass
//   - func reclassifyMessages(msgs []Message) []Message
//   - shuttle.ContextClassHinter { ContextClassHint() string } (pkg/shuttle/tool.go)

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// --- test doubles ---

// mockNamedTool implements shuttle.Tool with a configurable name and no
// ContextClassHinter — used to exercise toolResultClass's name-based rules
// (loader names, and the fail-safe ledger default for everything else,
// including mutating tools and contact_human).
type mockNamedTool struct {
	toolName string
}

func (m *mockNamedTool) Name() string        { return m.toolName }
func (m *mockNamedTool) Description() string { return "mock tool for context-class dispatch tests" }
func (m *mockNamedTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (m *mockNamedTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (m *mockNamedTool) Backend() string { return "" }

var _ shuttle.Tool = (*mockNamedTool)(nil)

// mockHintedTool additionally implements shuttle.ContextClassHinter, simulating a
// whitelisted read-only data tool (opt-in ballast hint) or any other opt-in hint value.
type mockHintedTool struct {
	mockNamedTool
	hint string
}

func (m *mockHintedTool) ContextClassHint() string { return m.hint }

var _ shuttle.ContextClassHinter = (*mockHintedTool)(nil)

// --- toolResultClass: pure dispatch function (AC-2) ---

func TestToolResultClass_LoaderToolNames_Narrative(t *testing.T) {
	// v5 correction: skill/pattern load results carry executable instructions
	// the LLM follows. Under the previous charter classification these were
	// pinned in L1 forever. Narrative-classed so fold's LLM compressor
	// summarizes them into residue under pressure.
	for _, name := range []string{"manage_skills", "manage_patterns"} {
		t.Run(name, func(t *testing.T) {
			got := toolResultClass(name, &mockNamedTool{toolName: name})
			assert.Equal(t, ClassNarrative, got, "loader tool %q must classify narrative so fold can summarize into residue", name)
		})
	}
}

func TestToolResultClass_LoaderToolNames_Narrative_NilTool(t *testing.T) {
	for _, name := range []string{"manage_skills", "manage_patterns"} {
		t.Run(name, func(t *testing.T) {
			got := toolResultClass(name, nil)
			assert.Equal(t, ClassNarrative, got, "loader tool %q must classify narrative with a nil tool handle", name)
		})
	}
}

func TestToolResultClass_WhitelistedReadOnlyHint_Ballast(t *testing.T) {
	// Under the new default all non-loader non-contact_human tools are
	// ballast; a hinter returning "ballast" is redundant with the default.
	tool := &mockHintedTool{mockNamedTool: mockNamedTool{toolName: "read_only_data"}, hint: "ballast"}
	got := toolResultClass("read_only_data", tool)
	assert.Equal(t, ClassBallast, got, "a tool whose ContextClassHint() returns \"ballast\" must classify ballast")
}

func TestToolResultClass_MutatingTool_Ballast(t *testing.T) {
	// v5 correction: mutating tools no longer classify ledger by default.
	// Context visibility does not prevent double-execution — that's a
	// tool-idempotency / approval-gate concern. All non-loader non-contact_human
	// tool results (mutating or not) classify ballast; opt out via
	// ContextClassHinter returning ledger if a specific tool needs pinning.
	tool := &mockNamedTool{toolName: "delete_records"}
	got := toolResultClass("delete_records", tool)
	assert.Equal(t, ClassBallast, got, "under the new default, mutating tools without a ledger opt-out classify ballast")
}

func TestToolResultClass_ContactHuman_Ledger(t *testing.T) {
	tool := &mockNamedTool{toolName: "contact_human"}
	got := toolResultClass("contact_human", tool)
	assert.Equal(t, ClassLedger, got, "contact_human must classify ledger (user consent, permanent)")
}

func TestToolResultClass_UnknownToolWithoutHint_Ballast(t *testing.T) {
	// Under the new default, every non-loader non-contact_human tool result
	// is ballast so valve reclaims it under yellow-zone pressure.
	got := toolResultClass("some_random_tool", &mockNamedTool{toolName: "some_random_tool"})
	assert.Equal(t, ClassBallast, got)
}

func TestToolResultClass_NoLiveToolHandle_Ballast(t *testing.T) {
	// Restore-time (tool==nil) still reaches the same default: ballast.
	got := toolResultClass("read_only_data", nil)
	assert.Equal(t, ClassBallast, got)
}

func TestToolResultClass_HintOptsOutToLedgerOrCharter(t *testing.T) {
	// The hint is now an opt-OUT from the ballast default. "ledger" and
	// "charter" opt out; "ballast" reaches ballast redundantly; anything
	// else falls through to the default.
	optOutLedger := &mockHintedTool{mockNamedTool: mockNamedTool{toolName: "audit_log"}, hint: "ledger"}
	assert.Equal(t, ClassLedger, toolResultClass("audit_log", optOutLedger),
		"hint \"ledger\" opts the tool out of the ballast default")
	optOutCharter := &mockHintedTool{mockNamedTool: mockNamedTool{toolName: "pinned_tool"}, hint: "charter"}
	assert.Equal(t, ClassCharter, toolResultClass("pinned_tool", optOutCharter),
		"hint \"charter\" opts the tool out of the ballast default")
	unknownHint := &mockHintedTool{mockNamedTool: mockNamedTool{toolName: "weird"}, hint: "narrative"}
	assert.Equal(t, ClassBallast, toolResultClass("weird", unknownHint),
		"any hint value other than the four classes falls through to the ballast default")
}

// --- contract: named constants match the persisted string values (LLD Seam 1) ---

func TestContextClassConstants_MatchContract(t *testing.T) {
	assert.Equal(t, "", string(ClassNarrative), "narrative is the zero value")
	assert.Equal(t, "charter", string(ClassCharter))
	assert.Equal(t, "ledger", string(ClassLedger))
	assert.Equal(t, "ballast", string(ClassBallast))
}

// --- AC-1: construction-site tagging via the real Agent.Chat/ChatWithProgress surface ---

func newContextClassTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false // deterministic mock LLM behavior
	return cfg
}

func TestContextClass_GenuineUserMessage_Chat_Ledger(t *testing.T) {
	llm := &mockSimpleLLM{}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))

	ctx := context.Background()
	_, err := ag.Chat(ctx, "genuine-user-chat", "hello there")
	require.NoError(t, err)

	session, ok := ag.memory.GetSession("genuine-user-chat")
	require.True(t, ok)

	var sawUser, sawAssistant bool
	for _, msg := range session.Messages {
		switch msg.Role {
		case "user":
			sawUser = true
			assert.Equal(t, ClassLedger, ContextClass(msg.ContextClass), "genuine user message (Chat) must classify ledger")
		case "assistant":
			sawAssistant = true
			assert.Equal(t, ClassNarrative, ContextClass(msg.ContextClass), "assistant messages must stay narrative")
		}
	}
	assert.True(t, sawUser, "test setup: conversation must contain a user message")
	assert.True(t, sawAssistant, "test setup: conversation must contain an assistant message")
}

func TestContextClass_GenuineUserMessage_ChatWithProgress_Ledger(t *testing.T) {
	llm := &mockSimpleLLM{}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))

	ctx := context.Background()
	_, err := ag.ChatWithProgress(ctx, "genuine-user-progress", "hello there", func(ProgressEvent) {})
	require.NoError(t, err)

	session, ok := ag.memory.GetSession("genuine-user-progress")
	require.True(t, ok)

	var sawUser, sawAssistant bool
	for _, msg := range session.Messages {
		switch msg.Role {
		case "user":
			sawUser = true
			assert.Equal(t, ClassLedger, ContextClass(msg.ContextClass), "genuine user message (ChatWithProgress) must classify ledger")
		case "assistant":
			sawAssistant = true
			assert.Equal(t, ClassNarrative, ContextClass(msg.ContextClass), "assistant messages must stay narrative")
		}
	}
	assert.True(t, sawUser, "test setup: conversation must contain a user message")
	assert.True(t, sawAssistant, "test setup: conversation must contain an assistant message")
}

// TestContextClass_NudgeMessage_Narrative drives the empty-response one-shot retry: the
// LLM first returns an empty, tool-free response (triggering the synthetic nudge message),
// then a normal response to end the conversation. The synthetic user-role nudge must
// classify narrative, never ledger — this is exactly why classification is by construction
// site, not role: both the genuine and synthetic sites set AgentID.
func TestContextClass_NudgeMessage_Narrative(t *testing.T) {
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{content: ""}, // empty, no tool calls -> triggers nudge
			{content: "here is my answer"},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))

	ctx := context.Background()
	_, err := ag.Chat(ctx, "nudge-session", "please respond")
	require.NoError(t, err)

	session, ok := ag.memory.GetSession("nudge-session")
	require.True(t, ok)

	var nudge *Message
	var genuine *Message
	for i, msg := range session.Messages {
		if msg.Role != "user" {
			continue
		}
		if strings.Contains(msg.Content, "Your previous response was empty") {
			nudge = &session.Messages[i]
		} else {
			genuine = &session.Messages[i]
		}
	}
	require.NotNil(t, genuine, "test setup: conversation must contain the genuine user message")
	require.NotNil(t, nudge, "test setup: conversation must contain the empty-response nudge")

	assert.Equal(t, ClassLedger, ContextClass(genuine.ContextClass), "genuine user message must classify ledger")
	assert.Equal(t, ClassNarrative, ContextClass(nudge.ContextClass), "synthetic empty-response nudge must classify narrative, not ledger")
}

// TestContextClass_MaxTurnSynthesisMessage_Narrative drives the max-turns synthesis path:
// alwaysCallTools never emits end_turn, so the loop exhausts MaxTurns and the agent injects
// the synthetic "provide your final answer NOW" user-role message. It must classify
// narrative, never ledger.
func TestContextClass_MaxTurnSynthesisMessage_Narrative(t *testing.T) {
	llm := &mockToolCallingLLM{alwaysCallTools: true}

	cfg := newContextClassTestConfig()
	cfg.MaxTurns = 1
	cfg.MaxToolExecutions = 50

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "synthesis-session", "please help with something")
	require.NoError(t, err)

	session, ok := ag.memory.GetSession("synthesis-session")
	require.True(t, ok)

	var synthesis *Message
	var genuine *Message
	for i, msg := range session.Messages {
		if msg.Role != "user" {
			continue
		}
		if strings.Contains(msg.Content, "You must provide your final answer NOW") {
			synthesis = &session.Messages[i]
		} else {
			genuine = &session.Messages[i]
		}
	}
	require.NotNil(t, genuine, "test setup: conversation must contain the genuine user message")
	require.NotNil(t, synthesis, "test setup: conversation must have hit max-turns synthesis")

	assert.Equal(t, ClassLedger, ContextClass(genuine.ContextClass), "genuine user message must classify ledger")
	assert.Equal(t, ClassNarrative, ContextClass(synthesis.ContextClass), "synthetic max-turn synthesis message must classify narrative, not ledger")
}

// --- AC-2: tool-result class dispatch through the real Agent.Chat surface ---

func chatSessionMessages(t *testing.T, ag *Agent, sessionID string) []Message {
	t.Helper()
	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok)
	return session.Messages
}

func TestContextClass_ToolResult_LoaderTool_Narrative(t *testing.T) {
	// v5 correction: manage_skills load results tag narrative — fold's LLM
	// compressor summarizes skill bodies into residue under pressure.
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "manage_skills", Input: map[string]interface{}{}}}},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))
	ag.RegisterTool(&mockNamedTool{toolName: "manage_skills"})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "loader-tool-session", "load a skill")
	require.NoError(t, err)

	toolMsg := findToolMessage(t, chatSessionMessages(t, ag, "loader-tool-session"), "call_1")
	assert.Equal(t, ClassNarrative, ContextClass(toolMsg.ContextClass), "manage_skills tool result must classify narrative — fold summarizes into residue")
}

func TestContextClass_ToolResult_WhitelistedReadOnly_Ballast(t *testing.T) {
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "read_only_data", Input: map[string]interface{}{}}}},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))
	ag.RegisterTool(&mockHintedTool{mockNamedTool: mockNamedTool{toolName: "read_only_data"}, hint: "ballast"})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "ballast-tool-session", "read some data")
	require.NoError(t, err)

	toolMsg := findToolMessage(t, chatSessionMessages(t, ag, "ballast-tool-session"), "call_1")
	assert.Equal(t, ClassBallast, ContextClass(toolMsg.ContextClass), "whitelisted read-only tool result must classify ballast")
}

func TestContextClass_ToolResult_MutatingTool_Ballast(t *testing.T) {
	// v5 correction: mutating tools no longer classify ledger by default.
	// All non-loader non-contact_human tools are ballast; correctness for
	// mutations is a tool-idempotency / approval-gate concern, not a
	// context-visibility concern.
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}}}},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "mutating-tool-session", "calculate something")
	require.NoError(t, err)

	toolMsg := findToolMessage(t, chatSessionMessages(t, ag, "mutating-tool-session"), "call_1")
	assert.Equal(t, ClassBallast, ContextClass(toolMsg.ContextClass), "under the new default, non-loader tool results classify ballast")
}

func TestContextClass_ToolResult_ContactHuman_Ledger(t *testing.T) {
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "contact_human", Input: map[string]interface{}{"question": "ok?"}}}},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))
	ag.RegisterTool(&mockNamedTool{toolName: "contact_human"})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "contact-human-session", "ask a human")
	require.NoError(t, err)

	toolMsg := findToolMessage(t, chatSessionMessages(t, ag, "contact-human-session"), "call_1")
	assert.Equal(t, ClassLedger, ContextClass(toolMsg.ContextClass), "contact_human tool result must classify ledger (fail-safe retain)")
}

// TestContextClass_SkipMsg_Ballast triggers the per-turn tool-call cap (MaxIterations): two
// tool calls in a single LLM response, cap of 1, so the second is skipped with a synthetic
// tool-role control message. It must classify the same as any other non-loader tool result —
// ballast under the new default.
func TestContextClass_SkipMsg_Ballast(t *testing.T) {
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}},
				{ID: "call_2", Name: "calculator", Input: map[string]interface{}{"expression": "2+2"}},
			}},
		},
	}
	cfg := newContextClassTestConfig()
	cfg.MaxIterations = 1

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "skip-session", "do two calculations")
	require.NoError(t, err)

	msgs := chatSessionMessages(t, ag, "skip-session")
	var skipMsg *Message
	for i, msg := range msgs {
		if msg.Role == "tool" && strings.Contains(msg.Content, "turn_limit_exceeded") {
			skipMsg = &msgs[i]
		}
	}
	require.NotNil(t, skipMsg, "test setup: the per-turn cap must have skipped the second tool call")
	assert.Equal(t, ClassBallast, ContextClass(skipMsg.ContextClass), "the turn-limit skip control message classifies the same as any non-loader tool result — ballast under the new default")
}

// TestContextClass_DedupMsg_Ballast issues two identical tool calls (same name + input) in a
// single LLM response; the second is served from the in-turn dedup cache as a synthetic
// tool-role control message. It must classify the same as its underlying tool result —
// ballast under the new default.
func TestContextClass_DedupMsg_Ballast(t *testing.T) {
	sameInput := map[string]interface{}{"expression": "3+3"}
	llm := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				{ID: "call_1", Name: "calculator", Input: sameInput},
				{ID: "call_2", Name: "calculator", Input: sameInput},
			}},
		},
	}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(newContextClassTestConfig()))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "dedup-session", "repeat the same calculation")
	require.NoError(t, err)

	msgs := chatSessionMessages(t, ag, "dedup-session")
	var dedupMsg *Message
	for i, msg := range msgs {
		if msg.Role == "tool" && strings.Contains(msg.Content, "deduplicated") {
			dedupMsg = &msgs[i]
		}
	}
	require.NotNil(t, dedupMsg, "test setup: the second identical call must have been deduplicated")
	assert.Equal(t, ClassBallast, ContextClass(dedupMsg.ContextClass), "the dedup control message classifies the same as its underlying tool result — ballast under the new default")
}

// findToolMessage returns the single tool-role message matching toolUseID, failing the test
// if it is not found.
func findToolMessage(t *testing.T, msgs []Message, toolUseID string) *Message {
	t.Helper()
	for i, msg := range msgs {
		if msg.Role == "tool" && msg.ToolUseID == toolUseID {
			return &msgs[i]
		}
	}
	t.Fatalf("no tool message found with ToolUseID=%q", toolUseID)
	return nil
}

// --- AC-4: reclassify-on-restore (pure function) ---

func TestReclassifyMessages_PersistedNonEmpty_UsedVerbatim(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc1", Name: "delete_records"}}},
		// Already persisted as ballast, even though its paired tool name would
		// otherwise default to ledger — the persisted value must win, unchanged.
		{Role: "tool", ToolUseID: "tc1", ContextClass: string(ClassBallast)},
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 2)
	assert.Equal(t, ClassBallast, ContextClass(out[1].ContextClass), "a persisted non-empty class must be used verbatim, never re-derived")
}

func TestReclassifyMessages_LegacyUserRole_Ledger(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "do the thing"}, // legacy row, empty ContextClass
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 1)
	assert.Equal(t, ClassLedger, ContextClass(out[0].ContextClass), "a bare legacy user-role row must reclassify to ledger (safe over-retention default)")
}

func TestReclassifyMessages_LegacyAssistantRole_Narrative(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: "here is the answer"}, // legacy row, empty ContextClass
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 1)
	assert.Equal(t, ClassNarrative, ContextClass(out[0].ContextClass), "a legacy assistant-role row must reclassify to narrative")
}

func TestReclassifyMessages_LegacyToolRole_PairedLoaderName_Narrative(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "load a skill"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc1", Name: "manage_skills"}}},
		{Role: "tool", ToolUseID: "tc1", Content: "loaded"}, // legacy row, empty ContextClass
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 3)
	assert.Equal(t, ClassNarrative, ContextClass(out[2].ContextClass), "a legacy tool row paired to a loader tool call reclassifies to narrative (fold summarizes into residue)")
}

func TestReclassifyMessages_LegacyToolRole_PairedGenericName_Ballast(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "delete something"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc1", Name: "delete_records"}}},
		{Role: "tool", ToolUseID: "tc1", Content: "deleted"}, // legacy row, empty ContextClass
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 3)
	assert.Equal(t, ClassBallast, ContextClass(out[2].ContextClass), "a legacy tool row paired to a non-loader tool call reclassifies to ballast (valve-reclaimable)")
}

// TestReclassifyMessages_LegacyToolRole_PairingWalksBackward exercises multiple tool
// calls in a single assistant turn, verifying the ToolUseID pairing recovers the correct
// name for each — not just the first or last ToolCalls entry.
func TestReclassifyMessages_LegacyToolRole_PairingWalksBackward(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "do two things"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "tc1", Name: "manage_patterns"},
			{ID: "tc2", Name: "delete_records"},
		}},
		{Role: "tool", ToolUseID: "tc1", Content: "loaded"},  // legacy row
		{Role: "tool", ToolUseID: "tc2", Content: "deleted"}, // legacy row
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 4)
	assert.Equal(t, ClassNarrative, ContextClass(out[2].ContextClass), "tc1 pairs to manage_patterns -> narrative (fold summarizes into residue)")
	assert.Equal(t, ClassBallast, ContextClass(out[3].ContextClass), "tc2 pairs to delete_records -> ballast (valve-reclaimable)")
}

// --- AC-5: pressure treatment is a function of class, not age ---

// TestReclassifyMessages_AgeInvariant_SameStructureDifferentTimestamps proves that
// reclassification depends only on structural provenance (role / paired tool name), never
// on message age: two structurally identical legacy rows separated by 20 years of
// Timestamp must reclassify to the exact same class.
func TestReclassifyMessages_AgeInvariant_SameStructureDifferentTimestamps(t *testing.T) {
	oldTime := time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Now()

	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc-old", Name: "manage_skills"}}, Timestamp: oldTime},
		{Role: "tool", ToolUseID: "tc-old", Timestamp: oldTime}, // ancient legacy row

		{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc-new", Name: "manage_skills"}}, Timestamp: newTime},
		{Role: "tool", ToolUseID: "tc-new", Timestamp: newTime}, // fresh legacy row, same structure
	}

	out := reclassifyMessages(msgs)

	require.Len(t, out, 4)
	assert.Equal(t, ClassNarrative, ContextClass(out[1].ContextClass))
	assert.Equal(t, ClassNarrative, ContextClass(out[3].ContextClass))
	assert.Equal(t, out[1].ContextClass, out[3].ContextClass, "identical structure at different ages must classify identically — age never influences the class")
}

// TestToolResultClass_AgeInvariant proves toolResultClass itself carries no age/time input:
// classifying the same tool twice, arbitrarily far apart in wall-clock time, is
// deterministic and identical.
func TestToolResultClass_AgeInvariant(t *testing.T) {
	tool := &mockNamedTool{toolName: "delete_records"}

	first := toolResultClass("delete_records", tool)
	time.Sleep(2 * time.Millisecond) // real wall-clock time passes between calls
	second := toolResultClass("delete_records", tool)

	assert.Equal(t, first, second, "toolResultClass has no age/time input: repeated calls must be deterministic regardless of elapsed wall-clock time")
	assert.Equal(t, ClassBallast, first, "under the new default, an unknown tool without a ledger/charter hint classifies ballast")
}
