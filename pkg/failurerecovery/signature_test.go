// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package failurerecovery

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/types"
)

// userCtx returns a context carrying a concrete user id so the user-scoped
// guard path (which is the only mode that stores/reads signatures) is active.
func userCtx() context.Context {
	return types.ContextWithUserID(context.Background(), "u1")
}

func TestExtractOffendingTokens(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want []string // tokens that MUST be present
	}{
		{
			name: "unknown tool quoted",
			err:  "Unknown tool 'execute_sql_query'. Use teradata_search_tools() to find it.",
			want: []string{"execute_sql_query"},
		},
		{
			name: "unknown tool inside JSON-wrapped error",
			err:  `{"error":"Unknown tool 'execute_sql_query'. suggestions: [...]"}`,
			want: []string{"execute_sql_query"},
		},
		{
			name: "no such table",
			err:  "no such table 'demo_fr_orders' in schema",
			want: []string{"demo_fr_orders"},
		},
		{
			name: "schema-qualified missing relation",
			err:  `relation "agentic_demo.demo_fr_orders" does not exist`,
			want: []string{"agentic_demo.demo_fr_orders", "demo_fr_orders"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOffendingTokens(tc.err)
			set := map[string]bool{}
			for _, g := range got {
				set[g] = true
			}
			for _, w := range tc.want {
				if !set[w] {
					t.Fatalf("want token %q in %v", w, got)
				}
			}
		})
	}
}

// TestExtractOffendingTokens_ReservedYieldsNone pins the soundness rule: a
// reserved-word / syntax error must NOT yield any tier-2 token. The named
// identifier (e.g. `order`) is VALID but misused — it legitimately reappears in
// the quoted fix and in unrelated ORDER BY clauses, so blocking on it would
// false-positive. Those cases are left to tier-1 exact replay + the advisory.
func TestExtractOffendingTokens_ReservedYieldsNone(t *testing.T) {
	errs := []string{
		`(teradatasql.OperationalError) [Error 3706] [SQLState 42000] Syntax error: expected something between the 'SELECT' keyword and the 'order' keyword.`,
		`syntax error: reserved word "order" cannot be used as a column name`,
		`Syntax error: expected something between ',' and the 'order' keyword.`,
	}
	for _, e := range errs {
		if got := extractOffendingTokens(e); len(got) != 0 {
			t.Fatalf("reserved/syntax error must yield no tier-2 tokens, got %v for %q", got, e)
		}
	}
}

func TestExtractOffendingTokens_IgnoresNoise(t *testing.T) {
	// Database framing must not become offending tokens.
	got := extractOffendingTokens("[Teradata Database] [Error 3706] [Version 20.0.0.68] something failed")
	if len(got) != 0 {
		t.Fatalf("framing-only error should yield no tokens, got %v", got)
	}
}

func TestCanonicalCall_OrderIndependent(t *testing.T) {
	a := canonicalCall("tool", map[string]any{"b": 2, "a": "x"})
	b := canonicalCall("tool", map[string]any{"a": "x", "b": 2})
	if a != b {
		t.Fatalf("canonical call not order-independent: %q vs %q", a, b)
	}
}

func TestMatchTier_ExactReplay(t *testing.T) {
	sig := callSignature{CanonicalCall: canonicalCall("run_sql", map[string]any{"q": "select order from t"})}
	planned := canonicalCall("run_sql", map[string]any{"q": "select order from t"})
	if tier := matchTier(sig, planned, nil); tier != 1 {
		t.Fatalf("want tier 1 exact replay, got %d", tier)
	}
}

func TestMatchTier_TokenOverlap(t *testing.T) {
	sig := callSignature{OffendingTokens: []string{"execute_sql_query"}}
	tokens := argTokens("teradata_tool_call", map[string]any{"tool_name": "execute_sql_query"})
	if tier := matchTier(sig, "", tokens); tier != 2 {
		t.Fatalf("want tier 2 token overlap, got %d", tier)
	}
}

func TestMatchTier_NoMatch(t *testing.T) {
	sig := callSignature{OffendingTokens: []string{"execute_sql_query"}}
	tokens := argTokens("run_sql", map[string]any{"q": "select count(*) from t"})
	if tier := matchTier(sig, "", tokens); tier != 0 {
		t.Fatalf("want no match, got tier %d", tier)
	}
}

