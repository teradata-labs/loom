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

package task

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// Implicit task emission makes the task the default unit of record.
//
// Without it, a task exists only when an agent chooses to call the task_board
// tool and an operator has switched the board on — so in practice almost no
// work has a task, and a task-keyed timeline renders nothing. The runtime mints
// one instead, on the first event of a turn that constitutes real work.
//
// Three properties keep this from being expensive:
//
//  1. LAZY. A turn that only produces text mints nothing. The task appears on
//     the first qualifying trigger, so the board tracks work rather than
//     conversation.
//  2. AT MOST ONE PER TURN. Enforced twice: an in-process memo for the hot
//     path, and an idempotency key so concurrent triggers and process restarts
//     converge on the same row.
//  3. INVISIBLE TO THE AGENT. Implicit tasks carry CreatedViaImplicit and are
//     excluded from the task context block and the ready front, so they cannot
//     accumulate in the prompt turn over turn.

// Defaults for implicit emission. Exported so operators reading configuration
// can see what "unset" resolves to.
const (
	// DefaultMaxImplicitPerSession caps implicit tasks per session. A long
	// conversation should not grow a board without bound; past the cap the
	// runtime stops minting and counts the skips.
	DefaultMaxImplicitPerSession = 100

	// MaxImplicitTitleLen bounds the generated title.
	MaxImplicitTitleLen = 110
)

// Metrics exported by the emitter.
const (
	MetricImplicitTaskCreated = "task.implicit.created"
	MetricImplicitTaskSkipped = "task.implicit.skipped"
)

// DefaultImplicitTriggers is the effective trigger set when configuration
// names none.
//
// TOOL_CALL, HUMAN_REQUEST, and SUBAGENT_SPAWN are in. Each is an unambiguous
// sign the turn did something a human may need to audit, and each is bounded
// per turn by the laziness rule.
//
// SKILL_ACTIVATION is OUT of the default set: the skills task emitter already
// creates tasks on activation, so including it would produce two tasks for one
// cause. It remains available for agents that disable skill emission.
//
// WORKFLOW_STEP is OUT for the same reason — the task-tracked orchestrator
// already creates a task per stage.
func DefaultImplicitTriggers() []loomv1.ImplicitTaskTrigger {
	return []loomv1.ImplicitTaskTrigger{
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST,
		loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_SUBAGENT_SPAWN,
	}
}

// ImplicitPolicy is the resolved configuration, with defaults applied.
type ImplicitPolicy struct {
	Enabled       bool
	Triggers      map[loomv1.ImplicitTaskTrigger]bool
	MaxPerSession int
	AgentVisible  bool
}

// ResolveImplicitPolicy turns the proto configuration into a decided policy.
//
// A nil config resolves to enabled with defaults: implicit emission is opt-out,
// so an agent that says nothing about tasks still gets working timelines.
func ResolveImplicitPolicy(cfg *loomv1.ImplicitTaskConfig) ImplicitPolicy {
	p := ImplicitPolicy{
		Enabled:       true,
		MaxPerSession: DefaultMaxImplicitPerSession,
		Triggers:      map[loomv1.ImplicitTaskTrigger]bool{},
	}

	base := DefaultImplicitTriggers()
	if cfg != nil {
		// UNSPECIFIED means enabled — see ImplicitTaskMode in the proto for why
		// the default lives in the enum rather than in an inverted bool.
		if cfg.Mode == loomv1.ImplicitTaskMode_IMPLICIT_TASK_MODE_DISABLED {
			p.Enabled = false
		}
		if len(cfg.Triggers) > 0 {
			base = cfg.Triggers
		}
		if cfg.MaxPerSession > 0 {
			p.MaxPerSession = int(cfg.MaxPerSession)
		}
		p.AgentVisible = cfg.AgentVisible
	}

	for _, tr := range base {
		if tr != loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_UNSPECIFIED {
			p.Triggers[tr] = true
		}
	}
	// excluded_triggers is applied last so "everything except X" does not
	// require enumerating the rest — and does not silently opt out of a trigger
	// added in a later release.
	if cfg != nil {
		for _, tr := range cfg.ExcludedTriggers {
			delete(p.Triggers, tr)
		}
	}

	if len(p.Triggers) == 0 {
		p.Enabled = false
	}
	return p
}

