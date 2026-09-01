// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"context"
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

const (
	trToolCall  = loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL
	trSkill     = loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_SKILL_ACTIVATION
	trHuman     = loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST
	trSubagent  = loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_SUBAGENT_SPAWN
	trWorkflow  = loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_WORKFLOW_STEP
	modeDisable = loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED
	modeEnable  = loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_ENABLED
)

// TestResolveImplicitPolicy_DefaultsToEnabled is the opt-out guarantee: an
// agent that says nothing about tasks still gets them, or the whole rendering
// layer is invisible out of the box.
func TestResolveImplicitPolicy_DefaultsToEnabled(t *testing.T) {
	for name, cfg := range map[string]*loomv1.ImplicitTaskConfig{
		"nil config":         nil,
		"empty config":       {},
		"mode unspecified":   {Mode: loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_UNSPECIFIED},
		"explicitly enabled": {Mode: modeEnable},
	} {
		p := ResolveImplicitPolicy(cfg)
		if !p.Enabled {
			t.Errorf("%s: implicit emission must default to ON (opt-out)", name)
		}
		if !p.Allows(trToolCall) {
			t.Errorf("%s: tool calls must be a default trigger", name)
		}
		if p.MaxPerSession != DefaultMaxImplicitPerSession {
			t.Errorf("%s: expected default cap %d, got %d", name, DefaultMaxImplicitPerSession, p.MaxPerSession)
		}
	}
}

// TestResolveImplicitPolicy_AgentInvisibleByDefault guards the context window:
// if implicit tasks were agent-visible, every turn's bookkeeping would enter
// the prompt on every later turn.
func TestResolveImplicitPolicy_AgentInvisibleByDefault(t *testing.T) {
	p := ResolveImplicitPolicy(nil)
	if p.AgentVisible {
		t.Fatal("implicit tasks must NOT be agent-visible by default")
	}
	excluded := p.ExcludedCreatedVia()
	if len(excluded) != 1 || excluded[0] != taskctx.CreatedViaImplicit {
		t.Fatalf("expected implicit tasks excluded from agent queries, got %v", excluded)
	}

	// Opting in removes the exclusion, so an operator who wants the agent to
	// see them gets exactly that.
	visible := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{AgentVisible: true})
	if len(visible.ExcludedCreatedVia()) != 0 {
		t.Error("agent_visible=true must stop excluding implicit tasks")
	}
}

func TestResolveImplicitPolicy_Disable(t *testing.T) {
	p := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{Mode: modeDisable})
	if p.Enabled || p.Allows(trToolCall) {
		t.Fatal("DISABLED must stop all implicit emission")
	}
}

// TestResolveImplicitPolicy_GranularControl covers the two shapes an operator
// actually needs: "only these" and "everything except these".
func TestResolveImplicitPolicy_GranularControl(t *testing.T) {
	// Only-these.
	only := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		Triggers: []loomv1.ImplicitTaskTrigger{trHuman},
	})
	if !only.Allows(trHuman) {
		t.Error("explicitly listed trigger must be allowed")
	}
	if only.Allows(trToolCall) {
		t.Error("an explicit trigger list must replace the defaults, not extend them")
	}

	// Everything-except, without enumerating the rest.
	except := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		ExcludedTriggers: []loomv1.ImplicitTaskTrigger{trToolCall},
	})
	if except.Allows(trToolCall) {
		t.Error("excluded trigger must be removed")
	}
	if !except.Allows(trHuman) || !except.Allows(trSubagent) {
		t.Error("exclusion must leave the other defaults intact")
	}

	// Exclusion applies after the allow-list.
	both := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		Triggers:         []loomv1.ImplicitTaskTrigger{trToolCall, trHuman},
		ExcludedTriggers: []loomv1.ImplicitTaskTrigger{trHuman},
	})
	if !both.Allows(trToolCall) || both.Allows(trHuman) {
		t.Error("excluded_triggers must be applied after triggers")
	}

	// Excluding everything is equivalent to disabling, not to a silent no-op
	// that leaves Enabled true with an empty trigger set.
	none := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		ExcludedTriggers: DefaultImplicitTriggers(),
	})
	if none.Enabled {
		t.Error("excluding every trigger must resolve to disabled")
	}
}

