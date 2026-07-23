// Copyright 2026 Teradata
package agent

// Skill catalog in ROM — assembly semantics.
//
// The new skill discovery surface is the session's ROM catalog:
//   - Router appends candidates to session.RomCatalog (append-only, dedup)
//   - Session.GetMessages assembles ROM = base + [Available Skills] catalog
//     entries whose skill is NOT currently loaded (walk L1 for load metadata)
//   - No tail-note menu injection (fake user turn after real user turn — the
//     old anti-pattern that caused the skill re-load loop)
//
// These tests pin the assembly semantics using real Session + real
// SegmentedMemory. No mocks. Everything covered here is a Session-level
// integration of Contract 1's composition rules.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// TestCatalog_AppendDedupAndOrderStable pins the two invariants
// AppendToRomCatalog is supposed to guarantee: first-seen wins for the
// same skill name (dedup), and insertion order is preserved (so the ROM
// prefix stays cache-friendly as new entries append to the end).
func TestCatalog_AppendDedupAndOrderStable(t *testing.T) {
	s := &types.Session{ID: "cat-1"}
	s.AppendToRomCatalog(
		types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile Teradata tables"},
		types.SkillCatalogEntry{Name: "td-schema-check", Description: "validate schemas"},
	)
	// Re-append td-data-profile with a different description — dedup by name
	// must skip; the first description wins.
	s.AppendToRomCatalog(
		types.SkillCatalogEntry{Name: "td-data-profile", Description: "SHOULD-NOT-APPEAR"},
		types.SkillCatalogEntry{Name: "td-join-analysis", Description: "join key analysis"},
	)
	// Empty name is skipped (would render a bare "- " line in the ROM).
	s.AppendToRomCatalog(types.SkillCatalogEntry{Name: "", Description: "ignored"})

	require.Len(t, s.RomCatalog, 3, "dedup by name; empty name skipped")
	assert.Equal(t, "td-data-profile", s.RomCatalog[0].Name)
	assert.Equal(t, "profile Teradata tables", s.RomCatalog[0].Description,
		"first-seen description wins — later duplicates must not overwrite")
	assert.Equal(t, "td-schema-check", s.RomCatalog[1].Name)
	assert.Equal(t, "td-join-analysis", s.RomCatalog[2].Name)
}

// TestGetMessages_ROMComposesBaseAndCatalog is the happy path: base ROM +
// catalog entries render into a single system message, in exactly this
// shape. No entries → no [Available Skills] section at all (no empty
// header leak).
func TestGetMessages_ROMComposesBaseAndCatalog(t *testing.T) {
	sm := NewSegmentedMemory("You are Tera. Follow user intent.", 200000, 20000)
	s := &types.Session{ID: "assemble-1", SegmentedMem: sm}
	s.AppendToRomCatalog(
		types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile tables"},
		types.SkillCatalogEntry{Name: "td-schema-check", Description: "validate schemas"},
	)

	msgs := s.GetMessages()
	require.NotEmpty(t, msgs, "must emit ROM at minimum")
	assert.Equal(t, "system", msgs[0].Role, "ROM sits in the first system slot")

	rom := msgs[0].Content
	assert.Contains(t, rom, "You are Tera.", "base ROM preserved verbatim")
	assert.Contains(t, rom, "[Available Skills]", "catalog section emitted when entries exist")
	assert.Contains(t, rom, "- td-data-profile: profile tables")
	assert.Contains(t, rom, "- td-schema-check: validate schemas")

	// Order-stable: profile before schema-check, matches insertion order.
	pIdx := strings.Index(rom, "td-data-profile")
	sIdx := strings.Index(rom, "td-schema-check")
	assert.Less(t, pIdx, sIdx, "catalog rendered in insertion order")
}

// TestGetMessages_ROMSkipsCatalogSectionWhenEmpty guards against an empty
// [Available Skills] header leaking into ROM when the catalog is empty or
// entirely filtered out (all entries active).
func TestGetMessages_ROMSkipsCatalogSectionWhenEmpty(t *testing.T) {
	sm := NewSegmentedMemory("base rom text", 200000, 20000)
	s := &types.Session{ID: "empty-cat", SegmentedMem: sm}
	// No AppendToRomCatalog call — catalog is nil.

	msgs := s.GetMessages()
	require.NotEmpty(t, msgs)
	assert.Equal(t, "base rom text", msgs[0].Content,
		"empty catalog must NOT introduce a bare [Available Skills] header")
	assert.NotContains(t, msgs[0].Content, "[Available Skills]")
}