// Allows reports whether a trigger may mint a task under this policy.
func (p ImplicitPolicy) Allows(tr loomv1.ImplicitTaskTrigger) bool {
	return p.Enabled && p.Triggers[tr]
}

// ExcludedCreatedVia returns the created_via values to hide from the agent's
// own task queries. Empty when implicit tasks are configured as agent-visible.
//
// Callers pass this into ListTasksOpts and ReadyFrontOpts. It is the mechanism
// that stops per-turn tasks from accumulating in the system prompt.
func (p ImplicitPolicy) ExcludedCreatedVia() []string {
	if p.AgentVisible {
		return nil
	}
	return []string{taskctx.CreatedViaImplicit}
}

// ImplicitEmitter mints at most one task per turn.
type ImplicitEmitter struct {
	manager *Manager
	policy  ImplicitPolicy
	tracer  observability.Tracer
	logger  *zap.Logger

	mu sync.Mutex
	// minted maps a turn key to the task minted for it, so the second and
	// subsequent triggers in a turn cost a map lookup rather than a database
	// round trip. A turn with forty tool calls must not do forty lookups.
	minted map[string]string
	// perSession counts minted tasks against MaxPerSession.
	perSession map[string]int
}

// NewImplicitEmitter builds an emitter. A nil manager yields an emitter that
// never mints, so callers can construct it unconditionally.
func NewImplicitEmitter(manager *Manager, policy ImplicitPolicy, tracer observability.Tracer, logger *zap.Logger) *ImplicitEmitter {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ImplicitEmitter{
		manager:    manager,
		policy:     policy,
		tracer:     tracer,
		logger:     logger,
		minted:     map[string]string{},
		perSession: map[string]int{},
	}
}

// Policy returns the resolved policy, for callers that need ExcludedCreatedVia.
func (e *ImplicitEmitter) Policy() ImplicitPolicy {
	if e == nil {
		return ImplicitPolicy{}
	}
	return e.policy
}

// TurnRequest describes the turn a trigger fired in.
type TurnRequest struct {
	SessionID string
	AgentID   string
	BoardID   string
	// TurnIndex distinguishes turns within a session. It is part of the
	// idempotency key, so it must be stable for the whole turn.
	TurnIndex int
	// Trigger is the event asking for a task.
	Trigger loomv1.ImplicitTaskTrigger
	// UserMessage seeds the title, so a human scanning the board sees what the
	// turn was about rather than "Turn 7".
	UserMessage string
}

// turnKey identifies a turn for memoization and idempotency.
func turnKey(r TurnRequest) string {
	return fmt.Sprintf("implicit:sess:%s|turn:%d", r.SessionID, r.TurnIndex)
}

// EnsureForTurn returns the implicit task for this turn, minting it on the
// first qualifying trigger.
//
// Returns (nil, nil) — not an error — when the policy declines: emission
// disabled, trigger not allowed, or the session cap reached. Declining is the
// common case and callers treat it as "carry on without a task".
//
// On success the returned context carries the attribution, and the turn's
// Binding (if one is attached) is filled so contexts captured earlier see it
// too.
func (e *ImplicitEmitter) EnsureForTurn(ctx context.Context, r TurnRequest) (context.Context, *Task, error) {
	if e == nil || e.manager == nil || r.SessionID == "" {
		return ctx, nil, nil
	}
	if !e.policy.Allows(r.Trigger) {
		return ctx, nil, nil
	}

	// An attribution already on the context means a real task owns this work —
	// an agent-claimed task, or an earlier trigger this turn. Never shadow it.
	if a, ok := taskctx.AttributionFromContext(ctx); ok {
		return ctx, nil, e.noteExisting(a)
	}

	key := turnKey(r)

	e.mu.Lock()
	if id, ok := e.minted[key]; ok {
		e.mu.Unlock()
		return e.bind(ctx, r, id), nil, nil
	}
	if e.policy.MaxPerSession > 0 && e.perSession[r.SessionID] >= e.policy.MaxPerSession {
		e.mu.Unlock()
		e.tracer.RecordMetric(MetricImplicitTaskSkipped, 1, map[string]string{"reason": "session_cap"})
		e.logger.Debug("implicit task skipped: session cap reached",
			zap.String("session_id", r.SessionID),
			zap.Int("cap", e.policy.MaxPerSession))
		return ctx, nil, nil
	}
	e.mu.Unlock()

	// CreateTaskIdempotent collapses concurrent callers and process restarts
	// onto one row, reusing the partial unique index that the skills emitter
	// already uses. Migration 000008 names the column generically for exactly
	// this reuse.
	created, isNew, err := e.manager.CreateTaskIdempotent(ctx, &Task{
		Title:               implicitTitle(r),
		Description:         implicitDescription(r),
		Status:              loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		Priority:            loomv1.TaskPriority_TASK_PRIORITY_MEDIUM,
		Category:            loomv1.TaskCategory_TASK_CATEGORY_OTHER,
		OwnerAgentID:        r.AgentID,
		BoardID:             r.BoardID,
		ClaimedBySession:    r.SessionID,
		CreatedVia:          taskctx.CreatedViaImplicit,
		SkillIdempotencyKey: key,
		Metadata: map[string]string{
			"implicit_trigger": r.Trigger.String(),
		},
	})
	if err != nil {
		// Emission is best-effort: a turn must not fail because its bookkeeping
		// row could not be written.
		e.tracer.RecordMetric(MetricImplicitTaskSkipped, 1, map[string]string{"reason": "create_failed"})
		e.logger.Warn("implicit task creation failed; continuing without one",
			zap.String("session_id", r.SessionID), zap.Error(err))
		return ctx, nil, nil
	}

	e.mu.Lock()
	e.minted[key] = created.ID
	if isNew {
		e.perSession[r.SessionID]++
	}
	e.mu.Unlock()

	if isNew {
		e.tracer.RecordMetric(MetricImplicitTaskCreated, 1, map[string]string{
			"trigger": r.Trigger.String(),
		})
	}
	return e.bind(ctx, r, created.ID), created, nil
}

