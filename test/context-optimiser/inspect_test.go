// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
//
// inspect — the first route on the ContextCompilation design. It drives the
// REAL implemented loom through a scripted conversation and writes the exact
// compiled context of every provider call to out/inspect/stages.md, so the
// generated context can be READ and judged against the HLD. It also asserts
// the §5.2 render cases that are byte-exact in the design.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 -run TestInspect ./test/context-optimiser/ -v

package contextoptimiser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspect_RenderCases(t *testing.T) {
	requireGate(t)

	// threshold 16384 (HLD §4.1/§5.1); small budget so the context is compact
	// and easy to read. reservedOutput small — no relief wanted in this route.
	const threshold = 16384
	out := routeOutDir(t, "inspect")
	r := newRig(t, out, nil, 200000, 8000, threshold)
	sid := "inspect"
	ctx := context.Background()

	// Turn 1 — load a skill: body should enter L1 once, tools advertised.
	r.llm.turns = append(r.llm.turns,
		callTool("s1", "manage_skills", map[string]interface{}{"action": "load", "name": skillGrantReview}),
		sayText("Loaded grant-review."))
	if _, err := r.agent.Chat(ctx, sid, "Load the grant-review skill."); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Turn 2 — a SMALL tool result (< threshold): must render whole, no stub.
	r.llm.turns = append(r.llm.turns,
		callTools(emit("small", 300, "string")),
		sayText("Small result noted."))
	if _, err := r.agent.Chat(ctx, sid, "Emit a small payload."); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Turn 3 — a LARGE tool result (> threshold), current turn: must render as
	// the offload stub with message_id, payload held in memory this turn.
	r.llm.turns = append(r.llm.turns,
		callTools(emit("big", 40000, "string")),
		sayText("Large result loaded."))
	if _, err := r.agent.Chat(ctx, sid, "Load a large payload."); err != nil {
		t.Fatalf("turn 3: %v", err)
	}

	// Turn 4 — a plain conversational turn: this advances T, so turn 3's large
	// result is now a PRIOR turn — in standalone loom its in-memory content
	// should already be the persisted (truncated) form.
	r.llm.turns = append(r.llm.turns, sayText("Understood."))
	if _, err := r.agent.Chat(ctx, sid, "Thanks — hold there."); err != nil {
		t.Fatalf("turn 4: %v", err)
	}

	// --- write every compiled context to a readable file ---
	stages := r.readStages(t)
	var b strings.Builder
	fmt.Fprintf(&b, "# inspect — %d provider calls\n\n", len(stages))
	for i, st := range stages {
		fmt.Fprintf(&b, "## Call %d (turn %d) — %d messages, %d tools\n\n", i+1, st.Turn, len(st.Messages), len(st.Tools))
		for j, m := range st.Messages {
			c := strings.ReplaceAll(m.Content, "\n", " ")
			if len(c) > 300 {
				c = c[:300] + " …"
			}
			fmt.Fprintf(&b, "- [%d] %-9s %5dB | %s\n", j, m.Role, len(m.Content), c)
		}
		b.WriteString("\n")
	}
	dst := filepath.Join(out, "stages.md")
	if err := os.WriteFile(dst, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write stages: %v", err)
	}
	t.Logf("compiled contexts written to %s", dst)

	// --- byte-exact render-case assertions (HLD §5.2 / §5.5), read off the
	//     LAST compiled context (turn 4), where all three results have arrived ---
	if len(stages) == 0 {
		t.Fatal("no provider calls were captured — dump not produced")
	}
	last := stages[len(stages)-1]

	var sawSkillBody, sawSmallWhole, sawOffloadStub bool
	skillBodyCount := 0
	for _, m := range last.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "GRANT REVIEW PROCEDURE") {
			skillBodyCount++
			sawSkillBody = true
		}
		if m.Role == "tool" {
			// small result: whole scan-row payload, not a stub
			if strings.Contains(m.Content, "scan-row") && !strings.Contains(m.Content, "result, ~") {
				sawSmallWhole = true
			}
			// large current-turn result: the offload stub (HLD §5.5)
			if strings.Contains(m.Content, "held in memory this turn") &&
				strings.Contains(m.Content, "query_tool_result(message_id=") {
				sawOffloadStub = true
			}
		}
	}

	// These are observations first — a failure here is a real finding about the
	// generated context, to be read against stages.md, not a flaky assert.
	if !sawSkillBody {
		t.Errorf("§compile: the loaded skill body is not in the final context")
	}
	if skillBodyCount > 1 {
		t.Errorf("§compile: skill body appears %d times in one context — must be once", skillBodyCount)
	}
	if !sawSmallWhole {
		t.Errorf("§5.2: the small result did not render whole")
	}
	// The offload stub only survives in context while the result is current-turn.
	// By turn 4 the big result is a prior turn; whether it still shows as an
	// offload stub or has become an evicted/truncated form is exactly what
	// stages.md tells us — so this is logged, not failed.
	t.Logf("offload stub present in final context: %v (see stages.md for the prior-turn render)", sawOffloadStub)

	// No dead-design markers anywhere.
	for _, st := range stages {
		for _, m := range st.Messages {
			for _, dead := range []string{"reference_id=", "\U0001F4A1", "stored in memory ("} {
				if strings.Contains(m.Content, dead) {
					t.Errorf("dead-design marker %q in a compiled context (call turn %d)", dead, st.Turn)
				}
			}
		}
	}
}
