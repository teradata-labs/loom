// Copyright 2026 Teradata
//
// mechanisms — focused routes, one design rule each. Each generates the real
// compiled context and checks the specific §4/§5 rule it targets.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 -run TestMech ./test/context-optimiser/ -v

package contextoptimiser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/agent"
)

// drive is a tiny scripted-turn helper shared by these routes.
func drive(t *testing.T, r *rig, sid string, user string, resp ...scriptedTurn) error {
	t.Helper()
	r.llm.turns = append(r.llm.turns, resp...)
	_, err := r.agent.Chat(context.Background(), sid, user)
	return err
}

// TestMech_ThresholdRule — §5.2/§5.5: a result strictly OVER the threshold is an
// offload stub; a result exactly AT the threshold renders whole (a page must
// never stub itself).
func TestMech_ThresholdRule(t *testing.T) {
	requireGate(t)
	const threshold = 16384
	r := newRig(t, routeOutDir(t, "threshold"), nil, 200000, 8000, threshold)
	sid := "threshold"

	// at threshold → whole; over threshold → offload stub. Same turn.
	if err := drive(t, r, sid, "two scans",
		callTools(emit("at", threshold, "string"), emit("over", threshold+50, "string")),
		sayText("done")); err != nil {
		t.Fatal(err)
	}
	last := r.readStages(t)
	final := last[len(last)-1]

	var atWhole, overStub bool
	for _, m := range final.Messages {
		if m.Role != "tool" {
			continue
		}
		if len(m.Content) == threshold && strings.Contains(m.Content, "scan-row") &&
			!strings.Contains(m.Content, "held in memory") {
			atWhole = true
		}
		if strings.Contains(m.Content, "held in memory this turn") {
			overStub = true
		}
	}
	if !atWhole {
		t.Error("at-threshold result did not render whole — the strictly-over rule is off by one")
	}
	if !overStub {
		t.Error("over-threshold result did not render as an offload stub")
	}
}

// TestMech_EvictionFloor — §5.1: a tool row is evicted only if its stored bytes
// ≥ 2× its rendered stub. A tiny result must survive eviction whole.
func TestMech_EvictionFloor(t *testing.T) {
	requireGate(t)
	const threshold = 16384
	r := newRig(t, routeOutDir(t, "floor"), nil, 12000, 2000, threshold)
	sid := "floor"

	// a tiny result (well under 2× a stub) in an early turn
	if err := drive(t, r, sid, "tiny", callTools(emit("tiny", 250, "string")), sayText("noted")); err != nil {
		t.Fatal(err)
	}
	// large results to build pressure past the proactive limit, so relief runs
	// on loom's own accounting (no injected refusal — a forced refusal after a
	// proactive shed would trigger a second, fold-escalating pass that is not
	// what this route tests).
	if err := drive(t, r, sid, "big1", callTools(emit("b1", 40000, "string")), sayText("ok")); err != nil {
		t.Fatal(err)
	}
	if err := drive(t, r, sid, "big2", callTools(emit("b2", 40000, "string")), sayText("ok")); err != nil {
		t.Fatal(err)
	}
	if err := drive(t, r, sid, "status", sayText("proceeding")); err != nil {
		t.Fatal(err)
	}

	// After eviction the tiny result must still be present WHOLE (its raw payload),
	// never turned into an evicted stub — stubbing it would not have paid.
	rows := durableMessages(t, r, sid)
	var tinySurvivedWhole bool
	for _, m := range rows {
		if m.Role == "tool" && len(m.Content) < 400 && strings.Contains(m.Content, "scan-row") &&
			!strings.Contains(m.Content, "evicted from context") {
			tinySurvivedWhole = true
		}
	}
	if !tinySurvivedWhole {
		t.Error("the tiny result was evicted — the 2× floor did not protect it")
	}
}

// TestMech_TerminalReliefExhausted — §5.2 step 12: when the CURRENT turn alone
// overflows (relief may not touch it), the turn ends with the recoverable
// context_exhausted error, never a crash or silent trim.
func TestMech_TerminalReliefExhausted(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "terminal"), nil, 12000, 2000, 16384)
	sid := "terminal"

	// refuse BOTH the initial send and the post-relief resend → relief cannot
	// help (nothing outside the current turn) → terminal.
	r.llm.refuse(2)
	err := drive(t, r, sid, "one huge ask", sayText("unreached"))
	if err == nil {
		t.Fatal("expected a recoverable error when relief is exhausted, got nil")
	}
	var rec *agent.RecoverableError
	if !errors.As(err, &rec) {
		t.Fatalf("expected *agent.RecoverableError, got %T: %v", err, err)
	}
	if rec.ErrorType != "context_exhausted" {
		t.Errorf("terminal error type = %q, want context_exhausted", rec.ErrorType)
	}
}

