// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

// Integration tests for the v5 context pipeline fixes.
//
// These exercise the full memory pipeline end-to-end using the real
// SegmentedMemory, a real in-process SQLite SessionStore, and the real
// classifier — no mocks except the LLM compressor (a documented plug point).
//
// Covers:
//
//   Fix 1  toolResultClass now defaults to ClassBallast; skills classify as
//          ClassNarrative (fold rolls them over via LLM summary); contact_human
//          stays ClassLedger.
//
//   Fix 3  RetrieveL2Snapshots unwraps the {residue, foldIndex} JSON envelope
//          fold now persists.
//
//   Fix 4  Session.SnapshotMessages returns a locked copy so prepareContext's
//          zone-check reads don't race with AddMessage.
//
//   Fix 5  manage_skills honors max_concurrent_skills from agent config.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// ============================================================================
// Fix 1 — classifier flip
// ============================================================================

func TestToolResultClass_Default_IsBallast_NotLedger(t *testing.T) {
	// Any unknown tool — a Teradata read, an MCP call, a shell tool — must
	// default to ballast so valve has candidates to reclaim under yellow-zone
	// pressure. The pre-fix default was ledger, which left valve dormant on
	// every real workload and caused fold's breaker to trip on ordinary
	// data-heavy sessions.
	cases := []string{
		"teradata_tool_call",
		"mcp_random_tool",
		"shell_execute_sandbox",
		"my_custom_read_tool",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got := toolResultClass(name, nil)
			assert.Equal(t, ClassBallast, got,
				"tool %q must default to ballast (was ledger pre-fix)", name)
		})
	}
}

func TestToolResultClass_Skills_Narrative_NotCharter(t *testing.T) {
	// Under narrative, fold's LLM compressor summarizes them into residue
	// when pressure hits red, capturing state ("worked with skill X on Y")
	// so the LLM can resume from residue. Skill loads roll over cleanly.
	loaders := []string{"manage_skills", "manage_patterns"}
	for _, name := range loaders {
		t.Run(name, func(t *testing.T) {
			got := toolResultClass(name, nil)
			assert.Equal(t, ClassNarrative, got,
				"loader tool %q must classify as narrative so fold can summarize into residue", name)
		})
	}
}

func TestToolResultClass_ContactHuman_StaysLedger_UnderNewDefault(t *testing.T) {
	// contact_human carries the user's out-of-band consent/answer — the same
	// forward-correctness weight as a user turn (ledger). Must survive both
	// valve and fold as-is; never summarized, never evicted.
	got := toolResultClass("contact_human", nil)
	assert.Equal(t, ClassLedger, got,
		"contact_human is user consent — must stay ledger even under the ballast default")
}

func TestToolResultClass_ContextClassHinter_OptOutStillHonored(t *testing.T) {
	// The ContextClassHinter interface remains as an escape hatch: a tool
	// that must be pinned (e.g., a security audit log tool) can opt out of
	// the ballast default by returning ClassLedger or ClassCharter.
	optOutLedger := &stubHinter{hint: shuttle.ClassLedger}
	assert.Equal(t, ClassLedger, toolResultClass("audit_log", optOutLedger),
		"a tool that hints ledger must be treated as ledger (opt-out from ballast default)")

	optInCharter := &stubHinter{hint: shuttle.ClassCharter}
	assert.Equal(t, ClassCharter, toolResultClass("some_pinned_tool", optInCharter),
		"a tool that hints charter must be treated as charter (opt-out from ballast default)")

	redundantBallast := &stubHinter{hint: shuttle.ClassBallast}
	assert.Equal(t, ClassBallast, toolResultClass("read_tool", redundantBallast),
		"a tool that hints ballast reaches ballast via the default — redundant but harmless")
}

// ============================================================================
// Fix 1 — pipeline behavior (Teradata-shaped session)
// ============================================================================

