// Copyright 2026 Teradata
//
// lifecycle — THE route. One scripted conversation walks every mechanism of the
// ContextCompilation design in order; the compiled context is generated at each
// step and checked against the HLD. Relief is driven by injecting the provider
// refusal (refuse); fold's summary is deterministic (scripted compressor).
//
// TWO relief moments, because eviction and fold are sequential relief steps and
// fold consumes eviction's output — one refusal cannot show both:
//   - moment 1 (few results, little conversation): eviction ALONE relieves, so
//     evicted stubs are visible in the resend context.
//   - moment 2 (much conversation, unevictable): eviction is insufficient, so
//     fold fires and the summary is installed.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 -run TestFullLifecycle ./test/context-optimiser/ -v
//
// Writes out/lifecycle/stages.md — every compiled context, to be read.

package contextoptimiser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFullLifecycle(t *testing.T) {
	requireGate(t)

	const threshold = 16384
	// budget 12k / reserved 2k → usable 10k, target 7.5k tokens (~22.5 KB by
	// bytes/3). A few large results trip an injected refusal that eviction alone
	// clears; later, ~24 KB of conversation forces fold.
	out := routeOutDir(t, "lifecycle")
	r := newRig(t, out, nil, 12000, 2000, threshold)
	comp := &countingCompressor{summary: "SESSION STATE: grant audit in progress; decisions recorded."}
	r.setCompressor(comp)
	sid := "lifecycle"
	ctx := context.Background()

	step := func(user string, resp ...scriptedTurn) {
		t.Helper()
		r.llm.turns = append(r.llm.turns, resp...)
		if _, err := r.agent.Chat(ctx, sid, user); err != nil {
			t.Fatalf("turn %q: %v", user, err)
		}
	}
	mark := func() int { return len(r.readStages(t)) }

	// 1. skill load
	step("Load the grant-review skill.",
		callTool("s1", "manage_skills", map[string]interface{}{"action": "load", "name": skillGrantReview}),
		sayText("Loaded."))
	// 2. small result → whole
	step("Small scan.", callTools(emit("small", 300, "string")), sayText("noted"))
	// 3. large result → offload stub (current turn)
	offloadMark := mark()
	step("Big scan.", callTools(emit("big1", 40000, "string")), sayText("loaded big1"))
	// 4-5. two more large results
	step("Another scan.", callTools(emit("big2", 40000, "string")), sayText("ok"))
	step("And another.", callTools(emit("big3", 40000, "string")), sayText("ok"))
	// 6. RELIEF MOMENT 1 — eviction alone relieves
	evictMark := mark()
	r.llm.refuse(1)
	step("Status?", sayText("Three scans loaded; proceeding."))
	// 7+. pile on unevictable conversation until it crosses the start mark on its
	// own — big tool results were evicted at moment 1, so only conversation
	// (which renders whole) can drive the fold now.
	for i := 0; i < 18; i++ {
		step(heavyConversation(i), sayText("ack"))
	}
	// RELIEF MOMENT 2 — eviction insufficient, fold fires
	foldMark := mark()
	step("Summarise the audit so far.", sayText("Here is the audit summary."))
	// 17. recall across the fold
	step("What did I first ask you to do?", sayText("You asked me to load the grant-review skill."))

	stages := r.readStages(t)
	writeStages(t, out, stages)
	t.Logf("compiled contexts → %s", filepath.Join(out, "stages.md"))

	// A. §5.5 offload stub with message_id, in the big1 turn.
	if !anyStageHas(stages, offloadMark, evictMark, "held in memory this turn", "query_tool_result(message_id=") {
		t.Error("A. no offload stub with message_id when the large result arrived")
	}
	// B. §5.2 eviction: relief moment 1's resend shows evicted stubs.
	if !anyStageHas(stages, evictMark, foldMark, "evicted from context", "re-run the call above") {
		t.Error("B. relief moment 1 produced no evicted stub — eviction did not run")
	}
	// C. §5.4 fold: after moment 2, the summary is installed with its coverage line.
	finalText := contextText(stages[len(stages)-1])
	if !strings.Contains(finalText, comp.summary) {
		t.Error("C. fold summary is not in the final context")
	}
	if !strings.Contains(finalText, "covers msg") {
		t.Error("C. fold summary carries no 'covers msg' coverage line")
	}
	if comp.count() == 0 {
		t.Error("C. compressor never called — no fold happened")
	}
	// D. no dead-design markers anywhere.
	for _, st := range stages {
		for _, m := range st.Messages {
			for _, dead := range []string{"reference_id=", "stored in memory ("} {
				if strings.Contains(m.Content, dead) {
					t.Errorf("D. dead-design marker %q in a compiled context", dead)
				}
			}
		}
	}
	// E. §4.5/§5.5 message_id is the DURABLE id — resolves to a real row.
	assertOffloadIDsAreDurable(t, r, sid, stages[len(stages)-1])
	// F. §8 reload parity: a twin rebuilt from the rows compiles identically.
	live := compiledContext(t, r.liveSession(t, sid))
	restored := compiledContext(t, r.restoredSession(t, sid, 12000, 2000, threshold))
	if len(live) != len(restored) {
		t.Errorf("F. reload parity: live compiles to %d messages, restored to %d", len(live), len(restored))
	} else {
		for i := range live {
			if live[i].Role != restored[i].Role || live[i].Content != restored[i].Content {
				t.Errorf("F. reload parity: message %d differs\n  live     [%s]: %s\n  restored [%s]: %s",
					i, live[i].Role, trim(live[i].Content), restored[i].Role, trim(restored[i].Content))
				break
			}
		}
	}
}

func anyStageHas(stages []stage, lo, hi int, subs ...string) bool {
	if hi > len(stages) {
		hi = len(stages)
	}
	for i := lo; i < hi; i++ {
		for _, m := range stages[i].Messages {
			all := true
			for _, sub := range subs {
				if !strings.Contains(m.Content, sub) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

func contextText(st stage) string {
	var b strings.Builder
	for _, m := range st.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}

func assertOffloadIDsAreDurable(t *testing.T, r *rig, sid string, st stage) {
	t.Helper()
	rows := durableMessages(t, r, sid)
	for arrIdx, m := range st.Messages {
		if !strings.Contains(m.Content, "held in memory this turn") {
			continue
		}
		id := between(m.Content, "message_id=", ",")
		found := false
		for _, row := range rows {
			if row.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("E. offload stub message_id=%s (array index %d) resolves to no durable row — id is not the seq", id, arrIdx)
		}
	}
}

func writeStages(t *testing.T, out string, stages []stage) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# lifecycle — %d provider calls\n\n", len(stages))
	for i, st := range stages {
		fmt.Fprintf(&b, "## Call %d (turn %d) — %d messages\n\n", i+1, st.Turn, len(st.Messages))
		for j, m := range st.Messages {
			c := strings.ReplaceAll(m.Content, "\n", " ")
			if len(c) > 260 {
				c = c[:260] + " …"
			}
			fmt.Fprintf(&b, "- [%d] %-9s %6dB | %s\n", j, m.Role, len(m.Content), c)
		}
		b.WriteString("\n")
	}
	_ = os.WriteFile(filepath.Join(out, "stages.md"), []byte(b.String()), 0o644)
}