// TestMech_SkillDeactivatedOnFold — §4.5: when a skill's manage_skills load pair
// is folded, the skill's tools leave KERNEL (drop from the advertised set).
func TestMech_SkillDeactivatedOnFold(t *testing.T) {
	requireGate(t)
	const threshold = 16384
	r := newRig(t, routeOutDir(t, "skilldeact"), nil, 9000, 1000, threshold)
	r.setCompressor(&countingCompressor{summary: "STATE: audit trail loaded, then folded."})
	sid := "skilldeact"

	// Load audit-trail → it declares web_search, which must appear in KERNEL.
	if err := drive(t, r, sid, "load audit-trail",
		callTool("s1", "manage_skills", map[string]interface{}{"action": "load", "name": skillAuditTrail}),
		sayText("loaded")); err != nil {
		t.Fatal(err)
	}

	stagesBefore := r.readStages(t)
	if !toolAdvertised(stagesBefore[len(stagesBefore)-1], requiredToolName) {
		t.Fatalf("precondition: %s not advertised after loading audit-trail", requiredToolName)
	}

	// Pile conversation to force a fold that covers turn 1 (the load pair).
	for i := 0; i < 9; i++ {
		if err := drive(t, r, sid, heavyConversation(i), sayText("ack")); err != nil {
			t.Fatal(err)
		}
	}
	r.llm.refuse(1)
	if err := drive(t, r, sid, "summarise", sayText("done")); err != nil {
		t.Fatal(err)
	}

	stagesAfter := r.readStages(t)
	final := stagesAfter[len(stagesAfter)-1]
	// The load pair is now folded; web_search must have left KERNEL.
	if toolAdvertised(final, requiredToolName) {
		t.Errorf("§4.5: %s still advertised after its skill's load pair was folded — skill not deactivated", requiredToolName)
	}
	// §4.5: the fold summary must PIN a note naming the deactivated skill, so the
	// model can see the capability went out with the fold and reload it if still
	// in use — a silent deactivation leaves the model unable to tell.
	joined := ""
	for _, m := range final.Messages {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "Folded active skill") || !strings.Contains(joined, skillAuditTrail) {
		t.Errorf("§4.5: fold summary does not pin the folded-skill note naming %q", skillAuditTrail)
	}
}

// TestMech_RecallConversationOnly — §6: recall returns the span's user and
// assistant rows only; tool rows are omitted (their door is re-run).
func TestMech_RecallConversationOnly(t *testing.T) {
	requireGate(t)
	const threshold = 16384
	r := newRig(t, routeOutDir(t, "recall"), nil, 200000, 8000, threshold)
	sid := "recall"

	// Build a span that contains a tool result among conversation.
	if err := drive(t, r, sid, "first question about grants", sayText("here is my answer about grants")); err != nil {
		t.Fatal(err)
	}
	if err := drive(t, r, sid, "run a scan", callTools(emit("scan", 5000, "string")), sayText("scan done")); err != nil {
		t.Fatal(err)
	}
	if err := drive(t, r, sid, "note that", sayText("noted")); err != nil {
		t.Fatal(err)
	}

	// Now the model recalls the early span (msg:1-6, covering the tool result).
	if err := drive(t, r, sid, "what did we discuss?",
		callTool("rc", "recall", map[string]interface{}{"range": "msg:1-6"}),
		sayText("recalled")); err != nil {
		t.Fatal(err)
	}

	// The recall result is the tool row rendered as "msg:N role: content" lines.
	// It must carry the conversation (user/assistant) and OMIT tool rows.
	stages := r.readStages(t)
	var recallOut string
	for _, st := range stages {
		for _, m := range st.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "user:") && strings.Contains(m.Content, "grants") {
				recallOut = m.Content
			}
		}
	}
	if recallOut == "" {
		t.Fatal("§6: recall produced no conversation output")
	}
	if strings.Contains(recallOut, "scan-row scan-row scan-row scan-row scan-row") {
		t.Error("§6: recall returned a tool result's raw payload — tool rows must be omitted")
	}
	if !strings.Contains(recallOut, "assistant:") {
		t.Error("§6: recall omitted assistant rows — it must return user AND assistant conversation")
	}
}

// toolAdvertised reports whether a tool name appears in a stage's advertised set.
func toolAdvertised(st stage, name string) bool {
	for _, tl := range st.Tools {
		if tl.Name == name {
			return true
		}
	}
	return false
}

// TestMech_OrphanSyntheticResult — §5.2 step 7: a persisted tool_use with no
// matching result row (crash between the two writes) gets a synthetic failed
// result at compile — never stripped, because the signature is the re-run door.
func TestMech_OrphanSyntheticResult(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "orphan"), nil, 200000, 8000, 16384)
	sid := "orphan"
	ctx := context.Background()

	// Craft the crash state directly in the store: a user turn, then an
	// assistant row carrying a tool_call whose result row was never written.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// A normal turn first — this creates the session row (FK target).
	if err := drive(t, r, sid, "hello", sayText("hi")); err != nil {
		t.Fatal(err)
	}
	// Now inject the crash state: an assistant row with a tool_call whose result
	// row was never written.
	must(r.store.SaveMessage(ctx, sid, &agent.Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []agent.ToolCall{{ID: "orphan1", Name: "emit", Input: map[string]interface{}{"bytes": float64(100)}}},
	}, false))
	// (no tool result row for orphan1 — this is the orphan)

	// Reload the session and compile: the dangling tool_use must be paired with
	// the synthetic failed result.
	s := r.restoredSession(t, sid, 200000, 8000, 16384)
	ctxMsgs := compiledContext(t, s)

	var sawCall, sawSynthetic bool
	for _, m := range ctxMsgs {
		for _, c := range m.ToolCalls {
			if c.ID == "orphan1" {
				sawCall = true
			}
		}
		if strings.Contains(m.Content, "no result recorded") {
			sawSynthetic = true
		}
	}
	if !sawCall {
		t.Error("§5.2: the orphaned tool_use signature was stripped — it must survive (the re-run door)")
	}
	if !sawSynthetic {
		t.Error("§5.2 step 7: no synthetic failed result for the orphaned tool_use — API pairing would break on replay")
	}
}