// TestPreExecGuard_BlocksWrongToolReplay stores a wrong-tool lesson with its
// signature, then verifies a later planned call carrying the same bad inner tool
// name is caught deterministically (no embedder, no LLM).
func TestPreExecGuard_BlocksWrongToolReplay(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig()) // nil embedder => deterministic only
	ctx := userCtx()

	// Learn: a gateway call with a bogus inner tool name failed, then recovered.
	e.OnToolFailure("s1", ClassToolExec, "teradata_tool_call",
		"Unknown tool 'execute_sql_query'. Use teradata_search_tools().",
		FailedCall{Tool: "teradata_tool_call", Args: map[string]any{"tool_name": "execute_sql_query"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "teradata_tool_call", nil)

	if store.count() != 1 {
		t.Fatalf("want 1 stored lesson, got %d", store.count())
	}

	// Plan: another gateway call that again names the bad inner tool.
	hints := e.PreExecGuard(ctx, testAgent, "teradata_tool_call",
		map[string]any{"tool_name": "execute_sql_query", "sql": "SELECT COUNT(*) FROM t"}, nil)
	if len(hints) == 0 {
		t.Fatalf("guard should have matched the wrong-tool replay")
	}
	if hints[0].Tier != 2 {
		t.Fatalf("want tier 2 token overlap, got %d", hints[0].Tier)
	}
}

// TestPreExecGuard_ReservedKeyword_ExactReplayTier1 verifies the sound guard for
// reserved-word misuse: an IDENTICAL failing SQL call is caught by tier-1 exact
// replay (not by matching the keyword token, which would be unsound).
func TestPreExecGuard_ReservedKeyword_ExactReplayTier1(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{"sql": "SELECT order FROM demo"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	// Exact same call -> tier-1 exact replay.
	hints := e.PreExecGuard(ctx, testAgent, "execute_sql",
		map[string]any{"sql": "SELECT order FROM demo"}, nil)
	if len(hints) == 0 || hints[0].Tier != 1 {
		t.Fatalf("want tier-1 exact replay for identical reserved-keyword call, got %+v", hints)
	}
}

// TestPreExecGuard_ReservedKeyword_SimilarNotBlocked pins soundness: a DIFFERENT
// query that merely contains the reserved word must NOT be blocked. The word is
// a valid identifier that reappears in the quoted fix and in unrelated clauses;
// deterministic blocking on it would false-positive and even block the fix.
func TestPreExecGuard_ReservedKeyword_SimilarNotBlocked(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{"sql": "SELECT order FROM demo"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	// A different query that uses `order` (bare column, quoted fix, or ORDER BY)
	// must pass the deterministic guard untouched.
	for _, q := range []string{
		`SELECT order_id, order FROM demo`, // different bare-column query
		`SELECT "order" FROM demo`,         // the quoted FIX itself
		`SELECT id FROM demo ORDER BY id`,  // unrelated ORDER BY clause
	} {
		hints := e.PreExecGuard(ctx, testAgent, "execute_sql",
			map[string]any{"sql": q}, nil)
		if len(hints) != 0 {
			t.Fatalf("reserved-keyword lesson must not block %q, got %+v", q, hints)
		}
	}
}

// TestPreExecGuard_NoFalsePositive verifies an unrelated planned call is let
// through.
func TestPreExecGuard_NoFalsePositive(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, userScopedConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassToolExec, "teradata_tool_call",
		"Unknown tool 'execute_sql_query'.",
		FailedCall{Tool: "teradata_tool_call", Args: map[string]any{"tool_name": "execute_sql_query"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "teradata_tool_call", nil)

	// A clean call with the CORRECT tool name must not trip the guard.
	hints := e.PreExecGuard(ctx, testAgent, "teradata_tool_call",
		map[string]any{"tool_name": "execute_sql", "sql": "SELECT 1"}, nil)
	if len(hints) != 0 {
		t.Fatalf("guard false-positived on a clean call: %+v", hints)
	}
}

// TestPreExecGuard_DisabledWhenNotUserScoped verifies signatures are neither
// stored nor matched in shared mode (fail-closed on PII).
func TestPreExecGuard_DisabledWhenNotUserScoped(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, enabledConfig()) // shared mode
	ctx := context.Background()

	e.OnToolFailure("s1", ClassToolExec, "teradata_tool_call",
		"Unknown tool 'execute_sql_query'.",
		FailedCall{Tool: "teradata_tool_call", Args: map[string]any{"tool_name": "execute_sql_query"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "teradata_tool_call", nil)

	hints := e.PreExecGuard(ctx, testAgent, "teradata_tool_call",
		map[string]any{"tool_name": "execute_sql_query"}, nil)
	if len(hints) != 0 {
		t.Fatalf("guard must be inert in shared mode, got %+v", hints)
	}
}

// resourceProactiveConfig enables user-scoped signatures AND the proactive hook,
// so the deterministic resource-gated advisory path is exercised end-to-end.
func resourceProactiveConfig() Config {
	cfg := userScopedConfig()
	cfg.Proactive.Enabled = true
	return cfg
}

// TestResourceGatedAdvisory_SurfacesLessonForSameTable is the core prod case:
// a reserved-keyword failure is learned on agentic_demo.demo_fr_orders, then a
// LATER task that names the SAME table but asks a DIFFERENT query (so neither
// exact-replay nor token tiers apply) still receives the lesson's fix at
// planning time — deterministically, with no LLM intent translation.
func TestResourceGatedAdvisory_SurfacesLessonForSameTable(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig()) // nil model => no LLM intent
	ctx := userCtx()

	// Learn: an unquoted reserved word broke on this specific table.
	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)
	if store.count() != 1 {
		t.Fatalf("want 1 stored lesson, got %d", store.count())
	}

	// Plan-time task: same table, DIFFERENT request (not an exact replay).
	hints := e.ProactiveHints(ctx, testAgent,
		"For every row in agentic_demo.demo_fr_orders, show order_id and the order column.", nil)
	if len(hints) == 0 {
		t.Fatalf("resource-gated advisory should surface the lesson for the same table")
	}
}

// TestResourceGatedAdvisory_BoundedToReferencedTable proves the advisory is NOT
// "inject all memory": a task that names a DIFFERENT table gets nothing, even
// though a lesson exists.
func TestResourceGatedAdvisory_BoundedToReferencedTable(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	// A task on an unrelated table must not pull the lesson in.
	hints := e.ProactiveHints(ctx, testAgent,
		"Show me the totals from sales.monthly_revenue for last quarter.", nil)
	if len(hints) != 0 {
		t.Fatalf("advisory must be bounded to the referenced table, got %+v", hints)
	}
}

// TestResourceGatedAdvisory_BareWordDoesNotGate proves a plain (non-qualified)
// word in the task cannot act as a resource key — only schema-qualified objects
// qualify, so generic prose never drags a lesson in.
func TestResourceGatedAdvisory_BareWordDoesNotGate(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	// "order" and "orders" appear as bare words but name no qualified object.
	hints := e.ProactiveHints(ctx, testAgent,
		"Please list all the orders in order of amount.", nil)
	if len(hints) != 0 {
		t.Fatalf("bare words must not gate the advisory, got %+v", hints)
	}
}

// TestResourceTokensFromArgs_DottedOnly pins that only schema-qualified names
// (and their tail) become resource tokens — bare identifiers never do.
func TestResourceTokensFromArgs_DottedOnly(t *testing.T) {
	got := resourceTokensFromArgs(map[string]any{
		"sql": "SELECT a, b FROM agentic_demo.demo_fr_orders WHERE id = 1"})
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	if !set["agentic_demo.demo_fr_orders"] || !set["demo_fr_orders"] {
		t.Fatalf("want qualified name and its tail, got %v", got)
	}
	for _, bare := range []string{"select", "from", "where", "a", "b", "id"} {
		if set[bare] {
			t.Fatalf("bare word %q must not be a resource token (got %v)", bare, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-object structural pre-exec advice (PreExecAdvice)
// ---------------------------------------------------------------------------

// TestCallSkeleton_DropsObjectKeepsShape proves the skeleton keeps the call's
// structural tokens (tool, keys, SQL keywords) and drops the object identifiers,
// so two calls that differ only in table collapse to the SAME skeleton.
func TestCallSkeleton_DropsObjectKeepsShape(t *testing.T) {
	a := callSkeleton("execute_sql",
		map[string]any{"sql": "SELECT order FROM agentic_demo.demo_fr_orders"},
		resourceTokensFromArgs(map[string]any{"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}))
	b := callSkeleton("execute_sql",
		map[string]any{"sql": "SELECT order FROM warehouse.customer_order"},
		resourceTokensFromArgs(map[string]any{"sql": "SELECT order FROM warehouse.customer_order"}))

	for _, keep := range []string{"execute_sql", "sql", "select", "order", "from"} {
		if !a[keep] {
			t.Fatalf("skeleton should keep structural token %q, got %v", keep, a)
		}
	}
	for _, drop := range []string{"agentic_demo", "demo_fr_orders", "warehouse", "customer_order"} {
		if a[drop] || b[drop] {
			t.Fatalf("skeleton must drop object token %q (a=%v b=%v)", drop, a, b)
		}
	}
	if jac, overlap := skeletonSimilarity(a, b); jac != 1.0 || overlap != 5 {
		t.Fatalf("same-shape different-table must score 1.0 with full overlap, got jac=%v overlap=%d", jac, overlap)
	}
}

// TestSkeletonSimilarity_UnrelatedScoresLow pins specificity: an unrelated call
// shares little skeleton and must not reach the advisory floor.
func TestSkeletonSimilarity_UnrelatedScoresLow(t *testing.T) {
	a := callSkeleton("execute_sql",
		map[string]any{"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}, nil)
	b := callSkeleton("teradata_search_tools",
		map[string]any{"query": "find a tool to list tables"}, nil)
	if jac, _ := skeletonSimilarity(a, b); jac >= adviceSkeletonThreshold {
		t.Fatalf("unrelated calls must score below the advisory floor, got %v", jac)
	}
}

// TestPreExecAdvice_SurfacesCrossObject is the generalization case the certain
// tiers cannot cover: a lesson learned while failing on table A is surfaced for a
// planned call of the SAME SHAPE on a DIFFERENT table B. No exact replay, no
// offending-token, no same-table resource gate applies — only the structural
// skeleton match. Fully deterministic (nil embedder).
func TestPreExecAdvice_SurfacesCrossObject(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	// Learn on table A: an unquoted reserved word broke the query.
	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)
	if store.count() != 1 {
		t.Fatalf("want 1 stored lesson, got %d", store.count())
	}

	// Plan a call on a DIFFERENT table that repeats the same reserved-word defect.
	advice := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql",
		map[string]any{"sql": "SELECT order FROM warehouse.customer_order"}, nil)
	if len(advice) == 0 {
		t.Fatalf("advice should surface the lesson for the same call shape on a different table")
	}
}

// TestPreExecAdvice_DedupNoLoop pins loop safety: the SAME planned call is
// advised at most once per session, so the advisory can never trap the model by
// re-surfacing on a call it deliberately re-issued.
func TestPreExecAdvice_DedupNoLoop(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	args := map[string]any{"sql": "SELECT order FROM warehouse.customer_order"}
	if first := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql", args, nil); len(first) == 0 {
		t.Fatalf("first advice should fire")
	}
	if second := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql", args, nil); len(second) != 0 {
		t.Fatalf("second identical call must be deduped, got %+v", second)
	}
}

// TestPreExecAdvice_SkipsExactReplay verifies advice does NOT fire for an exact
// replay on the SAME object — that is PreExecGuard's certain-block job, not the
// advisory's. (Here the planned call equals the failing call verbatim.)
func TestPreExecAdvice_SkipsExactReplay(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	fail := map[string]any{"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}
	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: fail})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	if advice := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql", fail, nil); len(advice) != 0 {
		t.Fatalf("advice must defer exact replay to the guard, got %+v", advice)
	}
}

// TestPreExecAdvice_UnrelatedCallNoAdvice verifies specificity end-to-end: an
// unrelated planned call pulls no advice even though a lesson exists.
func TestPreExecAdvice_UnrelatedCallNoAdvice(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, resourceProactiveConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	advice := e.PreExecAdvice(ctx, testAgent, "s2", "teradata_search_tools",
		map[string]any{"query": "find a tool to list tables"}, nil)
	if len(advice) != 0 {
		t.Fatalf("unrelated call must pull no advice, got %+v", advice)
	}
}

// TestPreExecAdvice_InertWhenNotUserScoped verifies the advice obeys the same PII
// fail-closed rule as the rest of the deterministic layer.
func TestPreExecAdvice_InertWhenNotUserScoped(t *testing.T) {
	store := &fakeStore{}
	cfg := enabledConfig()
	cfg.Proactive.Enabled = true
	e := NewEngine(store, nil, cfg) // shared mode
	ctx := context.Background()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	advice := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql",
		map[string]any{"sql": "SELECT order FROM warehouse.customer_order"}, nil)
	if len(advice) != 0 {
		t.Fatalf("advice must be inert in shared mode, got %+v", advice)
	}
}

// ---------------------------------------------------------------------------
// SemanticOnly mode (deterministic layers off, FTS + embedding retrieval kept)
// ---------------------------------------------------------------------------

// semanticOnlyConfig enables user-scoped signatures and the proactive hook, but
// disables every deterministic signature-matching layer.
func semanticOnlyConfig() Config {
	cfg := resourceProactiveConfig()
	cfg.SemanticOnly = true
	return cfg
}

// TestSemanticOnly_DisablesPreExecGuard proves the exact-replay / token-overlap
// guard does not fire in semantic-only mode, even for a certain repeat.
func TestSemanticOnly_DisablesPreExecGuard(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, semanticOnlyConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassToolExec, "teradata_tool_call",
		"Unknown tool 'execute_sql_query'. Use teradata_search_tools().",
		FailedCall{Tool: "teradata_tool_call", Args: map[string]any{"tool_name": "execute_sql_query"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "teradata_tool_call", nil)
	if store.count() != 1 {
		t.Fatalf("want 1 stored lesson, got %d", store.count())
	}

	hints := e.PreExecGuard(ctx, testAgent, "teradata_tool_call",
		map[string]any{"tool_name": "execute_sql_query", "sql": "SELECT COUNT(*) FROM t"}, nil)
	if len(hints) != 0 {
		t.Fatalf("guard must be off in semantic-only mode, got %+v", hints)
	}
}

// TestSemanticOnly_DisablesPreExecAdvice proves the cross-object skeleton
// advisory does not fire in semantic-only mode.
func TestSemanticOnly_DisablesPreExecAdvice(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, semanticOnlyConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	advice := e.PreExecAdvice(ctx, testAgent, "s2", "execute_sql",
		map[string]any{"sql": "SELECT order FROM warehouse.customer_order"}, nil)
	if len(advice) != 0 {
		t.Fatalf("cross-object advice must be off in semantic-only mode, got %+v", advice)
	}
}

// TestSemanticOnly_DisablesResourceGatedAdvisory mirrors
// TestResourceGatedAdvisory_SurfacesLessonForSameTable with the flag on: the
// task names the exact table a lesson failed on, yet no deterministic hint is
// injected. (The FTS fallback does not match this intent's vocabulary, so the
// result must be empty.)
func TestSemanticOnly_DisablesResourceGatedAdvisory(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, semanticOnlyConfig())
	ctx := userCtx()

	e.OnToolFailure("s1", ClassGuardrail, "execute_sql",
		`Syntax error: expected something between 'SELECT' and the 'order' keyword`,
		FailedCall{Tool: "execute_sql", Args: map[string]any{
			"sql": "SELECT order FROM agentic_demo.demo_fr_orders"}})
	e.OnToolSuccess(ctx, testAgent, "s1", "execute_sql", nil)

	hints := e.ProactiveHints(ctx, testAgent,
		"For every row in agentic_demo.demo_fr_orders, show order_id and the order column.", nil)
	if len(hints) != 0 {
		t.Fatalf("resource-gated advisory must be off in semantic-only mode, got %+v", hints)
	}
}

// TestSemanticOnly_KeepsSemanticRetrieval pins what the mode KEEPS: proactive
// top-K retrieval (FTS fallback here, nil embedder) and reactive recall both
// still surface a stored lesson.
func TestSemanticOnly_KeepsSemanticRetrieval(t *testing.T) {
	store := &fakeStore{}
	e := NewEngine(store, nil, semanticOnlyConfig())
	ctx := userCtx()

	seedLessonCtx(t, e, ctx, "run_sql", `relation "users" does not exist`)

	// Proactive: intent shares vocabulary with the stored abstract problem.
	if hints := e.ProactiveHints(ctx, testAgent,
		"referenced a table that does not exist", nil); len(hints) == 0 {
		t.Fatalf("semantic-only mode must keep proactive FTS/embedding retrieval")
	}

	// Reactive: a repeat-class failure still recalls its lesson. The query
	// shares vocabulary with the stored abstraction so the FTS fallback
	// (nil embedder) matches it.
	if hints := e.ReactiveHints(ctx, testAgent, testSession, ClassToolExec, "run_sql",
		`relation "users" does not exist`, nil); len(hints) == 0 {
		t.Fatalf("semantic-only mode must keep reactive recall")
	}
}
