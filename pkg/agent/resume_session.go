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
package agent

// Crash-resume for the bare conversation loop (issue #229). ResumeSession is
// the crash-triggered counterpart to ResumeChat's human-triggered resume
// (park.go): both complete an interrupted tail tool-call batch via the SAME
// locateParkedBatch detection and the SAME dispatchOneCall per-call body —
// they differ only in what decides each rowless call's fate (a human's
// ParkDecision there; the execution_events log here).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ExecutionGap is returned by ResumeSession when a call's real-world outcome
// is genuinely unknown: its intent was durably recorded but no matching
// result ever landed — either the process crashed mid-call, or the result
// write itself failed after the action succeeded. Both look identical, and
// neither is auto-retried (no per-tool idempotency classification exists to
// know if that would be safe). Resolve with `looms agent resolve-gap`.
type ExecutionGap struct {
	SessionID     string
	StepIndex     int
	BatchPosition int
	ToolName      string
	ToolUseID     string
}

func (e *ExecutionGap) Error() string {
	return fmt.Sprintf("execution gap: session %s step %d position %d (tool %q, call %s) has a recorded intent but no result — outcome unknown, resolve with `looms agent resolve-gap`",
		e.SessionID, e.StepIndex, e.BatchPosition, e.ToolName, e.ToolUseID)
}

