// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// taskTurnContext is the per-turn task state carried on the concrete
// agentContext. Declared as a local interface so the runtime can read it without
// widening the public types.Context contract.
type taskTurnContext interface {
	TaskBinding() *taskctx.Binding
	TurnIndex() int64
	UserMessage() string
}

// maybeRecordImplicitTask records a task for the current turn, deterministically.
//
// This is the mechanism that replaces "the agent decides when a task is needed".
// The model is never consulted and never has to call task_board: the RUNTIME
// records a task the first time a turn does something worth recording, and the
// rule is fixed in configuration rather than chosen per-turn by an LLM.
//
// Determinism matters for two reasons beyond predictability. A board that fills
// only when a model remembers to ask is a board users cannot rely on — which is
// exactly the reported experience. And a model deciding produces a different
// board for identical work, so nothing about the board is reproducible.
//
// Laziness is what keeps the board meaningful: it fires on the first qualifying
// trigger, so "thanks, that worked" leaves no row while a turn that runs a tool
// always has somewhere to hang its evidence.
//
// Called at each trigger point; every call after the first in a turn is a cheap
// no-op (the emitter memoizes per turn, and an already-bound context short
// circuits before any store access).
func (a *Agent) maybeRecordImplicitTask(ctx Context, trigger loomv1.ImplicitTaskTrigger) {
	if a.implicitTasks == nil {
		return
	}
	// The per-turn task fields live on the concrete agentContext, not on the
	// shared types.Context interface. Asserting for them rather than widening
	// that interface keeps downstream implementers compiling — pkg/types.Context
	// is public API and avmo-tera-cloud is not the only consumer.
	tc, ok := ctx.(taskTurnContext)
	if !ok {
		return
	}
	binding := tc.TaskBinding()
	if binding == nil {
		return
	}
	// Already attributed — either a real claimed task or an earlier trigger this
	// turn. Never shadow it.
	if _, ok := binding.Get(); ok {
		return
	}

	sess := ctx.Session()
	if sess == nil {
		return
	}

	boardID := ""
	if a.taskBoardConfig != nil {
		boardID = a.taskBoardConfig.DefaultBoardId
	}
	if boardID == "" {
		// A board-less task is legal but never appears on a board, so fall back
		// to the session — the same default the cloud executor uses.
		boardID = sess.ID
	}

	// SessionEpoch carries the session incarnation into the idempotency key,
	// so a recreated session id cannot rebind new work to a prior
	// conversation's terminal task. CreatedAt is durable and restored, which
	// is what lets a resume after a process restart still find its own turn.
	epoch := int64(0)
	if !sess.CreatedAt.IsZero() {
		epoch = sess.CreatedAt.Unix()
	}
	_, created, err := a.implicitTasks.EnsureForTurn(ctx, task.TurnRequest{
		SessionID:    sess.ID,
		AgentID:      a.id,
		BoardID:      boardID,
		TurnIndex:    int(tc.TurnIndex()),
		SessionEpoch: epoch,
		Trigger:      trigger,
		UserMessage:  tc.UserMessage(),
	})
	if err != nil {
		// Never fatal: a turn must not fail because its bookkeeping row could
		// not be written.
		zap.L().Warn("implicit task recording failed",
			zap.String("session_id", sess.ID), zap.Error(err))
		return
	}
	if created == nil {
		return
	}

	// Back-fill this turn's already-written messages. The task is recorded
	// lazily, so the user message that asked for the work was persisted before
	// it existed and carries no task_id. Without this the timeline starts at the
	// agent's first action and never shows the request that caused it.
	// The back-fill is an OPTIONAL capability on the session store: loom's
	// SQLite store implements it, other stores need not. Type-asserting rather
	// than widening the SessionStorage interface keeps downstream implementers
	// (avmo-tera-cloud has its own) compiling — the same reason CountByStatus is
	// an optional capability rather than an interface method.
	//
	// Two shapes exist for this one concept, and this is the one loom wires. The
	// other is task.TurnMessageAttributor.AttributeTurnMessages in
	// pkg/task/implicit.go, keyed by turn index rather than by `since`, which the
	// emitter calls itself once a host installs it with SetTurnMessageAttributor.
	// Nothing in this repo calls that setter — only avmo-tera-cloud does — so the
	// emitter's path is dormant here and the assertion below is the live
	// back-fill. Neither is dead: dropping the interface breaks the cloud host,
	// dropping this breaks loom.
	//
	// Installing both on one host would run the back-fill twice, and that is
	// harmless for one specific reason rather than by luck: both UPDATE only rows
	// whose task_id IS NULL, so the first run leaves the second nothing to claim.
	// An implementation that ever widens that predicate loses the property.
	if bf, ok := a.memory.Store().(interface {
		AttributeTurnMessages(ctx context.Context, sessionID, taskID string, turn int64) (int64, error)
	}); ok {
		if _, err := bf.AttributeTurnMessages(ctx, sess.ID, created.ID, tc.TurnIndex()); err != nil {
			zap.L().Debug("implicit task back-fill failed",
				zap.String("task_id", created.ID), zap.Error(err))
		}
	}

	zap.L().Debug("implicit task recorded",
		zap.String("task_id", created.ID),
		zap.String("trigger", trigger.String()),
		zap.Int64("turn", tc.TurnIndex()))
}

// completeImplicitTask ends the turn for the implicit emitter: it releases the
// turn's per-turn memo and closes the turn's recorded task, if there is one.
//
// The task is opened IN_PROGRESS at the first tool call and nothing else would
// ever close it — no agent claimed it and no human is working it — so without
// this every recorded task sits in progress forever and the session panel reads
// 0/1 done for finished work.
//
// This is also the emitter's per-turn teardown hook. It is deferred once in
// chat() and covers every return point, so the memo release belongs here rather
// than in a second mechanism that would have to re-establish the same coverage.
func (a *Agent) completeImplicitTask(ctx context.Context, binding *taskctx.Binding, sessionID string, turnIndex int, closeReason string) {
	// Release the turn's memo FIRST, and unconditionally.
	//
	// Before the early returns below, not after: those guards are about whether
	// there is a TASK TO CLOSE, which is a different question from whether the
	// turn left an entry in the emitter's per-turn map. Today they agree — the
	// memo and the binding are filled together — but hanging the memory
	// reclamation off the close guard would mean any future change to the close
	// condition silently reintroduces the leak.
	//
	// Unconditional is safe and cheap: EndTurn is nil-receiver-safe (so the
	// a.implicitTasks == nil guard below is not needed for it) and is a single
	// map delete under the emitter's mutex. A turn that minted nothing has no
	// entry and the delete is a no-op.
	a.implicitTasks.EndTurn(sessionID, turnIndex)

	if a.implicitTasks == nil || binding == nil {
		return
	}
	attr, ok := binding.Get()
	if !ok {
		// No task was recorded for this turn — the common case for a turn that
		// only talked.
		return
	}
	// The close must survive the request context. This runs deferred, so when
	// the turn ended BECAUSE the caller canceled (or its deadline lapsed), ctx
	// is already dead here — CompleteForTurn's own GetTask fails on it and
	// returns silently, and the one turn shape whose close reason says
	// "canceled" is exactly the one whose task stayed IN_PROGRESS forever.
	// WithoutCancel keeps the context's values (user identity above all — the
	// store's owner predicates read it) while shedding the cancellation; the
	// fresh deadline keeps the cleanup bounded instead of unbounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	a.implicitTasks.CompleteForTurn(ctx, attr.TaskID, closeReason)
}
