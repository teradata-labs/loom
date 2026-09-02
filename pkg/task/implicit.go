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
	"strings"
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
	MetricImplicitTaskClosed  = "task.implicit.closed"
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

// TurnMessageAttributor back-fills a task id onto rows a turn has ALREADY
// written.
//
// It exists because minting is lazy. The task appears on the first tool call,
// but the user message that asked for the work was written before that — so it
// lands unattributed, and a timeline built only from write-time stamping begins
// at the agent's first action and never shows the request that caused it, which
// is the single most important line for a human reading the task.
//
// OPTIONAL by design. This is a capability a host may or may not have (it needs
// a message table with a task_id column and a turn boundary to scope by), so it
// is an interface satisfied by assertion rather than a required dependency —
// hosts without it still get correct write-time attribution for everything
// after the mint.
type TurnMessageAttributor interface {
	// AttributeTurnMessages stamps taskID on the turn's unattributed rows and
	// reports how many it claimed. Implementations must touch only rows whose
	// task id is unset, so a row already owned by a real claimed task is never
	// reassigned.
	AttributeTurnMessages(ctx context.Context, sessionID, taskID string, turn int64) (int64, error)
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
	// boardsKnown remembers boards this emitter has already confirmed exist.
	//
	// ensureBoard probes with GetBoard on every mint, and after a session's
	// first turn that probe always hits and always returns the same answer —
	// one of the four round trips a mint costs, spent re-learning a fact that
	// cannot change back. Caching it takes a mint from four round trips to
	// three.
	//
	// Safe because board deletion mid-process is not a case this optimizes for:
	// if a cached board does disappear, CreateTask fails its foreign key, the
	// turn declines quietly as it already does on any create failure, and
	// forgetBoard drops the entry so the next turn re-probes and re-creates.
	// The cache is therefore self-healing rather than merely optimistic.
	boardsKnown map[string]struct{}

	// attributor back-fills pre-mint rows. Nil when the host cannot.
	attributor TurnMessageAttributor
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
		manager:     manager,
		policy:      policy,
		tracer:      tracer,
		logger:      logger,
		minted:      map[string]string{},
		perSession:  map[string]int{},
		boardsKnown: map[string]struct{}{},
	}
}

// SetTurnMessageAttributor installs the optional back-fill hook.
//
// A setter rather than a constructor parameter: the attributor needs per-request
// identity (a user, a tenant) that is not available where the emitter is built,
// so a host wires it once it has one.
func (e *ImplicitEmitter) SetTurnMessageAttributor(a TurnMessageAttributor) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.attributor = a
	e.mu.Unlock()
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
	// SessionEpoch distinguishes INCARNATIONS of a session id — the session's
	// durable creation time, in Unix seconds.
	//
	// It exists because the durable idempotency key must not outlive the
	// conversation it described. Session ids come from outside, and a caller
	// that deletes a session may reuse its id: without an incarnation
	// component the new conversation's turn 0 resolves to the OLD
	// conversation's turn-0 key, and CreateTaskIdempotent hands back a
	// terminal task titled with the previous conversation's opening message.
	// New work then attaches to a task marked DONE before it began.
	//
	// The session's creation time is the incarnation: deletion + recreation
	// yields a fresh CreatedAt and therefore fresh keys, while a process
	// restart restores the SAME CreatedAt and therefore rebinds — which is
	// exactly the split a resume-after-restart needs. Zero (the caller has no
	// session timestamp) is a valid stable epoch, so older callers and tests
	// that never set it keep single-incarnation behaviour.
	SessionEpoch int64
	// Trigger is the event asking for a task.
	Trigger loomv1.ImplicitTaskTrigger
	// UserMessage seeds the title, so a human scanning the board sees what the
	// turn was about rather than "Turn 7".
	UserMessage string

	// ParentTaskID and ParentAgentID identify the work that spawned this turn,
	// set when an ephemeral agent runs on behalf of another.
	//
	// A spawned agent gets its OWN session, so nothing in the session record
	// connects its work back to the agent that asked for it. Without these, a
	// subagent's task lands on the board as an unexplained sibling. With them,
	// the mint would link child to parent with a PARENT_CHILD edge and stamp
	// ParentAgentID onto the attribution, so a reader could walk from the
	// delegating turn into the delegated work.
	//
	// NEITHER IS SET TODAY. No spawn path populates them — maybeRecordImplicitTask
	// builds a TurnRequest without them and only ever passes TOOL_CALL, never
	// SUBAGENT_SPAWN — so linkToParent returns early on every real mint and no
	// PARENT_CHILD edge is drawn for a subagent. This is infrastructure awaiting
	// a caller, not current behaviour; see ContextWithParentTask in pkg/taskctx,
	// which carries the same note.
	//
	// Both are optional; a top-level turn leaves them empty.
	ParentTaskID  string
	ParentAgentID string
}

