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

	_, created, err := a.implicitTasks.EnsureForTurn(ctx, task.TurnRequest{
		SessionID:   sess.ID,
		AgentID:     a.id,
		BoardID:     boardID,
		TurnIndex:   int(tc.TurnIndex()),
		Trigger:     trigger,
		UserMessage: tc.UserMessage(),
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
	if bf, ok := a.memory.Store().(interface {
		AttributeTurnMessages(ctx context.Context, sessionID, taskID string, since time.Time) (int64, error)
	}); ok {
		if _, err := bf.AttributeTurnMessages(ctx, sess.ID, created.ID, turnStartedAt(sess)); err != nil {
			zap.L().Debug("implicit task back-fill failed",
				zap.String("task_id", created.ID), zap.Error(err))
		}
	}

	zap.L().Debug("implicit task recorded",
		zap.String("task_id", created.ID),
		zap.String("trigger", trigger.String()),
		zap.Int64("turn", tc.TurnIndex()))
}

// turnStartedAt returns a lower time bound for the current turn's messages,
// used to scope the back-fill.
//
// The session's last message is the turn's own opening user message (the hook
// runs before any assistant row is persisted), so its timestamp is the boundary.
// An empty session falls back to the zero time, which claims nothing.
func turnStartedAt(sess *Session) time.Time {
	msgs := sess.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Timestamp
		}
	}
	return time.Time{}
}

// completeImplicitTask closes the turn's recorded task once the conversation
// loop returns.
//
// The task is opened IN_PROGRESS at the first tool call and nothing else would
// ever close it — no agent claimed it and no human is working it — so without
// this every recorded task sits in progress forever and the session panel reads
// 0/1 done for finished work.
func (a *Agent) completeImplicitTask(ctx context.Context, binding *taskctx.Binding, closeReason string) {
	if a.implicitTasks == nil || binding == nil {
		return
	}
	attr, ok := binding.Get()
	if !ok {
		// No task was recorded for this turn — the common case for a turn that
		// only talked.
		return
	}
	a.implicitTasks.CompleteForTurn(ctx, attr.TaskID, closeReason)
}
