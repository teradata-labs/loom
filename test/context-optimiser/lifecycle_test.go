// Copyright 2026 Teradata
//
// lifecycle — THE route. One scripted conversation walks every mechanism of the
// ContextCompilation design in order; the compiled context is generated at each
// step and checked against the HLD. Relief is driven by injecting the provider
// refusal (refuse); fold's summary is deterministic (scripted compressor).
//
// TWO relief moments, both folds, because a fold sheds what is settled at the
// time and a later turn settles more:
//   - moment 1 (a few large scans, little conversation): the fold sheds the
//     settled scans and the summary row is in the resend context.
//   - moment 2 (much conversation): a second fold, now over conversation, and
//     the summary stands in for it.
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
	// bytes/3). A few large results trip an injected refusal that a fold
	// clears; later, ~24 KB of conversation forces a second fold.
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
	// 3. large result → bounded at arrival (current turn)
	step("Big scan.", callTools(emit("big1", 40000, "string")), sayText("loaded big1"))
	// 4-5. two more large results
	step("Another scan.", callTools(emit("big2", 40000, "string")), sayText("ok"))
	step("And another.", callTools(emit("big3", 40000, "string")), sayText("ok"))
	// 6. RELIEF MOMENT 1 — the fold sheds the settled scans
	evictMark := mark()
	r.llm.refuse(1)
	step("Status?", sayText("Three scans loaded; proceeding."))
	// 7+. pile on conversation until it crosses the start mark on its own — the
	// big tool results were shed at moment 1, so only conversation can drive
	// the next fold, and the fold takes the conversation with it.
	for i := 0; i < 18; i++ {
		step(heavyConversation(i), sayText("ack"))
	}
	// RELIEF MOMENT 2 — a second fold, now over conversation
	foldMark := mark()
	step("Summarise the audit so far.", sayText("Here is the audit summary."))
	// 17. recall across the fold
	step("What did I first ask you to do?", sayText("You asked me to load the grant-review skill."))

	stages := r.readStages(t)
	writeStages(t, out, stages)
	t.Logf("compiled contexts → %s", filepath.Join(out, "stages.md"))

	// A. The §4.1 persist bound is not asserted here: this route folds, and a
	// folded row leaves the database read, so by the end of it the large results
	// are gone from the durable view rather than present-and-bounded.
	// TestMech_ThresholdRule proves the bound on a route that does not fold.
	// B. §5.2 relief: the first relief moment sheds by folding, and the summary
	// row it installs is what stands in for the shed history.
	if !anyStageHas(stages, evictMark, foldMark, summaryRowMarker) {
		t.Error("B. relief moment 1 installed no summary row — the fold did not run")
	}
	// C. fold over conversation: the final context opens on the summary, and
	// the conversation it stands in for is gone — the opening ask included —
	// leaving only the last turns.
	finalText := contextText(stages[len(stages)-1])
	if !strings.Contains(finalText, summaryRowMarker) {
		t.Error("C. the final context holds no fold summary")
	}
	if strings.Contains(finalText, "Load the grant-review skill.") {
		t.Error("C. the opening ask is still in context: the fold did not take the conversation")
	}
	// A fold is answered by the model (in-loop, at G) or by the compressor
	// (relief after a refusal); which one runs first depends on the estimate,
	// and the request's tool list is part of it.
	if comp.count() == 0 && r.llm.foldsAnswered == 0 {
		t.Error("C. neither the model nor the compressor summarised — no fold happened")
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
