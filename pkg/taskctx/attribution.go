// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package taskctx

import (
	"context"
	"sync"
)

// Package taskctx carries the identity of the task that in-flight work belongs
// to, on context.Context.
//
// It is a LEAF package by design: it imports nothing but "context". The
// attribution has to be readable from pkg/shuttle (the HITL store), pkg/agent
// (the session store), and pkg/task itself — and pkg/shuttle cannot import
// pkg/task, because pkg/task -> pkg/communication -> pkg/types -> pkg/shuttle
// is a cycle. Putting the accessors below every one of those packages is what
// lets all of them stamp a task_id without any of them depending on each other.
//
// pkg/task re-exports these names, so callers already holding a task import do
// not need a second one.

// attributionKey is the context key for the ambient task attribution.
type attributionKey struct{}

// Attribution identifies the task that in-flight work belongs to.
//
// It exists to solve one problem: the rows that already record what the system
// did — conversation messages with their tool calls and results, human-in-the-loop
// requests — are written several frames below the code that knows which task is
// claimed. Those writers need a task_id to stamp, and threading one through every
// signature in between would touch dozens of call sites and be silently wrong
// wherever a caller forgot to pass it.
//
// Carrying it on context.Context follows two patterns already established in
// this codebase: observability.SpanFromContext, and the agent's
// ContextWithProgressCallback.
//
// Invariant: an Attribution is present only when a task is genuinely claimed for
// the current unit of work. Writers that find no attribution leave task_id NULL
// rather than guessing an owner. A NULL task_id is the normal case — not every
// agent turn operates under a task — so it must never be treated as an error.
type Attribution struct {
	// TaskID is the task to attribute work to. Required; an empty TaskID makes
	// the attribution inert.
	TaskID string

	// BoardID is the task's board, carried to avoid a lookup at each write.
	BoardID string

	// SessionID is the conversation session the work belongs to.
	SessionID string

	// AgentID is the agent performing the work.
	AgentID string

	// ParentAgentID is set when AgentID belongs to an ephemeral agent spawned by
	// another agent, so a timeline can nest a subagent's work under its spawner.
	ParentAgentID string
}

// ContextWithAttribution returns a context carrying the ambient task
// attribution. Call it immediately after claiming a task, and pass the returned
// context down into tool execution, skill activation, and subagent dispatch.
func ContextWithAttribution(ctx context.Context, a Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, a)
}

// AttributionFromContext returns the ambient task attribution. The boolean is
// false when no task is claimed for the current work, which is a normal
// condition rather than an error.
func AttributionFromContext(ctx context.Context) (Attribution, bool) {
	if a, ok := ctx.Value(attributionKey{}).(Attribution); ok && a.TaskID != "" {
		return a, true
	}
	// Fall back to a lazily-filled binding, so a writer holding a context from
	// before the implicit task existed still sees it once it does.
	if a, ok := BindingFromContext(ctx).Get(); ok {
		return a, true
	}
	return Attribution{}, false
}

// TaskIDFromContext is a convenience for writers that only need the ID to stamp
// on a row. Returns "" when no task is claimed, which callers should persist as
// NULL rather than as an empty string.
func TaskIDFromContext(ctx context.Context) string {
	a, ok := AttributionFromContext(ctx)
	if !ok {
		return ""
	}
	return a.TaskID
}

// Creation sources recorded in tasks.created_via. This closes the provenance
// gap the task data model has today: nothing on the task row says how it came
// to exist, so a reader has to infer it from owner_agent_id, claimed_by_session,
// and parent_id.
const (
	// CreatedViaUser: a person created the task directly.
	CreatedViaUser = "user"
	// CreatedViaAgent: an agent created it through the task_board tool.
	CreatedViaAgent = "agent"
	// CreatedViaDecompose: produced by LLM decomposition of a goal.
	CreatedViaDecompose = "decompose"
	// CreatedViaSkillTemplate: emitted by a skill's task template.
	CreatedViaSkillTemplate = "skill_template"
	// CreatedViaWorkflow: created by the task-tracked orchestrator for a stage.
	CreatedViaWorkflow = "workflow"
	// CreatedViaImplicit: minted by the runtime because a turn did real work
	// and its evidence needed an owner. These are a rendering artifact for
	// humans: they are excluded from the agent's own task context and ready
	// front by default, so they cannot grow the prompt turn over turn.
	CreatedViaImplicit = "implicit"
)