// ResumeSession completes an interrupted turn after a crash: it claims the
// session, locates the incomplete tail batch (reusing locateParkedBatch —
// the same detection ResumeChat uses for HITL-park resume), and for each
// unresolved call either dispatches it (never attempted), backfills it from
// the event log (known-complete), or surfaces an ExecutionGap (unknown
// outcome). See the design doc's "Resume flow" section for the full
// rationale behind each branch.
func (a *Agent) ResumeSession(ctx context.Context, sessionID string) (*Response, error) {
	if a.eventLog == nil {
		return nil, ErrEventLogDisabled
	}
	ctx = session.WithSessionID(ctx, sessionID)

	claimToken := uuid.New().String()
	won, err := a.eventLog.ClaimSessionResume(ctx, sessionID, claimToken, eventlog.DefaultResumeClaimTTL)
	if err != nil {
		return nil, fmt.Errorf("claiming session for resume: %w", err)
	}
	if !won {
		return nil, fmt.Errorf("session %s is already being resumed by another process (or was, within the last %s)", sessionID, eventlog.DefaultResumeClaimTTL)
	}
	defer func() {
		if err := a.eventLog.ReleaseSessionResume(ctx, sessionID, claimToken); err != nil {
			zap.L().Warn("failed to release session resume claim", zap.String("session_id", sessionID), zap.Error(err))
		}
	}()

	// Also serializes against a same-process ResumeChat/chat on this session —
	// the same in-process guard ResumeChat itself takes (park.go:787).
	unlock := a.lockSession(sessionID)
	defer unlock()

	ctx, span := a.tracer.StartSpan(ctx, "agent.conversation.resume_session")
	defer a.tracer.EndSpan(span)
	span.SetAttribute(observability.AttrSessionID, sessionID)

	// This loop entry continues a turn that already ran the loop's per-TURN
	// entry work (graph-memory context injection) before it died — the same
	// reason ResumeChat marks its own ctx (park.go). Without it, a same-process
	// resume of a still-warm session injects a second context block into the
	// prompt and pays a second recall round-trip.
	ctx = contextWithResumedTurn(ctx)

	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")

	// A session that is legitimately HITL-parked — waiting on a human, not
	// crashed — must not be crash-resumed. Parking happens PRE-dispatch, so
	// every call in its tail batch looks "never attempted" here (no tool rows,
	// no recorded intents) and the re-park branch below would mint a SECOND
	// HumanRequest for the same batch: two cards for one decision, and (per
	// guardParkedTail's own logic) a session blocked until the duplicate's TTL
	// lapses. Only a PENDING request is the hazard, so this takes the narrow
	// guardPendingPark rather than Chat's full guardParkedTail: that guard's
	// decided-row branch would refuse a crash-resume merely because an old,
	// fully applied park is still inside its TTL.
	if err := a.guardPendingPark(ctx, sessionID); err != nil {
		span.AddEvent("resume_session.refused_parked", map[string]interface{}{"reason": err.Error()})
		return nil, err
	}

	batch, rowless, positions, err := locateParkedBatch(sess, nil)
	if err != nil {
		span.AddEvent("resume_session.nothing_to_resume", map[string]interface{}{"reason": err.Error()})
		return nil, err // ErrNothingParked / ErrNotParkedTail, both meaningful to the caller as-is
	}
	// len(rowless) == 0 here means every call in the tail batch already has a
	// row but no final assistant reply exists yet: the batch was applied and
	// the process crashed before the LLM produced its synthesizing reply (see
	// locateParkedBatch's own comment). The per-call loop below is naturally a
	// no-op in that case, and control falls through to runConversationLoop —
	// exactly like ResumeChat (park.go:941-945), which has no equivalent
	// refusal. Refusing here would strand the turn's answer permanently.

	events, err := a.eventLog.LoadEvents(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading execution events for resume: %w", err)
	}
	latestStep := latestStepIndex(events)

	ctx, handleCollector := a.adoptParkedHandles(ctx, sessionID)
	parkedAgain := false
	defer func() {
		if parkedAgain {
			return
		}
		a.leases.apply(sessionID, handleCollector.ReleaseAll(zap.L()), nil)
	}()

	agentCtx := &agentContext{Context: ctx, session: sess, tracer: a.tracer, progressCallback: nil}
	st := &batchState{
		span:      span,
		batchLen:  len(batch.ToolCalls),
		turnDedup: make(map[string]*shuttle.Result),
		stepIndex: latestStep,
	}
	var toolExecCount int
	var allExecs []ToolExecution
	st.toolExecutionCount = &toolExecCount
	st.allToolExecutions = &allExecs
	tools := a.advertisedTools(sess)
	st.tools = &tools
	if maxPerTurn := a.config.MaxIterations; maxPerTurn > 0 {
		st.maxPerTurn = maxPerTurn
	} else {
		st.maxPerTurn = 10
	}

	// neverAttempted is a FILTERED SUBSET of rowless, so its own indices say
	// nothing about batch position — it carries a parallel positions slice of
	// its own for exactly the reason locateParkedBatch's doc comment gives.
	var neverAttempted []ToolCall
	var neverAttemptedPositions []int
	for j, call := range rowless {
		position := positions[j]
		intentEvt, resultEvt := findEventPair(events, latestStep, position)

		switch {
		case intentEvt == nil:
			// Never attempted: collected for dispatch below, AFTER giving it
			// the same chance a fresh turn's pre-scan gives it to park again
			// for HITL (maybeParkBatch is never reached by dispatching
			// straight to dispatchOneCall, which bypasses that scan).
			neverAttempted = append(neverAttempted, call)
			neverAttemptedPositions = append(neverAttemptedPositions, position)

		case resultEvt != nil:
			// Known-complete: backfill messages/tool_executions from the
			// recorded event rather than re-invoking the tool. The event
			// log is authoritative here — see the design doc's "Resume
			// flow" step 3.
			if err := a.backfillFromResultEvent(agentCtx, sess, call, *resultEvt); err != nil {
				return nil, fmt.Errorf("backfilling call %s from recorded result: %w", call.ID, err)
			}

		default:
			// Intent with no result: genuinely unknown outcome. The gap is
			// named by FindExecutionGap, the same function `looms agent
			// resolve-gap` uses, so the gap resume reports is by construction
			// the gap resolve-gap resolves. Calls earlier in the batch have
			// already been backfilled by this point, which is intended.
			if gap := FindExecutionGap(sess, events); gap != nil {
				return nil, gap
			}
			return nil, fmt.Errorf("internal: call %s at position %d has an intent but no result and FindExecutionGap did not report it", call.ID, position)
		}
	}

	// Re-park check: a never-attempted call may be Ask-classified or a
	// contact_human whose intent crashed before it was even recorded.
	// Classifying it against each call's TRUE batch position (not a fresh
	// index over this subset) keeps verifyItemBinding's "seq is the call's
	// true position in the original batch" invariant intact for a later
	// resume of this re-park.
	if a.hitlPark != nil && a.memory.HasStore() && len(neverAttempted) > 0 {
		items := a.classifyParkItems(agentCtx, neverAttempted, neverAttemptedPositions)
		if parkErr := a.parkItems(agentCtx, sess, items, Usage{}, Usage{}); parkErr != nil {
			var parked *TurnParkedError
			if errors.As(parkErr, &parked) {
				parkedAgain = true
				a.parkHandles(sessionID, handleCollector)
				if perr := a.memory.PersistSession(ctx, sess); perr != nil {
					zap.L().Warn("failed to persist session at resume-park", zap.String("session_id", sessionID), zap.Error(perr))
				}
			}
			return nil, parkErr
		}
	}
	for j, call := range neverAttempted {
		a.dispatchOneCall(agentCtx, sess, call, neverAttemptedPositions[j], st)
	}

	// Every call in the tail batch is now resolved: known-complete calls
	// were backfilled above, and every never-attempted call either
	// re-parked (returning before this point) or was just dispatched. No
	// ExecutionGap or re-park occurred, or we would already have returned —
	// so latestStep carries no further replay value.
	a.pruneStepIfResolved(ctx, sessionID, latestStep)

	response, err := a.runConversationLoop(agentCtx)
	if err != nil {
		var parked *TurnParkedError
		if errors.As(err, &parked) {
			parkedAgain = true
			a.parkHandles(sessionID, handleCollector)
			if perr := a.memory.PersistSession(ctx, sess); perr != nil {
				zap.L().Warn("failed to persist session at resume-park", zap.String("session_id", sessionID), zap.Error(perr))
			}
			return nil, err
		}
		return nil, fmt.Errorf("conversation loop after resume: %w", err)
	}

	// Record the synthesized reply in session history — mirrors Chat's own
	// post-loop append (agent.go) and ResumeChat's (park.go): runConversationLoop
	// itself never appends its own final assistant message, so every caller
	// must. Without this, locateParkedBatch's finalReply scan (park.go) never
	// finds a trailing assistant row for this tail batch, and the batch never
	// reaches a terminal "nothing to resume" state — a second ResumeSession
	// call would re-enter the conversation loop and re-synthesize a reply
	// instead of reporting ErrNothingParked, even though every tool call was
	// already resolved.
	a.appendMessage(ctx, sess, Message{
		Role:       "assistant",
		Content:    response.Content,
		AgentID:    a.id,
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}, false)

	if perr := a.memory.PersistSession(ctx, sess); perr != nil {
		zap.L().Warn("failed to persist session after resume", zap.String("session_id", sessionID), zap.Error(perr))
	}
	return response, nil
}