// sessionKeyPrefix is the shape every one of a session's turn keys begins with,
// and the ONLY place that shape is written down.
//
// It exists because ForgetSession reclaims the per-turn memo by prefix sweep
// while turnKey mints the keys being swept. Those were built independently, so
// a change to either format left the sweep matching nothing — reinstating the
// per-session leak with the emitter otherwise behaving correctly, and therefore
// silently. Both sides now derive from here.
//
// The trailing separator is load-bearing: without it the prefix for session
// "s1" would also match "s10"'s keys, and retiring one session would drop
// another's live memo.
func sessionKeyPrefix(sessionID string) string {
	return implicitKeyPrefix + sessionID + "|"
}

// turnKey identifies a turn for memoization and idempotency.
//
// The epoch sits AFTER the session prefix on purpose: ForgetSession sweeps by
// sessionKeyPrefix, and a session's retirement must reclaim every incarnation's
// memos, not only the current one's.
func turnKey(r TurnRequest) string {
	return fmt.Sprintf("%sepoch:%d|turn:%d", sessionKeyPrefix(r.SessionID), r.SessionEpoch, r.TurnIndex)
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
	// MaxPerSession is an IN-PROCESS noise guard, decided deliberately rather
	// than left as an accident: it bounds how much one conversation can grow a
	// board between restarts, and it is NOT a durable quota. A process restart
	// grants a fresh budget; deleting a session resets its counter
	// (ForgetSession — and the reclaim tests assert that on purpose); and a
	// recreated session id is a new incarnation (SessionEpoch), so a fresh
	// budget there is correct, not a leak. A durable quota would need a
	// per-incarnation count against the store and a decision about what
	// deletion means for it — a different feature, not a stricter version of
	// this one.
	if e.policy.MaxPerSession > 0 && e.perSession[r.SessionID] >= e.policy.MaxPerSession {
		e.mu.Unlock()
		e.tracer.RecordMetric(MetricImplicitTaskSkipped, 1, map[string]string{"reason": "session_cap"})
		e.logger.Debug("implicit task skipped: session cap reached",
			zap.String("session_id", r.SessionID),
			zap.Int("cap", e.policy.MaxPerSession))
		return ctx, nil, nil
	}
	e.mu.Unlock()

	// The board must exist before the task references it: tasks.board_id carries
	// a foreign key, so creating a task against a missing board fails with an
	// opaque constraint violation rather than anything actionable. Both the
	// task_board tool (resolveBoardForWrite) and the skills emitter (ensureBoard)
	// solve this the same way; skipping it here was a real bug, because the
	// board id defaults to the session id and no board row exists under that id.
	if err := e.ensureBoard(ctx, r.BoardID); err != nil {
		e.tracer.RecordMetric(MetricImplicitTaskSkipped, 1, map[string]string{"reason": "board_unavailable"})
		e.logger.Warn("implicit task skipped: board could not be ensured",
			zap.String("board_id", r.BoardID), zap.Error(err))
		return ctx, nil, nil
	}

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
		// The board cache may have vouched for a board that is gone; drop it so
		// the next turn re-probes instead of failing identically forever.
		e.forgetBoard(r.BoardID)
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
		e.linkToParent(ctx, created.ID, r)
		e.backfillTurnMessages(ctx, created.ID, r)
	}
	return e.bind(ctx, r, created.ID), created, nil
}

// linkToParent records the PARENT_CHILD edge from a delegated turn's task back
// to the task that delegated it.
//
// Reached but inert today: no caller populates TurnRequest.ParentTaskID, so the
// empty-parent guard below returns before any edge is drawn. See that field's
// comment. Everything after this paragraph describes what happens once a spawn
// path supplies one.
//
// Only on first mint: the edge is a property of the task's creation, and an
// idempotent re-entry returns the existing task whose edge already exists.
//
// Failure is logged, not returned. The child task and its timeline are already
// correct on their own; a missing edge costs a reader one hop of navigation,
// which does not justify failing the turn that was trying to do real work.
func (e *ImplicitEmitter) linkToParent(ctx context.Context, childID string, r TurnRequest) {
	if r.ParentTaskID == "" || r.ParentTaskID == childID {
		return
	}
	err := e.manager.AddDependency(ctx, &TaskDependency{
		FromTaskID: childID,
		ToTaskID:   r.ParentTaskID,
		Type:       loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_PARENT_CHILD,
		CreatedBy:  r.AgentID,
		Metadata: map[string]string{
			"linked_by":       "implicit_emitter",
			"parent_agent_id": r.ParentAgentID,
		},
	})
	if err != nil {
		e.logger.Warn("implicit task parent link failed; child task stands alone",
			zap.String("child_task_id", childID),
			zap.String("parent_task_id", r.ParentTaskID),
			zap.Error(err))
	}
}