// =============================================================================
// Lazy binding
// =============================================================================

// bindingKey is the context key for a deferred attribution.
type bindingKey struct{}

// Binding is a mutable, concurrency-safe slot for an attribution that is not
// known when the context is created.
//
// It exists because implicit task creation must be LAZY. The runtime decides
// whether a turn deserves a task based on whether the turn does real work, and
// that is only known once the first tool call, skill activation, or human
// request happens — by which time the context has already been passed down
// several frames and cannot be replaced.
//
// Placing a pointer in the context at the start of the turn and filling it on
// the first qualifying event means every downstream reader, including ones that
// captured the context before the task existed, sees the attribution as soon as
// it is set. The alternative — creating a task at the start of every turn — puts
// a row on the board for "thanks, that worked".
//
// A Binding with nothing set behaves exactly like no attribution at all.
type Binding struct {
	mu  sync.RWMutex
	val Attribution
	set bool
}

// NewBinding returns an unset binding.
func NewBinding() *Binding { return &Binding{} }

// Set records the attribution. The first Set wins: a turn mints at most one
// implicit task, and a later trigger in the same turn must not retarget work
// already attributed. Returns true if this call was the one that set it.
func (b *Binding) Set(a Attribution) bool {
	if b == nil || a.TaskID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.set {
		return false
	}
	b.val, b.set = a, true
	return true
}

// Get returns the bound attribution, and whether one has been set.
func (b *Binding) Get() (Attribution, bool) {
	if b == nil {
		return Attribution{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.val, b.set
}

// ContextWithBinding attaches a fresh binding for the current turn and returns
// it alongside the derived context. Call once per turn, before any work.
func ContextWithBinding(ctx context.Context) (context.Context, *Binding) {
	b := NewBinding()
	return context.WithValue(ctx, bindingKey{}, b), b
}

// BindingFromContext returns the turn's binding, or nil when none is attached.
func BindingFromContext(ctx context.Context) *Binding {
	b, _ := ctx.Value(bindingKey{}).(*Binding)
	return b
}

// parentTaskKey carries the delegating task across a spawn boundary.
type parentTaskKey struct{}

// ParentTask names the work a spawned agent is running on behalf of.
type ParentTask struct {
	// TaskID is the delegating turn's task.
	TaskID string
	// AgentID is the agent that delegated.
	AgentID string
}

// ContextWithParentTask marks a context as running on behalf of another task,
// and REMOVES any attribution the parent had installed.
//
// Both halves matter. A spawned agent must not inherit the parent's
// attribution: EnsureForTurn declines when it finds one ("a real task owns this
// work"), so an inherited attribution would silently file the child's tool
// calls under the parent's task and the delegated work would have no task of
// its own. Stripping it lets the child mint its own; the marker left behind is
// what lets that mint draw a PARENT_CHILD edge home.
//
// Clearing takes BOTH paths that AttributionFromContext consults, or the parent
// leaks through the one that is missed:
//
//	1. the attribution key, overwritten with an empty value — an empty TaskID
//	   already reads as absent; and
//	2. the turn Binding, replaced with a fresh unset one. This is the subtle
//	   half: AttributionFromContext falls back to the binding precisely so a
//	   context captured before the task existed still sees it, which means the
//	   parent's filled binding would otherwise remain visible to the child no
//	   matter what the attribution key says.
func ContextWithParentTask(ctx context.Context, p ParentTask) context.Context {
	ctx = context.WithValue(ctx, attributionKey{}, Attribution{})
	ctx, _ = ContextWithBinding(ctx)
	if p.TaskID == "" {
		return ctx
	}
	return context.WithValue(ctx, parentTaskKey{}, p)
}

// ParentTaskFromContext returns the delegating task, if this context was
// created for a spawned agent.
func ParentTaskFromContext(ctx context.Context) (ParentTask, bool) {
	if p, ok := ctx.Value(parentTaskKey{}).(ParentTask); ok && p.TaskID != "" {
		return p, true
	}
	return ParentTask{}, false
}