// TestDefaultTriggers_NoDoubleCounting pins the reasoning that skills and
// workflows are excluded by default: both already create their own tasks, so
// including them would put two tasks on the board for one cause.
func TestDefaultTriggers_NoDoubleCounting(t *testing.T) {
	p := ResolveImplicitPolicy(nil)
	if p.Allows(trSkill) {
		t.Error("SKILL_ACTIVATION must be off by default — the skills emitter already creates tasks")
	}
	if p.Allows(trWorkflow) {
		t.Error("WORKFLOW_STEP must be off by default — the task-tracked orchestrator already creates tasks")
	}
	// Still opt-in-able for agents that disabled those emitters.
	opted := ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		Triggers: []loomv1.ImplicitTaskTrigger{trSkill},
	})
	if !opted.Allows(trSkill) {
		t.Error("SKILL_ACTIVATION must remain available when explicitly requested")
	}
}

func TestImplicitTitle(t *testing.T) {
	// A human scanning the board should see what the turn was about.
	got := implicitTitle(TurnRequest{UserMessage: "  Find the slow queries\nand fix them  ", TurnIndex: 3})
	if got != "Find the slow queries" {
		t.Errorf("expected the first line of the user message, got %q", got)
	}

	// No message: fall back to a turn number rather than an empty title.
	if got := implicitTitle(TurnRequest{TurnIndex: 6}); got != "Turn 7" {
		t.Errorf("expected 'Turn 7', got %q", got)
	}

	// Long titles are cut on a rune boundary.
	long := implicitTitle(TurnRequest{UserMessage: strings.Repeat("→", 200)})
	if len(long) > MaxImplicitTitleLen+4 {
		t.Errorf("title not bounded: %d bytes", len(long))
	}
	for _, r := range long {
		if r == '�' {
			t.Fatalf("title truncated mid-rune: %q", long)
		}
	}
}

func TestBinding_FirstSetWinsAndIsVisibleThroughOldContext(t *testing.T) {
	// A writer captures the context before any task exists...
	ctx, binding := taskctx.ContextWithBinding(context.Background())
	captured := ctx

	if _, ok := taskctx.AttributionFromContext(captured); ok {
		t.Fatal("nothing should be attributed before the binding is set")
	}

	// ...then the runtime mints a task mid-turn.
	if !binding.Set(taskctx.Attribution{TaskID: "task-1", SessionID: "s1"}) {
		t.Fatal("first Set must succeed")
	}

	// The previously-captured context now sees it. This is the whole reason the
	// binding exists: lazy creation cannot replace a context already passed down.
	got, ok := taskctx.AttributionFromContext(captured)
	if !ok || got.TaskID != "task-1" {
		t.Fatalf("captured context did not observe the late binding: %+v ok=%v", got, ok)
	}

	// A second trigger in the same turn must not retarget the work.
	if binding.Set(taskctx.Attribution{TaskID: "task-2"}) {
		t.Error("second Set must not win")
	}
	if got, _ := taskctx.AttributionFromContext(captured); got.TaskID != "task-1" {
		t.Errorf("attribution was retargeted to %q", got.TaskID)
	}

	// An empty TaskID is refused rather than stored as a half-set binding.
	fresh := taskctx.NewBinding()
	if fresh.Set(taskctx.Attribution{SessionID: "s"}) {
		t.Error("Set with no TaskID must be refused")
	}
	if _, ok := fresh.Get(); ok {
		t.Error("refused Set must leave the binding unset")
	}
}

func TestBinding_NilSafe(t *testing.T) {
	var b *taskctx.Binding
	if b.Set(taskctx.Attribution{TaskID: "x"}) {
		t.Error("Set on nil binding must be a no-op")
	}
	if _, ok := b.Get(); ok {
		t.Error("Get on nil binding must report unset")
	}
	// A context with no binding must not panic.
	if _, ok := taskctx.AttributionFromContext(context.Background()); ok {
		t.Error("bare context must report no attribution")
	}
}