// backfillTurnMessages claims the turn's already-written rows for a new task.
//
// Only on first mint: an idempotent re-entry returns a task whose rows were
// claimed when it was created.
//
// Failure is logged, not returned. A turn must never fail over bookkeeping, and
// a partially attributed timeline is still useful — it just starts at the
// agent's first action instead of at the request.
func (e *ImplicitEmitter) backfillTurnMessages(ctx context.Context, taskID string, r TurnRequest) {
	e.mu.Lock()
	a := e.attributor
	e.mu.Unlock()
	if a == nil || r.SessionID == "" {
		return
	}

	claimed, err := a.AttributeTurnMessages(ctx, r.SessionID, taskID, int64(r.TurnIndex))
	if err != nil {
		e.logger.Warn("implicit task message back-fill failed; timeline will start at the first tool call",
			zap.String("task_id", taskID),
			zap.String("session_id", r.SessionID),
			zap.Int("turn", r.TurnIndex),
			zap.Error(err))
		return
	}
	e.logger.Debug("implicit task back-filled turn messages",
		zap.String("task_id", taskID),
		zap.Int64("rows", claimed))
}

// bind attaches the attribution to the context and fills the turn's binding.
func (e *ImplicitEmitter) bind(ctx context.Context, r TurnRequest, taskID string) context.Context {
	a := taskctx.Attribution{
		TaskID:        taskID,
		BoardID:       r.BoardID,
		SessionID:     r.SessionID,
		AgentID:       r.AgentID,
		ParentAgentID: r.ParentAgentID,
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
	// Epoch-agnostic on purpose: the caller knows which session and turn ended,
	// not which incarnation minted the memo, and a turn index only ever belongs
	// to one incarnation of a live session — any same-turn entry from a prior
	// incarnation is garbage this delete is welcome to take with it. Matching by
	// prefix and suffix instead of reconstructing the exact key is what lets the
	// epoch stay out of every EndTurn caller's signature.
	prefix := sessionKeyPrefix(sessionID)
	suffix := fmt.Sprintf("|turn:%d", turnIndex)
	e.mu.Lock()
	for k := range e.minted {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, suffix) {
			delete(e.minted, k)
		}
	}
	e.mu.Unlock()
}

// ForgetSession drops all state for a session: its cap counter, its remaining
// per-turn memos, and its board.
//
// Called from every session-retirement path. All three maps are bounded by live
// sessions ONLY because retirement frees them; without this the emitter grows
// for the life of the process, which on a long-running server is unbounded.
func (e *ImplicitEmitter) ForgetSession(sessionID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perSession, sessionID)
	prefix := sessionKeyPrefix(sessionID)
	for k := range e.minted {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(e.minted, k)
		}
	}
	// Deleting a SESSION id from a BOARD-keyed map is deliberate, not a
	// category error. maybeRecordImplicitTask defaults the board to the
	// session id when no DefaultBoardId is configured — the default
	// configuration — so in that shape the board id IS the session id and
	// boardsKnown accumulates one entry per session, the same leak as the two
	// maps above.
	//
	// A configured shared board cannot be harmed: its id is an operator-chosen
	// name, never equal to a session id, so this delete misses it. And even if
	// a shared board's entry were somehow dropped, the cost is one re-probe in
	// ensureBoard on the next mint — the cache is an optimisation, not a
	// correctness requirement.
	delete(e.boardsKnown, sessionID)
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

// ensureBoard guarantees the referenced board exists before a task points at it.
//
// tasks.board_id is a foreign key, so a task created against a missing board
// fails with a constraint violation. The implicit emitter defaults the board to
// the session id, and nothing creates a board under that id, so without this
// every implicit task would fail — which is precisely what happened the first
// time this ran against a real backend.
//
// Probe-then-create with a second probe on failure: two turns in the same
// session can reach here concurrently, and one of them will lose the create.
// The re-probe decides which way that race went instead of failing the loser.
func (e *ImplicitEmitter) ensureBoard(ctx context.Context, boardID string) error {
	if boardID == "" {
		// A board-less task is legal; the FK is only enforced on non-empty ids.
		return nil
	}
	// Already confirmed this process: skip the probe entirely. This is the
	// steady state for every turn of a session after its first.
	e.mu.Lock()
	_, known := e.boardsKnown[boardID]
	e.mu.Unlock()
	if known {
		return nil
	}
	if _, err := e.manager.GetBoard(ctx, boardID); err == nil {
		e.rememberBoard(boardID)
		return nil
	}
	if _, err := e.manager.CreateBoard(ctx, &TaskBoard{
		ID:   boardID,
		Name: "Session work",
	}); err != nil {
		if _, gerr := e.manager.GetBoard(ctx, boardID); gerr == nil {
			e.rememberBoard(boardID)
			return nil
		}
		return err
	}
	e.rememberBoard(boardID)
	e.logger.Info("implicit emitter: auto-created board",
		zap.String("board_id", boardID))
	return nil
}

