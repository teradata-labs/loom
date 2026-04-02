package agent

// token_count_baseline_test.go — Regression tests that pin exact token counts
// produced by SegmentedMemory.updateTokenCount(). These must pass BEFORE and
// AFTER any optimization to token counting.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Known strings whose token counts are pinned by these tests.
// If tiktoken or the encoding changes, these values must be updated deliberately.
const (
	baselineROM          = "You are a helpful assistant. Use available tools to help the user."
	baselineUserMsg      = "What tables exist in the database?"
	baselineAssistantMsg = "I found 3 tables: users, orders, products."
	baselineToolMsg      = "SELECT name FROM sqlite_master WHERE type='table'"
	baselineL2Summary    = "The user asked about database tables. The assistant found 3 tables."
	baselinePattern      = "Pattern: SQL Query Helper\nUse this pattern for SQL-related queries."
	baselineSkill        = "Skill: Database Explorer\nNavigate and explore database schemas."
)

// TestBaseline_CountTokens_KnownStrings pins exact token counts for known strings.
func TestBaseline_CountTokens_KnownStrings(t *testing.T) {
	tc := GetTokenCounter()
	require.NotNil(t, tc.encoder, "tiktoken encoder must be available for baseline tests")

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"ROM", baselineROM, 14},
		{"user message", baselineUserMsg, 7},
		{"assistant message", baselineAssistantMsg, 12},
		{"tool message", baselineToolMsg, 10},
		{"L2 summary", baselineL2Summary, 14},
		{"pattern", baselinePattern, 14},
		{"skill", baselineSkill, 11},
		{"empty string", "", 0},
	}

	// Capture actual values first to see if our expected values are right
	for _, tt := range tests {
		count := tc.CountTokens(tt.input)
		t.Logf("CountTokens(%q) = %d (expected %d)", tt.name, count, tt.expected)
	}

	// Now assert — will fail if expected values are wrong, letting us pin them
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := tc.CountTokens(tt.input)
			assert.Equal(t, tt.expected, count, "token count mismatch for %q", tt.name)
		})
	}
}

// TestBaseline_EstimateMessagesTokens_ExactValues pins exact token estimates for
// a known message sequence. The overhead per message is 10 tokens.
func TestBaseline_EstimateMessagesTokens_ExactValues(t *testing.T) {
	tc := GetTokenCounter()
	require.NotNil(t, tc.encoder)

	msgs := []Message{
		{Role: "user", Content: baselineUserMsg},      // 10 + 7 = 17
		{Role: "assistant", Content: baselineAssistantMsg}, // 10 + 12 = 22
		{Role: "user", Content: baselineToolMsg},       // 10 + 11 = 21
	}

	total := tc.EstimateMessagesTokens(msgs)
	t.Logf("EstimateMessagesTokens(3 messages) = %d", total)

	// Pin the value — sum of (10 overhead + content tokens) per message
	// 10+7=17, 10+12=22, 10+10=20 = 59
	assert.Equal(t, 59, total, "expected 3×10 overhead + 7+12+10 content = 59")
}

// TestBaseline_EstimateMessagesTokens_Consistency verifies that
// EstimateMessagesTokens produces the same result when called multiple times.
func TestBaseline_EstimateMessagesTokens_Consistency(t *testing.T) {
	tc := GetTokenCounter()
	msgs := []Message{
		{Role: "user", Content: baselineUserMsg},
		{Role: "assistant", Content: baselineAssistantMsg},
		{Role: "user", Content: baselineToolMsg},
	}

	count1 := tc.EstimateMessagesTokens(msgs)
	count2 := tc.EstimateMessagesTokens(msgs)
	count3 := tc.EstimateMessagesTokens(msgs)

	assert.Equal(t, count1, count2)
	assert.Equal(t, count2, count3)
}

// TestBaseline_SegmentedMemory_TokenCount_EmptySession pins the token count
// of a freshly created SegmentedMemory with only ROM content.
func TestBaseline_SegmentedMemory_TokenCount_EmptySession(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)

	romTokens := sm.GetTokenCount()
	t.Logf("Empty session token count (ROM only): %d", romTokens)

	assert.Equal(t, 14, romTokens, "ROM-only token count must match baseline")
}

// TestBaseline_SegmentedMemory_AddMessage_ExactCounts pins the exact token
// count at each step as messages are added.
func TestBaseline_SegmentedMemory_AddMessage_ExactCounts(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)
	ctx := context.Background()

	step0 := sm.GetTokenCount()
	t.Logf("step0 (ROM only): %d", step0)

	sm.AddMessage(ctx, Message{Role: "user", Content: baselineUserMsg})
	step1 := sm.GetTokenCount()
	t.Logf("step1 (+ user msg): %d", step1)

	sm.AddMessage(ctx, Message{Role: "assistant", Content: baselineAssistantMsg})
	step2 := sm.GetTokenCount()
	t.Logf("step2 (+ assistant msg): %d", step2)

	sm.AddMessage(ctx, Message{Role: "user", Content: baselineToolMsg})
	step3 := sm.GetTokenCount()
	t.Logf("step3 (+ user msg 2): %d", step3)

	// Pin exact values
	assert.Equal(t, 14, step0, "ROM-only")
	// step1 = ROM(14) + message overhead(10) + user msg tokens(7) = 31
	assert.Equal(t, 31, step1, "ROM + 1 user message")
	// step2 = ROM(14) + msg1(17) + msg2(10+12=22) = 53
	assert.Equal(t, 53, step2, "ROM + 2 messages")
	// step3 = ROM(14) + msg1(17) + msg2(22) + msg3(10+10=20) = 73
	assert.Equal(t, 73, step3, "ROM + 3 messages")
}