// bind attaches the attribution to the context and fills the turn's binding.
func (e *ImplicitEmitter) bind(ctx context.Context, r TurnRequest, taskID string) context.Context {
	a := taskctx.Attribution{
		TaskID:    taskID,
		BoardID:   r.BoardID,
		SessionID: r.SessionID,
		AgentID:   r.AgentID,
	}
	// Fill the turn binding first: writers that captured the context before the
	// task existed read through the binding, not through this new context.
	taskctx.BindingFromContext(ctx).Set(a)
	return taskctx.ContextWithAttribution(ctx, a)
}

// noteExisting is a no-op hook kept for symmetry and future accounting.
func (e *ImplicitEmitter) noteExisting(taskctx.Attribution) error { return nil }

// EndTurn releases the per-turn memo. Called at the end of a turn so the map
// does not grow with conversation length.
func (e *ImplicitEmitter) EndTurn(sessionID string, turnIndex int) {
	if e == nil {
		return
	}
	e.mu.Lock()
	delete(e.minted, turnKey(TurnRequest{SessionID: sessionID, TurnIndex: turnIndex}))
	e.mu.Unlock()
}

// ForgetSession drops all state for a session, including its cap counter.
func (e *ImplicitEmitter) ForgetSession(sessionID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perSession, sessionID)
	prefix := fmt.Sprintf("implicit:sess:%s|", sessionID)
	for k := range e.minted {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(e.minted, k)
		}
	}
}

// implicitTitle names the task after what the turn was about, so a board reads
// as work rather than as a numbered log.
func implicitTitle(r TurnRequest) string {
	msg := firstLineOf(r.UserMessage)
	if msg == "" {
		return fmt.Sprintf("Turn %d", r.TurnIndex+1)
	}
	if len(msg) > MaxImplicitTitleLen {
		cut := MaxImplicitTitleLen
		for cut > 0 && msg[cut]&0xC0 == 0x80 {
			cut--
		}
		return msg[:cut] + "…"
	}
	return msg
}

func implicitDescription(r TurnRequest) string {
	return fmt.Sprintf("Recorded automatically when this turn first %s.",
		triggerPhrase(r.Trigger))
}

func triggerPhrase(tr loomv1.ImplicitTaskTrigger) string {
	switch tr {
	case loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL:
		return "called a tool"
	case loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_SKILL_ACTIVATION:
		return "activated a skill"
	case loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST:
		return "asked a human"
	case loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_SUBAGENT_SPAWN:
		return "spawned a subagent"
	case loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_WORKFLOW_STEP:
		return "entered a workflow stage"
	default:
		return "did work"
	}
}

// firstLineOf returns the first non-empty line of s, trimmed.
func firstLineOf(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			s = s[:i]
			break
		}
	}
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