// FindExecutionGap returns the gap ResumeSession reports for sess, or nil when
// there is none: the first call in the tail batch that has NO tool row yet and
// whose recorded tool_call_intent has no matching tool_call_result, taken in
// batch order at the latest recorded step.
//
// A call that already has a tool row is not a gap even if its result event is
// missing: the row is the durable record that it ran and its output was kept,
// so there is nothing for an operator to resolve. Deciding that needs the
// session, which is why this takes it rather than only the event log — and why
// the CLI must use this instead of scanning events for unmatched intents.
func FindExecutionGap(sess *Session, events []eventlog.ExecutionEvent) *ExecutionGap {
	_, rowless, positions, err := locateParkedBatch(sess, nil)
	if err != nil {
		return nil
	}
	latestStep := latestStepIndex(events)
	for j, call := range rowless {
		position := positions[j]
		intentEvt, resultEvt := findEventPair(events, latestStep, position)
		if intentEvt != nil && resultEvt == nil {
			return &ExecutionGap{
				SessionID: sess.ID, StepIndex: latestStep, BatchPosition: position,
				ToolName: call.Name, ToolUseID: call.ID,
			}
		}
	}
	return nil
}

// latestStepIndex returns the highest StepIndex among events, or 0 if events
// is empty (in which case no event will ever match in findEventPair below,
// so every rowless call falls into the "never attempted" branch — a safe
// default for a session with no recorded events at all).
func latestStepIndex(events []eventlog.ExecutionEvent) int {
	max := 0
	for _, e := range events {
		if e.StepIndex > max {
			max = e.StepIndex
		}
	}
	return max
}

// findEventPair returns the intent and result events (if any) for one call's
// (stepIndex, batchPosition), or nil for either that was not found.
func findEventPair(events []eventlog.ExecutionEvent, stepIndex, batchPosition int) (intent, result *eventlog.ExecutionEvent) {
	for i := range events {
		e := &events[i]
		if e.StepIndex != stepIndex || e.BatchPosition != batchPosition {
			continue
		}
		switch e.EventType {
		case eventlog.EventToolCallIntent:
			intent = e
		case eventlog.EventToolCallResult:
			result = e
		}
	}
	return intent, result
}

// backfillFromResultEvent reconstructs the tool_executions row and tool
// message from a recorded tool_call_result event, without re-invoking the
// tool. Uses the SAME persistence calls dispatchOneCall itself uses
// (PersistToolExecution, appendMessage) so a backfilled call is
// indistinguishable from a normally-completed one in every later read.
func (a *Agent) backfillFromResultEvent(ctx Context, sess *Session, call ToolCall, resultEvt eventlog.ExecutionEvent) error {
	var recorded struct {
		Result *shuttle.Result `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(resultEvt.Payload, &recorded); err != nil {
		return fmt.Errorf("unmarshal recorded tool_call_result payload: %w", err)
	}

	execution := ToolExecution{ToolName: call.Name, Input: call.Input, Result: recorded.Result}
	if recorded.Error != "" {
		execution.Error = fmt.Errorf("%s", recorded.Error)
	}
	if persistErr := a.memory.PersistToolExecution(ctx, sess.ID, execution); persistErr != nil {
		zap.L().Warn("failed to persist backfilled tool execution", zap.String("session_id", sess.ID), zap.String("tool", call.Name), zap.Error(persistErr))
	}

	a.appendMessage(ctx, sess, Message{
		Role:       "tool",
		Content:    a.formatToolResult(ctx, sess.ID, call.Name, recorded.Result, execution.Error),
		ToolUseID:  call.ID,
		ToolResult: recorded.Result,
		AgentID:    a.id,
		Timestamp:  time.Now(),
	}, false)
	return nil
}
