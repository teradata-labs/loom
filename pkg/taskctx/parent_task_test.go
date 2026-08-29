// Copyright 2026 Teradata

package taskctx

import (
	"context"
	"testing"
)

// TestContextWithParentTask_ClearsBothAttributionPaths pins the isolation a
// spawn boundary depends on.
//
// AttributionFromContext consults two sources: the attribution value and the
// turn Binding. A spawn that cleared only the first would leave the parent
// visible through the second, and the child's tool calls would be filed under
// the parent's task instead of getting one of their own.
func TestContextWithParentTask_ClearsBothAttributionPaths(t *testing.T) {
	parent := Attribution{TaskID: "task-parent", SessionID: "sess-parent", AgentID: "agent-parent"}

	t.Run("clears an attribution value", func(t *testing.T) {
		ctx := ContextWithAttribution(context.Background(), parent)
		if _, ok := AttributionFromContext(ctx); !ok {
			t.Fatal("precondition: parent attribution should be visible")
		}

		child := ContextWithParentTask(ctx, ParentTask{TaskID: "task-parent", AgentID: "agent-parent"})
		if a, ok := AttributionFromContext(child); ok {
			t.Fatalf("child inherited parent attribution %q; it must mint its own", a.TaskID)
		}
	})

	t.Run("clears a filled binding", func(t *testing.T) {
		// The binding path: a parent whose task was minted mid-turn.
		ctx, binding := ContextWithBinding(context.Background())
		binding.Set(parent)
		if _, ok := AttributionFromContext(ctx); !ok {
			t.Fatal("precondition: a filled binding should be visible")
		}

		child := ContextWithParentTask(ctx, ParentTask{TaskID: "task-parent", AgentID: "agent-parent"})
		if a, ok := AttributionFromContext(child); ok {
			t.Fatalf("child read parent through the binding (%q); the binding must be replaced", a.TaskID)
		}
	})

	t.Run("leaves the parent marker readable", func(t *testing.T) {
		ctx := ContextWithAttribution(context.Background(), parent)
		child := ContextWithParentTask(ctx, ParentTask{TaskID: "task-parent", AgentID: "agent-parent"})

		p, ok := ParentTaskFromContext(child)
		if !ok {
			t.Fatal("parent marker missing; the child could not draw a PARENT_CHILD edge")
		}
		if p.TaskID != "task-parent" || p.AgentID != "agent-parent" {
			t.Fatalf("unexpected parent marker: %+v", p)
		}
	})

	t.Run("the parent context is not mutated", func(t *testing.T) {
		ctx := ContextWithAttribution(context.Background(), parent)
		_ = ContextWithParentTask(ctx, ParentTask{TaskID: "task-parent"})

		if a, ok := AttributionFromContext(ctx); !ok || a.TaskID != "task-parent" {
			t.Fatal("deriving a child context must not disturb the parent's own attribution")
		}
	})

	t.Run("an empty parent still isolates", func(t *testing.T) {
		// Defensive: a spawn with no task of its own must not hand the child
		// the parent's attribution just because there is no marker to set.
		ctx := ContextWithAttribution(context.Background(), parent)
		child := ContextWithParentTask(ctx, ParentTask{})

		if _, ok := AttributionFromContext(child); ok {
			t.Fatal("an empty ParentTask must still clear inherited attribution")
		}
		if _, ok := ParentTaskFromContext(child); ok {
			t.Fatal("no marker should be set for an empty ParentTask")
		}
	})
}