// rememberBoard records that a board is known to exist.
func (e *ImplicitEmitter) rememberBoard(boardID string) {
	if boardID == "" {
		return
	}
	e.mu.Lock()
	e.boardsKnown[boardID] = struct{}{}
	e.mu.Unlock()
}

// forgetBoard drops a cached board so the next mint re-probes it.
//
// Called when a create fails, which is the only signal available that a board
// the cache vouched for may be gone. Without this the cache would keep asserting
// a board that no longer exists and every later turn in the session would fail
// the same way.
func (e *ImplicitEmitter) forgetBoard(boardID string) {
	if boardID == "" {
		return
	}
	e.mu.Lock()
	delete(e.boardsKnown, boardID)
	e.mu.Unlock()
}

// CompleteForTurn closes the task recorded for a turn, if there is one.
//
// Without this an implicit task is created IN_PROGRESS and stays there forever:
// nothing else has a reason to close it, because no agent claimed it and no
// human is working it. The board fills with permanently in-flight rows and the
// session panel shows 0/1 done for work that finished — which is exactly what
// happened the first time this ran end to end.
//
// Called once at the end of a turn. Idempotent: a turn with no recorded task, an
// already-closed task, or a task claimed by something else is a no-op.
//
// Best-effort by the same reasoning as creation: a turn must not fail because
// its bookkeeping row could not be closed. A close failure leaves the task
// IN_PROGRESS and is counted, not raised.
func (e *ImplicitEmitter) CompleteForTurn(ctx context.Context, taskID, closeReason string) {
	if e == nil || e.manager == nil || taskID == "" {
		return
	}

	existing, err := e.manager.GetTask(ctx, taskID)
	if err != nil || existing == nil {
		return
	}
	// Only close what we recorded, and only if it is still open. A task the
	// agent genuinely claimed and closed itself must not be touched.
	//
	// The discriminator is the IDEMPOTENCY KEY, not created_via. created_via is
	// the natural choice and it is what this originally used — but it is not
	// read back by every store: avmo-tera-cloud persists the column and omits it
	// from its SELECT list, so GetTask returns it empty and the guard silently
	// rejected every task it was meant to admit. Nothing closed, nothing logged.
	//
	// skill_idempotency_key is selected by every store (both loom stores and
	// cloud's), and implicit keys carry a fixed prefix, so it identifies our own
	// rows without depending on a column a store may not project.
	if !isImplicitKey(existing.SkillIdempotencyKey) {
		e.logger.Debug("implicit close skipped: not a runtime-recorded task",
			zap.String("task_id", taskID),
			zap.String("idempotency_key", existing.SkillIdempotencyKey))
		return
	}
	if IsTerminal(existing.Status) {
		return
	}

	if closeReason == "" {
		closeReason = "Turn completed."
	}
	if _, err := e.manager.CloseTask(ctx, taskID, closeReason); err != nil {
		e.tracer.RecordMetric(MetricImplicitTaskSkipped, 1, map[string]string{"reason": "close_failed"})
		e.logger.Warn("implicit task close failed; task stays in progress",
			zap.String("task_id", taskID), zap.Error(err))
		return
	}
	e.tracer.RecordMetric(MetricImplicitTaskClosed, 1, nil)
}

// implicitKeyPrefix is the fixed prefix on every implicit task's idempotency
// key. sessionKeyPrefix and turnKey build the rest of the key on top of it, and
// isImplicitKey below recognises it, so all three move together.
const implicitKeyPrefix = "implicit:sess:"

// isImplicitKey reports whether an idempotency key was minted by this emitter.
//
// Used instead of created_via because it is projected by every store
// implementation, including downstream ones this package cannot see.
func isImplicitKey(key string) bool {
	return len(key) >= len(implicitKeyPrefix) && key[:len(implicitKeyPrefix)] == implicitKeyPrefix
}
