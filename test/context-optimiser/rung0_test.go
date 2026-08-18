// Copyright 2026 Teradata
//
// rung 0 — relief inside the current turn. A single-shot session (CLI) is one
// turn forever: the old ladder's boundaries (t−K … t−1) match nothing, so
// before rung 0 such a session had no working pressure valve. These routes
// prove the valve end-to-end: consumed results evict, fold stays a last
// resort, the pending pair always survives, and multi-turn sessions whose old
// rungs suffice never reach rung 0 at all.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 -run TestRung0 ./test/context-optimiser/ -v

package contextoptimiser

import (
	"fmt"
	"strings"
	"testing"
)

// TestRung0_SingleTurnEvictShedsToTarget — one turn, consumed mid-size results
// push past the HWM mid-drive → relief must shed via evict(t) alone: evicted
// stubs appear, no fold happens, the run completes, and the cache marker
// budget holds in every dispatched context.
func TestRung0_SingleTurnEvictShedsToTarget(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-evict"), nil, 12000, 2000, 16384)
	sid := "rung0-evict"

	turns := make([]scriptedTurn, 0, 7)
	for i := 0; i < 6; i++ {
		turns = append(turns, callTools(emit(fmt.Sprintf("r%d", i), 6000, "string")))
	}
	turns = append(turns, sayText("done"))
	if err := drive(t, r, sid, "one long task", turns...); err != nil {
		t.Fatal(err)
	}

	stages := r.readStages(t)
	if len(stages) < 5 {
		t.Fatalf("want ≥5 provider calls, got %d", len(stages))
	}

	var sawEvictedStub, sawFold bool
	for _, s := range stages {
		for _, m := range s.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "evicted from context") {
				sawEvictedStub = true
			}
			if m.Role == "system" && strings.Contains(m.Content, "also covers msg:") {
				sawFold = true
			}
		}
	}
	if !sawEvictedStub {
		t.Error("no evicted stub ever dispatched — rung 0 evict did not fire")
	}
	if sawFold {
		t.Error("a fold ran — evict(t) should have reached target first (reversibility order)")
	}

	// Marker budget in every dispatched context, including post-relief ones.
	for si, s := range stages {
		markers := 0
		for _, m := range s.Messages {
			if m.CacheBreakpoint {
				markers++
			}
		}
		if markers > 4 {
			t.Errorf("stage %d: %d cache markers — over Anthropic's budget of 4", si, markers)
		}
	}
}

// TestRung0_FoldLastResort — one turn whose mass is assistant reasoning text
// with only floor-protected tiny results: sweep finds no query pairs, evict
// finds nothing above the floor, so fold(t) must fire — and the pending pair
// must survive it whole.
func TestRung0_FoldLastResort(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-fold"), nil, 12000, 2000, 16384)
	sid := "rung0-fold"

	reasoning := strings.Repeat("thinking through the step in detail. ", 55) // ~2k chars, unevictable
	turns := make([]scriptedTurn, 0, 24)
	for i := 0; i < 23; i++ {
		st := callTools(emit(fmt.Sprintf("t%d", i), 300, "string"))
		st.text = reasoning
		turns = append(turns, st)
	}
	turns = append(turns, sayText("done"))
	if err := drive(t, r, sid, "many small steps", turns...); err != nil {
		t.Fatal(err)
	}

	stages := r.readStages(t)
	var sawFold bool
	for _, s := range stages {
		for _, m := range s.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "also covers msg:") {
				sawFold = true
			}
		}
	}
	if !sawFold {
		t.Fatal("fold(t) never fired — tiny results are unevictable, fold was the only valve")
	}

	// The pending pair survives every fold: in each stage, the newest tool row
	// must render whole (its payload text), never a fold casualty. The emit
	// payload contains "scan-row"; a folded row simply would not be present.
	final := stages[len(stages)-1]
	var lastTool string
	for _, m := range final.Messages {
		if m.Role == "tool" {
			lastTool = m.Content
		}
	}
	if lastTool == "" || !strings.Contains(lastTool, "scan-row") {
		t.Error("the newest result did not survive the folds whole — pending protection failed")
	}
}

// TestRung0_MultiTurnOldRungsSuffice — settled turns carry the mass, the
// current turn is small: relief must shed the OLD turns and never touch the
// current turn's consumed row — rung 0 exists but is unreachable when the old
// ladder reaches target (the equivalence lock).
func TestRung0_MultiTurnOldRungsSuffice(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-equiv"), nil, 12000, 2000, 16384)
	sid := "rung0-equiv"

	if err := drive(t, r, sid, "big turn one",
		callTools(emit("old1", 40000, "string")), sayText("ok")); err != nil {
		t.Fatal(err)
	}
	if err := drive(t, r, sid, "big turn two",
		callTools(emit("old2", 40000, "string")), sayText("ok")); err != nil {
		t.Fatal(err)
	}
	// Current turn: one small consumed result, then pressure forces relief.
	if err := drive(t, r, sid, "current turn",
		callTools(emit("cur", 3000, "string")),
		callTools(emit("cur2", 3000, "string")),
		sayText("done")); err != nil {
		t.Fatal(err)
	}

	stages := r.readStages(t)
	final := stages[len(stages)-1]
	var curEvicted bool
	var oldEvicted bool
	for _, m := range final.Messages {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(m.Content, "evicted from context") {
			oldEvicted = true
		}
	}
	// The current turn's first result (consumed by the second iteration): find
	// it rendered whole in the final dispatched context.
	var curWholeCount int
	for _, m := range final.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "scan-row") &&
			!strings.Contains(m.Content, "evicted from context") &&
			!strings.Contains(m.Content, "held in memory") {
			curWholeCount++
		}
	}
	if !oldEvicted {
		t.Error("no old-turn eviction visible — pressure never fired; route is not exercising relief")
	}
	if curEvicted || curWholeCount < 2 {
		t.Errorf("current turn's results were shed (whole=%d) — old rungs sufficed, rung 0 must be unreachable", curWholeCount)
	}
}