// TestPipeline_TypicalTeradataSession_ValveReclaimsBallast simulates a
// data-heavy Teradata session: a user turn (ledger) followed by several
// large tool_results (now ballast under Fix 1). Under the pre-fix default,
// these tool_results would be ledger and valve would find zero candidates.
// Under Fix 1, valve reclaims the oldest ones while keeping user turns and
// the newest-3 ballast intact.
func TestPipeline_TypicalTeradataSession_ValveReclaimsBallast(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "teradata-heavy"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	// User turn — ledger, must survive valve.
	userMsg := Message{
		Role: "user", Content: "profile every table in demo_car_db",
		ContextClass: ClassLedger, Timestamp: time.Now(),
	}
	sm.AddMessage(ctx, userMsg)

	// Six large tool_results, classified through the real classifier so we
	// pin Fix 1's effect end-to-end (not by hand-stamping ClassBallast).
	for i := 0; i < 6; i++ {
		result := Message{
			Role:         "tool",
			ToolUseID:    fmt.Sprintf("call-%d", i),
			Content:      sentenceRepeat(1500), // large enough to matter
			ContextClass: toolResultClass("teradata_tool_call", nil),
			Timestamp:    time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		require.Equal(t, ClassBallast, result.ContextClass,
			"Fix 1: teradata_tool_call results must classify as ballast")
		sm.AddMessage(ctx, result)
	}

	// Trigger valve with a modest payoff bar so it actually fires on this
	// scenario (the default 20K payoff assumes a full-session accumulation).
	sm.SetMinValvePayoffTokens(1000)
	sm.SetKeepRecentBallast(3)
	sm.ValveEvict(ctx)

	msgs := sm.GetMessages()

	// User turn survives — ledger is never a valve candidate.
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, ClassLedger, msgs[0].ContextClass)
	assert.Equal(t, userMsg.Content, msgs[0].Content, "ledger user turn must be untouched")

	// Newest 3 ballast items survive intact; older ones become stubs.
	toolMsgs := 0
	stubs := 0
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		toolMsgs++
		if isStub(m) {
			stubs++
			assert.Contains(t, m.Content, "recall_context",
				"stub must name recall_context as the recovery path")
		}
	}
	assert.Equal(t, 6, toolMsgs, "all 6 tool_result slots preserved (stub-in-place, never removed)")
	assert.Equal(t, 3, stubs, "oldest 3 ballast items evicted; newest 3 kept per keepRecentBallast=3")
}

// TestPipeline_SkillLoad_FoldedIntoResidue_NotPinned exercises Fix 1's
// skill-narrative classification: a skill load message goes through fold's
// compressor into residue (the LLM's continuation-ready summary), rather
// than being pinned as charter forever.
func TestPipeline_SkillLoad_FoldedIntoResidue_NotPinned(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "skill-fold"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	// Capture what the compressor sees so we can assert the skill body was
	// routed to fold's narrative bucket (compressor input), not to the
	// charter/ledger carry set.
	var capturedInput []Message
	sm.SetCompressor(&mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			capturedInput = append([]Message{}, msgs...)
			return "session state: worked through skill_A steps; ready to resume from step 5"
		},
	})

	// Seed the timeline: user turn (ledger), skill load (narrative under Fix 1),
	// assistant reasoning (narrative), a few tool results (ballast under Fix 1).
	sm.AddMessage(ctx, Message{
		Role: "user", Content: "profile Complaints",
		ContextClass: ClassLedger, Timestamp: time.Now(),
	})
	skillLoad := Message{
		Role: "tool", ToolUseID: "load-skill-A",
		Content:      "# Skill: td-data-profile\n" + sentenceRepeat(800),
		ContextClass: toolResultClass("manage_skills", nil),
		Timestamp:    time.Now(),
	}
	require.Equal(t, ClassNarrative, skillLoad.ContextClass,
		"Fix 1: manage_skills load must classify as narrative (was charter)")
	sm.AddMessage(ctx, skillLoad)

	sm.AddMessage(ctx, Message{
		Role: "assistant", Content: "loading td-data-profile; will run count, schema, sample",
		ContextClass: ClassNarrative, Timestamp: time.Now(),
	})
	for i := 0; i < 3; i++ {
		sm.AddMessage(ctx, Message{
			Role: "tool", ToolUseID: fmt.Sprintf("qc-%d", i),
			Content:      sentenceRepeat(200),
			ContextClass: toolResultClass("teradata_tool_call", nil),
			Timestamp:    time.Now(),
		})
	}

	// Drive fold.
	require.NoError(t, sm.Fold(ctx, 1, sm.GetL1MessageCount()))

	// The compressor received the skill body — that's the narrative rollover
	// path. Under the old charter classification, the skill body would have
	// been in fold's carry set and never reached the compressor.
	require.NotEmpty(t, capturedInput, "fold must call compressor with narrative input")
	sawSkillBody := false
	for _, m := range capturedInput {
		if strings.HasPrefix(m.Content, "# Skill: td-data-profile") {
			sawSkillBody = true
			break
		}
	}
	assert.True(t, sawSkillBody,
		"under Fix 1 the skill body must reach fold's compressor as narrative — the whole point of the classifier flip")

	// L2 summary is the residue the compressor returned — this is what the
	// LLM will see on the next beat as "Previous conversation summary".
	assert.Contains(t, sm.GetL2Summary(), "ready to resume",
		"L2 summary must be the compressor's residue (continuation-ready)")

	// Post-fold L1: the user turn (ledger) survived; the skill body did not.
	postFold := sm.GetMessages()
	userSurvived := false
	skillBodyPresent := false
	for _, m := range postFold {
		if m.Role == "user" && m.ContextClass == ClassLedger {
			userSurvived = true
		}
		if strings.HasPrefix(m.Content, "# Skill: td-data-profile") {
			skillBodyPresent = true
		}
	}
	assert.True(t, userSurvived, "ledger user turn must be in the carry set after fold")
	assert.False(t, skillBodyPresent, "skill body must not be pinned in L1 after fold — that was the pre-fix charter behavior")
}