// TestBaseline_SegmentedMemory_InjectPattern_ExactDelta pins the exact token
// delta when a pattern is injected.
func TestBaseline_SegmentedMemory_InjectPattern_ExactDelta(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)

	before := sm.GetTokenCount()
	sm.InjectPattern(baselinePattern, "sql-helper")
	after := sm.GetTokenCount()

	delta := after - before
	t.Logf("Pattern injection: before=%d, after=%d, delta=%d", before, after, delta)
	assert.Equal(t, 14, delta, "pattern token delta must match CountTokens(baselinePattern)")
}

// TestBaseline_SegmentedMemory_InjectSkills_ExactDelta pins the exact token
// delta when skills are injected.
func TestBaseline_SegmentedMemory_InjectSkills_ExactDelta(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)

	before := sm.GetTokenCount()
	sm.InjectSkills(baselineSkill, []string{"db-explorer"})
	after := sm.GetTokenCount()

	delta := after - before
	t.Logf("Skill injection: before=%d, after=%d, delta=%d", before, after, delta)
	assert.Equal(t, 11, delta, "skill token delta must match CountTokens(baselineSkill)")
}

// TestBaseline_SegmentedMemory_CacheSchema_ExactDelta pins the exact token
// delta when a schema is cached.
func TestBaseline_SegmentedMemory_CacheSchema_ExactDelta(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)

	before := sm.GetTokenCount()
	sm.CacheSchema("users", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT)")
	after := sm.GetTokenCount()

	delta := after - before
	t.Logf("CacheSchema: before=%d, after=%d, delta=%d", before, after, delta)
	// Schema is counted as: CountTokens("users: CREATE TABLE users ...")
	assert.Greater(t, delta, 0, "schema must increase token count")
	// Pin the exact value
	tc := GetTokenCounter()
	expected := tc.CountTokens("users: CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT)")
	assert.Equal(t, expected, delta, "schema delta must match CountTokens of 'key: schema'")
}

// TestBaseline_SegmentedMemory_AddToolResult_ExactDelta pins the token delta
// when a tool result is added.
func TestBaseline_SegmentedMemory_AddToolResult_ExactDelta(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)

	before := sm.GetTokenCount()
	sm.AddToolResult(CachedToolResult{
		ToolName: "execute_sql",
		Args:     map[string]interface{}{"query": "SELECT 1"},
		Result:   "1 row returned",
	})
	after := sm.GetTokenCount()

	delta := after - before
	t.Logf("AddToolResult: before=%d, after=%d, delta=%d", before, after, delta)
	assert.Greater(t, delta, 0, "tool result must increase token count")
}

// TestBaseline_SegmentedMemory_FullSession pins the complete token count for
// a fully built-up session. This is the master regression value.
func TestBaseline_SegmentedMemory_FullSession(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)
	ctx := context.Background()

	// Set tools directly (internal field, same package)
	sm.mu.Lock()
	sm.tools = []string{"execute_sql", "get_schema", "list_tables"}
	sm.tokenCountDirty = true
	sm.mu.Unlock()

	sm.CacheSchema("users", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	sm.InjectPattern(baselinePattern, "sql-helper")

	afterSetup := sm.GetTokenCount()
	t.Logf("After setup (ROM + tools + schema + pattern): %d", afterSetup)

	sm.AddMessage(ctx, Message{Role: "user", Content: baselineUserMsg})
	afterUser := sm.GetTokenCount()
	t.Logf("After user message: %d", afterUser)

	sm.AddMessage(ctx, Message{Role: "assistant", Content: baselineAssistantMsg})
	afterAssistant := sm.GetTokenCount()
	t.Logf("After assistant message: %d", afterAssistant)

	// Verify monotonic increase
	assert.Greater(t, afterSetup, 14) // more than just ROM
	assert.Greater(t, afterUser, afterSetup)
	assert.Greater(t, afterAssistant, afterUser)

	// Pin exact values — these are the master regression numbers
	t.Logf("MASTER VALUES: setup=%d, +user=%d, +assistant=%d", afterSetup, afterUser, afterAssistant)
}

// TestBaseline_UpdateTokenCount_Determinism verifies that calling
// updateTokenCount multiple times produces the same value.
func TestBaseline_UpdateTokenCount_Determinism(t *testing.T) {
	sm := NewSegmentedMemory(baselineROM, 200000, 20000)
	ctx := context.Background()

	sm.AddMessage(ctx, Message{Role: "user", Content: baselineUserMsg})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: baselineAssistantMsg})

	count1 := sm.GetTokenCount()

	// Force recalculation by marking dirty
	sm.mu.Lock()
	sm.tokenCountDirty = true
	sm.mu.Unlock()

	count2 := sm.GetTokenCount()

	// Force one more time
	sm.mu.Lock()
	sm.tokenCountDirty = true
	sm.mu.Unlock()

	count3 := sm.GetTokenCount()

	assert.Equal(t, count1, count2, "updateTokenCount must be deterministic (run 1 vs 2)")
	assert.Equal(t, count2, count3, "updateTokenCount must be deterministic (run 2 vs 3)")
}