// TestDirectAttributionWinsOverBinding: an agent-claimed task must not be
// shadowed by runtime bookkeeping.
func TestDirectAttributionWinsOverBinding(t *testing.T) {
	ctx, binding := taskctx.ContextWithBinding(context.Background())
	binding.Set(taskctx.Attribution{TaskID: "implicit-task"})
	ctx = taskctx.ContextWithAttribution(ctx, taskctx.Attribution{TaskID: "real-task"})

	got, ok := taskctx.AttributionFromContext(ctx)
	if !ok || got.TaskID != "real-task" {
		t.Fatalf("explicit attribution must win, got %q", got.TaskID)
	}
}

// memoHas reports whether the per-turn memo holds exactly this key. Test-only
// observability, taking the mutex the emitter's own paths take.
func (e *ImplicitEmitter) memoHas(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.minted[key]
	return ok
}

// TestSweepPrefixCannotDriftFromTurnKey guards the seam rather than the
// symptom.
//
// ForgetSession reclaims the per-turn memo by prefix sweep. When that prefix
// was assembled independently of turnKey, changing either format left the sweep
// matching nothing: every other behaviour stayed correct and the per-session
// leak came back unannounced. These assertions fail if the two sides stop
// agreeing, whichever one moved.
func TestSweepPrefixCannotDriftFromTurnKey(t *testing.T) {
	const sess = "sess-1"

	prefix := sessionKeyPrefix(sess)
	if !strings.HasPrefix(prefix, implicitKeyPrefix) {
		t.Errorf("sweep prefix %q must start with implicitKeyPrefix %q, or isImplicitKey stops recognising minted keys and CompleteForTurn silently closes nothing",
			prefix, implicitKeyPrefix)
	}

	for _, turn := range []int{0, 1, 42} {
		key := turnKey(TurnRequest{SessionID: sess, TurnIndex: turn})
		if !strings.HasPrefix(key, prefix) {
			t.Errorf("ForgetSession sweeps %q but turnKey minted %q; the sweep would match nothing and leak the session's memo",
				prefix, key)
		}
		if len(key) <= len(prefix) {
			t.Errorf("turn key %q must extend the session prefix %q, not equal it, or turns within a session collide",
				key, prefix)
		}
		if !isImplicitKey(key) {
			t.Errorf("turnKey minted %q, which isImplicitKey rejects; CompleteForTurn would leave the task IN_PROGRESS forever", key)
		}
	}

	// The separator that stops one session's sweep reaching another's keys. Drop
	// it and forgetting "sess-1" would evict live entries for "sess-10".
	if other := turnKey(TurnRequest{SessionID: sess + "0", TurnIndex: 0}); strings.HasPrefix(other, prefix) {
		t.Errorf("sweep prefix %q also matches %q; retiring one session would drop another's memo", prefix, other)
	}
}

// TestForgetSessionSweepsTheKeyTurnKeyMinted is the behavioural half: the memo
// entry a mint really created, named by turnKey, is the entry the sweep really
// removes. The size-based reclamation tests cannot see which key was dropped.
func TestForgetSessionSweepsTheKeyTurnKeyMinted(t *testing.T) {
	e, _ := newLifecycleEmitter(t)
	const sess = "sess-0"

	mintTurns(t, e, []string{sess}, 2, boardIsSession)

	for _, turn := range []int{0, 1} {
		if key := turnKey(TurnRequest{SessionID: sess, TurnIndex: turn}); !e.memoHas(key) {
			t.Fatalf("mint should have memoized %q; the rig or the key format changed", key)
		}
	}

	e.ForgetSession(sess)

	for _, turn := range []int{0, 1} {
		if key := turnKey(TurnRequest{SessionID: sess, TurnIndex: turn}); e.memoHas(key) {
			t.Errorf("ForgetSession left %q behind: the sweep prefix no longer matches the keys turnKey mints", key)
		}
	}
}