// ============================================================================
// Fix 3 — RetrieveL2Snapshots unwraps the envelope
// ============================================================================

// TestRetrieveL2Snapshots_ReturnsResidueText_NotJSONBlob covers the graph
// memory extractor's contract: it consumes L2 snapshots as summary text and
// feeds them to an entity extractor. Fold persists snapshots as
// {residue, foldIndex} JSON. Pre-Fix-3, the extractor received the raw
// JSON blob and silently polluted graph memory with "residue"/"foldIndex"
// pseudo-entities.
func TestRetrieveL2Snapshots_ReturnsResidueText_NotJSONBlob(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "retrieve-envelope"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	// Persist a fold envelope exactly as Fold does today.
	residue := "user: profiled Complaints (1000 rows, 12 cols); ready to profile Service_Centers"
	envelope, err := json.Marshal(foldRecord{Residue: residue, FoldIndex: 42})
	require.NoError(t, err)
	require.NoError(t, store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", string(envelope), 0))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	out, err := sm.RetrieveL2Snapshots(ctx, 0)
	require.NoError(t, err)
	require.Len(t, out, 1)

	// The caller must receive the residue, not the JSON envelope.
	assert.Equal(t, residue, out[0],
		"RetrieveL2Snapshots must unwrap the {residue,foldIndex} envelope; consumers want summary text")

	// Defense-in-depth: assert no envelope keys leaked.
	assert.NotContains(t, out[0], "\"residue\"",
		"raw JSON keys must not leak to consumers (would pollute graph memory as pseudo-entities)")
	assert.NotContains(t, out[0], "\"foldIndex\"",
		"raw JSON keys must not leak to consumers")
}

// TestRetrieveL2Snapshots_LegacyPlainText_FallsThrough covers pre-envelope
// snapshots (written before the format change). Parse fails → fall back to
// raw Content. No consumer of RetrieveL2Snapshots sees a JSON parse error.
func TestRetrieveL2Snapshots_LegacyPlainText_FallsThrough(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "retrieve-legacy"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	legacy := "worked with td-data-profile skill; last state: sampling"
	require.NoError(t, store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", legacy, 0))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	out, err := sm.RetrieveL2Snapshots(ctx, 0)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, legacy, out[0], "legacy plain-text snapshots must fall through unchanged")
}

// ============================================================================
// Fix 4 — SnapshotMessages is race-free
// ============================================================================

// TestSnapshotMessages_ConcurrentWithAddMessage_NoRaceNoTear exercises the
// pattern prepareContext now uses: SnapshotMessages under lock while other
// goroutines call AddMessage. The go test race detector will fire if any
// reader reads a slice header being reallocated by a concurrent writer.
func TestSnapshotMessages_ConcurrentWithAddMessage_NoRaceNoTear(t *testing.T) {
	session := &Session{ID: "race", Context: map[string]interface{}{}}

	const writers = 4
	const perWriter = 500
	var wg sync.WaitGroup

	// Writers: many concurrent appends. Each Message carries a distinct
	// ToolUseID so we can verify no tearing after the fact.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				session.AddMessage(context.Background(), Message{
					Role:      "tool",
					ToolUseID: fmt.Sprintf("w%d-%d", id, i),
					Content:   "x",
				})
			}
		}(w)
	}

	// Readers: repeatedly snapshot the flat history.
	stop := make(chan struct{})
	readWG := sync.WaitGroup{}
	for r := 0; r < 2; r++ {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := session.SnapshotMessages()
				// Walk to force the race detector to touch every element.
				for _, m := range snap {
					if m.Role == "" && m.ToolUseID == "" && m.Content == "" {
						t.Errorf("snapshot contained a zero-valued Message — slice tear")
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	readWG.Wait()

	// Final consistency check: the full history is exactly writers*perWriter.
	final := session.SnapshotMessages()
	assert.Equal(t, writers*perWriter, len(final),
		"final snapshot must contain every append; a race would have dropped writes")
}

// ============================================================================
// LoadHardCap overrides the built-in load limit
// ============================================================================

// TestManageSkillsLoad_LoadHardCapOverridesBuiltinLimit verifies that
// executeLoad's limit comes from SkillsConfig.LoadHardCap when set,
// and falls back to skillActiveSafetyCap otherwise.
func TestManageSkillsLoad_LoadHardCapOverridesBuiltinLimit(t *testing.T) {
	ctx := context.Background()

	tmp := t.TempDir()
	library := skills.NewLibrary(skills.WithSearchPaths(tmp))

	// Seed a few skills to activate.
	names := []string{"skill_a", "skill_b", "skill_c", "skill_d"}
	seeded := make(map[string]*skills.Skill, len(names))
	for _, n := range names {
		s := &skills.Skill{Name: n, Title: n, SourcePath: tmp + "/" + n}
		library.Register(s)
		seeded[n] = s
	}

	tool := &ManageSkillsTool{
		orchestrator: skills.NewOrchestrator(library),
		config: &Config{
			SkillsConfig: &skills.SkillsConfig{LoadHardCap: 3},
		},
		taskBoardConfig: &loomv1.TaskBoardConfig{},
	}

	sessionID := "cap-session"
	// Activate the first 3 skills directly.
	for i := 0; i < 3; i++ {
		tool.orchestrator.ActivateSkill(sessionID, seeded[names[i]], "manual", names[i], 1.0)
	}

	// Try to load a 4th. With LoadHardCap=3, this must return the limit
	// error naming 3 (not the built-in 20).
	res, err := tool.executeLoad(ctx, sessionID, "skill_d")
	require.NoError(t, err, "limit breach returns Result{Success:false}, not a Go error")
	require.NotNil(t, res)
	require.False(t, res.Success, "load past LoadHardCap must fail")
	require.NotNil(t, res.Error)
	assert.Equal(t, "ACTIVE_SKILL_CAP_EXCEEDED", res.Error.Code)
	assert.Contains(t, res.Error.Message, "limit 3",
		"error message must name the configured limit (3), not the built-in 20")
	assert.NotContains(t, res.Error.Message, "limit 20",
		"the built-in 20 must not appear when LoadHardCap is set")
}

// ============================================================================
// Test doubles
// ============================================================================

// stubHinter is a shuttle.Tool that also implements ContextClassHinter,
// used to verify the classifier's opt-out escape hatch.
type stubHinter struct {
	hint string
}

func (s *stubHinter) Name() string                     { return "stub_hinter" }
func (s *stubHinter) Description() string              { return "" }
func (s *stubHinter) Backend() string                  { return "" }
func (s *stubHinter) InputSchema() *shuttle.JSONSchema { return nil }
func (s *stubHinter) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true}, nil
}
func (s *stubHinter) ContextClassHint() string { return s.hint }