// TestGetMessages_ROMFiltersActiveSkills is the core: a skill whose
// manage_skills(load) tool_result is present in L1 must NOT appear in the
// rendered ROM catalog. This is what breaks the re-load loop — the LLM
// only ever sees candidates it hasn't loaded.
func TestGetMessages_ROMFiltersActiveSkills(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("base", 200000, 20000)
	s := &types.Session{ID: "filter-1", SegmentedMem: sm}
	s.AppendToRomCatalog(
		types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile"},
		types.SkillCatalogEntry{Name: "td-schema-check", Description: "schema"},
		types.SkillCatalogEntry{Name: "td-join-analysis", Description: "joins"},
	)

	// Seed L1 with a load result for td-data-profile — exactly the shape
	// executeLoad emits: Role=tool, Metadata action=load skill=<name>.
	// This is the structural marker ActiveSkillNames walks for. No content
	// parsing anywhere — Metadata is the source of truth.
	loadMsg := Message{
		Role:      "tool",
		ToolUseID: "call-load-1",
		Content:   "# td-data-profile\nStep 1: ...", // body content, not what filter looks at
		ToolResult: &shuttle.Result{
			Success: true,
			Data:    "# td-data-profile\nStep 1: ...",
			Metadata: map[string]interface{}{
				"action": "load",
				"skill":  "td-data-profile",
			},
		},
	}
	sm.AddMessage(ctx, loadMsg)

	msgs := s.GetMessages()
	require.NotEmpty(t, msgs)
	rom := msgs[0].Content

	assert.NotContains(t, rom, "td-data-profile",
		"active skill's catalog entry must be filtered — this is what breaks the re-load loop")
	assert.Contains(t, rom, "td-schema-check", "inactive skills still surfaced")
	assert.Contains(t, rom, "td-join-analysis", "inactive skills still surfaced")
}

// TestGetMessages_ActiveDetection_IgnoresNonLoadToolResults confirms the
// structural filter is precise: any tool_result whose Metadata does NOT
// have action=="load" is ignored, so a SQL result / MCP call / anything
// else can never accidentally mark a "skill" as active.
func TestGetMessages_ActiveDetection_IgnoresNonLoadToolResults(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("base", 200000, 20000)
	s := &types.Session{ID: "precise-1", SegmentedMem: sm}
	s.AppendToRomCatalog(
		types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile"},
	)

	// A tool_result with action=list (not load) — must NOT hide the skill.
	sm.AddMessage(ctx, Message{
		Role:      "tool",
		ToolUseID: "call-list-1",
		Content:   "some list output",
		ToolResult: &shuttle.Result{
			Success: true,
			Metadata: map[string]interface{}{
				"action": "list",
				"skill":  "td-data-profile",
			},
		},
	})
	// A tool_result with no Metadata (e.g., generic MCP tool) — must not
	// panic and must not affect the filter.
	sm.AddMessage(ctx, Message{
		Role:       "tool",
		ToolUseID:  "call-mcp-1",
		Content:    "sql rows",
		ToolResult: &shuttle.Result{Success: true, Data: "rows"},
	})

	msgs := s.GetMessages()
	rom := msgs[0].Content
	assert.Contains(t, rom, "td-data-profile",
		"a non-load tool_result must not falsely mark the skill active")
}

// TestGetMessages_ActiveSkillReturnsToCatalog_AfterUnload is the
// self-healing property. Unload removes the load message from L1; the
// next Session.GetMessages walks L1, sees no load metadata for that
// skill, and the catalog entry re-surfaces. No separate bookkeeping to
// keep in sync.
//
// Same mechanism handles fold-eviction of a narrative-classed load body:
// once the body is gone from L1, the skill is "not active" and re-enters
// the catalog. LLM can decide to reload.
func TestGetMessages_ActiveSkillReturnsToCatalog_AfterUnload(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("base", 200000, 20000)
	s := &types.Session{ID: "self-heal", SegmentedMem: sm}
	s.AppendToRomCatalog(types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile"})

	// Load: body present → filtered out of catalog.
	loadMsg := Message{
		Role: "tool", ToolUseID: "call-1",
		ToolResult: &shuttle.Result{
			Success:  true,
			Metadata: map[string]interface{}{"action": "load", "skill": "td-data-profile"},
		},
	}
	sm.AddMessage(ctx, loadMsg)
	rom := s.GetMessages()[0].Content
	assert.NotContains(t, rom, "td-data-profile", "loaded → filtered")

	// Simulate the load message being removed from L1 (unload, fold, etc.).
	// TrimLastN is the closest existing surface — trim 1 removes the load msg.
	sm.TrimLastN(1)

	rom = s.GetMessages()[0].Content
	assert.Contains(t, rom, "td-data-profile",
		"body no longer in L1 → skill re-enters the ROM catalog (self-healing)")
}

// TestGetMessages_NoTailNoteMenu_ConversationShapeIntact verifies the
// tail-note menu injection is gone. Previously, a fresh Role:"user"
// message with "[Skill Discovery]" prefix was appended to the compiled
// slice after the real user turn — corrupting conversation shape and
// causing the LLM to obey the menu instead of the human. Now nothing
// gets injected between the real user's message and the assistant's turn.
func TestGetMessages_NoTailNoteMenu_ConversationShapeIntact(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("base rom", 200000, 20000)
	s := &types.Session{ID: "shape-1", SegmentedMem: sm}
	s.AppendToRomCatalog(types.SkillCatalogEntry{Name: "td-data-profile", Description: "profile"})

	sm.AddMessage(ctx, Message{Role: "user", Content: "profile Complaints", ContextClass: ClassLedger})

	msgs := s.GetMessages()
	// Expected shape: [system-ROM, user-real].
	// FORBIDDEN: any second user message carrying "[Skill Discovery]".
	require.Len(t, msgs, 2)
	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "profile Complaints", msgs[1].Content)

	for _, m := range msgs {
		assert.NotContains(t, m.Content, "[Skill Discovery]",
			"the tail-note menu injection is gone; skills live in the ROM catalog now")
	}
}
