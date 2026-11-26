// Copyright 2026 Teradata
//
// Relief inside the current turn. A single-shot session (CLI) is one turn
// forever — a turn is a Chat() entry, so every message a single prompt produces
// carries the same turn number and the ladder's t−K … t−1 boundaries match
// everything in it. These routes prove the valve end-to-end for that shape:
// pressure reaches the mark, the fold sheds it, the pending pair always
// survives, and the cache-marker budget holds in every dispatched context.
//
// The fold is the only shedding mechanism. Nothing rewrites a stored row, so
// what "shed" looks like here is a row that is absent from the dispatched
// context and a summary row that stands in its place — never a stub.
//
//	LOOM_CONTEXT_OPTIMISER=1 go test -tags fts5 -run TestRung0 ./test/context-optimiser/ -v

package contextoptimiser

import (
	"fmt"
	"strings"
	"testing"
)

// summaryRowMarker is how a fold is observed from a dispatched context: the
// compiled summary row opens with it (pkg/agent's summaryRowPrefix).
const summaryRowMarker = "Session summary — the work so far"

// sawSummaryRow reports whether any dispatched context carried a summary row —
// i.e. whether a fold committed at least once during the route.
func sawSummaryRow(stages []stage) bool {
	for _, s := range stages {
		for _, m := range s.Messages {
			if strings.Contains(m.Content, summaryRowMarker) {
				return true
			}
		}
	}
	return false
}

// toolBytes totals the tool-row payload in one dispatched context — the measure
// of what the context is actually carrying.
func toolBytes(s stage) int {
	n := 0
	for _, m := range s.Messages {
		if m.Role == "tool" {
			n += len(m.Content)
		}
	}
	return n
}

// TestRung0_SingleTurnReliefShedsToTarget — one turn, consumed results push past
// the start mark mid-drive → the fold must fire and shed, the run must complete,
// and the cache-marker budget must hold in every dispatched context including
// the post-fold ones.
func TestRung0_SingleTurnReliefShedsToTarget(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-shed"), nil, 12000, 2000, 16384)
	sid := "rung0-shed"

	// Payloads render whole (under the threshold) and their sum crosses the
	// mark. Each one fits the room above G, as a step's results must: past it
	// the send is over the window and the results become the note.
	turns := make([]scriptedTurn, 0, 41)
	for i := 0; i < 40; i++ {
		turns = append(turns, callTools(emit(fmt.Sprintf("r%d", i), 1500, "string")))
	}
	turns = append(turns, sayText("done"))
	if err := drive(t, r, sid, "one long task", turns...); err != nil {
		t.Fatal(err)
	}

	stages := r.readStages(t)
	if len(stages) < 5 {
		t.Fatalf("want ≥5 provider calls, got %d", len(stages))
	}

	if !sawSummaryRow(stages) {
		t.Error("no summary row ever dispatched — relief did not shed in a single-turn session")
	}

	// Shedding is real: the last context carries less tool payload than the peak.
	peak, final := 0, toolBytes(stages[len(stages)-1])
	for _, s := range stages {
		if b := toolBytes(s); b > peak {
			peak = b
		}
	}
	if final >= peak {
		t.Errorf("tool payload never fell (peak=%d final=%d) — nothing was shed", peak, final)
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

// TestRung0_FoldIsTheOnlyValve — one turn whose mass is assistant reasoning text
// with only tiny results: there is nothing to sweep, so the fold is the only
// thing that can shed — and the pending pair must survive it whole.
func TestRung0_FoldIsTheOnlyValve(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-fold"), nil, 12000, 2000, 16384)
	sid := "rung0-fold"

	reasoning := strings.Repeat("thinking through the step in detail. ", 55) // ~2k chars
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
	if !sawSummaryRow(stages) {
		t.Fatal("the fold never fired — with tiny results it is the only valve there is")
	}

	// The pending pair survives every fold: in the final context the newest tool
	// row must render whole. A folded row would simply not be present.
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

// TestRung0_MultiTurnShedsThePast — settled turns carry the mass, the current
// turn is small: relief must shed the settled turns and leave the current turn's
// consumed rows whole. Here turns genuinely advance (25 Chat() entries: 24
// settled, one current), so
// the ladder's recent-turn window is live rather than matching everything.
func TestRung0_MultiTurnShedsThePast(t *testing.T) {
	requireGate(t)
	r := newRig(t, routeOutDir(t, "rung0-equiv"), nil, 12000, 2000, 16384)
	sid := "rung0-equiv"

	// The settled turns together carry the context past the start mark; each
	// result fits the room above G, as a step's results must.
	const settled, size = 24, 1500
	for i := 1; i <= settled; i++ {
		name := fmt.Sprintf("old%d", i)
		if err := drive(t, r, sid, "big turn "+name,
			callTools(emit(name, size, "string")), sayText("ok")); err != nil {
			t.Fatal(err)
		}
	}
	// Current turn: two small consumed results, then pressure forces relief.
	if err := drive(t, r, sid, "current turn",
		callTools(emit("cur", size, "string")),
		callTools(emit("cur2", size, "string")),
		sayText("done")); err != nil {
		t.Fatal(err)
	}

	stages := r.readStages(t)
	if !sawSummaryRow(stages) {
		t.Error("no summary row ever dispatched — pressure never fired; route is not exercising relief")
	}

	// The current turn's own results survive whole. The settled payloads cannot
	// all still be present: the final context's tool payload must be a fraction
	// of what was emitted.
	final := stages[len(stages)-1]
	whole := 0
	for _, m := range final.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "scan-row") {
			whole++
		}
	}
	if whole < 2 {
		t.Errorf("current turn's results were shed (whole=%d) — the recent window must protect them", whole)
	}
	if b := toolBytes(final); b > settled*size/2 {
		t.Errorf("final context still carries %d bytes of tool payload — the settled turns were not shed", b)
	}
}
