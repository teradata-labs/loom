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
// (park.go): both complete an interrupted tail tool-call batch via the same
// locateParkedBatch detection and the same dispatchOneCall per-call body.
// They differ only in what decides each rowless call's fate: a human's
// ParkDecision there, the execution_events log here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/eventlog"
	mcpadapter "github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// ErrTurnNotRecorded is returned by ResumeSession when the interrupted batch
// has no matching LLM response in the execution log: the turn ran without the
// log, or the log lost its record of the step. Resume refuses because it
// cannot tell which calls already ran, and dispatching them blind could run a
// tool twice.
var ErrTurnNotRecorded = errors.New("the interrupted turn was not recorded in the execution log, so resume cannot tell which tool calls already ran")

// ErrBatchNotSaved is returned by ResumeSession when the session ends in an
// unanswered user message, but the execution log shows tools ran for a batch
// the session never saved (the assistant message requesting them failed to
// persist). Re-running the turn would ask the LLM again and could run those
// tools a second time.
var ErrBatchNotSaved = errors.New("tools ran for a batch whose assistant message was not saved; the turn cannot be resumed automatically")

// ExecutionGap is returned by ResumeSession when a call's real-world outcome
// is genuinely unknown: its intent was durably recorded but no matching
// result ever landed. Either the process crashed mid-call, or the result
// write itself failed after the action succeeded. Both look identical, and
// neither is retried automatically (no per-tool idempotency classification
// exists to know if that would be safe). Resolve with `loom sessions resolve-gap` (Agent.ResolveExecutionGap).
type ExecutionGap struct {
	SessionID     string
	StepIndex     int
	BatchPosition int
	ToolName      string
	ToolUseID     string
}

func (e *ExecutionGap) Error() string {
	return fmt.Sprintf("execution gap: session %s step %d position %d (tool %q, call %s) has a recorded intent but no result — outcome unknown, resolve with `loom sessions resolve-gap`",
		e.SessionID, e.StepIndex, e.BatchPosition, e.ToolName, e.ToolUseID)
}

// ResumeSession completes a turn interrupted by a crash.
//
// When the session ends in an unanswered user message (the crash came before
// the turn's first batch was saved), it re-runs the turn's conversation loop:
// LLM calls have no side effects. Otherwise it locates the incomplete tail
// batch and, for each call without a tool row, reads the step the batch was
// recorded under and either dispatches the call (no intent: never attempted),
// backfills it from the recorded result (known complete), or returns an
// ExecutionGap (intent with no result: outcome unknown). A batch with no
// recorded step returns ErrTurnNotRecorded instead of guessing.
func (a *Agent) ResumeSession(ctx context.Context, sessionID string) (*Response, error) {
	if a.eventLog == nil {
		return nil, ErrEventLogDisabled
	}
	ctx = session.WithSessionID(ctx, sessionID)

	// Same order as ResumeChat and ResolveExecutionGap: the in-process lock
	// first, then the lease. A caller waiting on the lock then holds no lease,
	// so it cannot keep another replica's resume out while it queues.
	unlock := a.lockSession(sessionID)
	defer unlock()

	// The session lease keeps a live turn, another resume, or a second
	// replica from dispatching the same calls while this resume runs.
	ctx, releaseLease, err := a.holdSessionLease(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer releaseLease()

	ctx, span := a.tracer.StartSpan(ctx, "agent.conversation.resume_session")
	defer a.tracer.EndSpan(span)
	span.SetAttribute(observability.AttrSessionID, sessionID)

	// This loop entry continues a turn that already ran the loop's per-turn
	// entry work (graph-memory context injection) before it died, the same
	// reason ResumeChat marks its own ctx. Without it, a same-process resume
	// of a still-warm session injects a second context block into the prompt.
	ctx = contextWithResumedTurn(ctx)

	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")

	// A session that is HITL-parked (waiting on a human, not crashed) must not
	// be crash-resumed: parking happens before dispatch, so every call in its
	// batch looks never attempted and the re-park below would mint a second
	// HumanRequest for the same batch. Only a pending request is the hazard.
	if err := a.guardPendingPark(ctx, sessionID); err != nil {
		span.AddEvent("resume_session.refused_parked", map[string]interface{}{"reason": err.Error()})
		return nil, err
	}

	events, err := a.eventLog.LoadEvents(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading execution events for resume: %w", err)
	}

	ctx, handleCollector := a.adoptParkedHandles(ctx, sessionID)
	parkedAgain := false
	defer func() {
		if parkedAgain {
			return
		}
		a.leases.apply(sessionID, handleCollector.ReleaseAll(zap.L()), nil)
	}()
	agentCtx := &agentContext{Context: ctx, session: sess, tracer: a.tracer, progressCallback: nil}

	// A batch whose tools ran but whose assistant message never saved leaves
	// the session looking like an earlier batch just finished. Whatever the
	// tail looks like, resuming from it would ask the LLM again and could run
	// that batch's tools a second time, so every path refuses here.
	msgs := rawSessionMessages(sess)
	if step, unsaved := unsavedBatchStep(events, msgs); unsaved {
		span.AddEvent("resume_session.batch_not_saved", map[string]interface{}{"step_index": step})
		return nil, fmt.Errorf("session %s step %d: %w", sessionID, step, ErrBatchNotSaved)
	}
	if unansweredTurnTail(msgs) {
		span.AddEvent("resume_session.unanswered_turn", nil)
		return a.finishResumedTurn(ctx, agentCtx, sess, handleCollector, &parkedAgain)
	}

	batch, rowless, positions, err := locateParkedBatch(sess, nil)
	if err != nil {
		span.AddEvent("resume_session.nothing_to_resume", map[string]interface{}{"reason": err.Error()})
		return nil, err // ErrNothingParked / ErrNotParkedTail, both meaningful to the caller as-is
	}
	// len(rowless) == 0 means every call in the tail batch already has a row
	// but no final assistant reply exists yet: the batch was applied and the
	// process crashed before the LLM produced its reply. No call needs a
	// decision, so the turn just continues, exactly like ResumeChat. (The
	// batch's step may already be pruned, since its rows are durable.)
	if len(rowless) == 0 {
		span.AddEvent("resume_session.batch_complete", nil)
		return a.finishResumedTurn(ctx, agentCtx, sess, handleCollector, &parkedAgain)
	}
	step, eventPositions, recorded := recordedStepForBatch(events, batch)
	if !recorded {
		span.AddEvent("resume_session.not_recorded", nil)
		return nil, fmt.Errorf("session %s: %w", sessionID, ErrTurnNotRecorded)
	}
	span.SetAttribute("resume.step_index", step)

	// The same recovery orchestrator the loop and ResumeChat give a batch, so
	// self-healing applies to calls dispatched by a resume too.
	var recovery *recoveryOrchestrator
	if a.config.EnableSelfHealing {
		recovery = newRecoveryOrchestrator(a.config.RecoveryConfig, span)
	}
	st := &batchState{
		span:      span,
		batchLen:  len(batch.ToolCalls),
		turnDedup: make(map[string]*shuttle.Result),
		recovery:  recovery,
		stepIndex: step,
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

	// Each call has two positions: its index in the session's batch message
	// (what HITL park binding uses) and its index in the recorded LLM response
	// (the batch_position its events were written under). They differ when the
	// saved message dropped calls the response held (query_tool_result).
	var neverAttempted []ToolCall
	var neverAttemptedPositions, neverAttemptedEventPositions []int
	for j, call := range rowless {
		eventPos := eventPositions[positions[j]]
		intentEvt, resultEvt := findEventPair(events, step, eventPos)

		switch {
		case intentEvt == nil:
			// Never attempted: collected for dispatch below, after giving it
			// the chance a fresh turn's pre-scan gives it to park for HITL.
			neverAttempted = append(neverAttempted, call)
			neverAttemptedPositions = append(neverAttemptedPositions, positions[j])
			neverAttemptedEventPositions = append(neverAttemptedEventPositions, eventPos)

		case resultEvt != nil:
			// Known complete: backfill the row from the recorded result rather
			// than re-invoking the tool.
			persisted, err := a.backfillFromResultEvent(agentCtx, sess, call, *resultEvt)
			if err != nil {
				return nil, fmt.Errorf("backfilling call %s from recorded result: %w", call.ID, err)
			}
			if !persisted {
				st.rowNotPersisted = true
			}

		default:
			// Intent with no result: outcome unknown. The gap is named by
			// FindExecutionGap, the same function ResolveExecutionGap
			// uses, so the gap resume reports is the gap resolve-gap resolves.
			if gap := FindExecutionGap(sess, events); gap != nil {
				return nil, gap
			}
			return nil, fmt.Errorf("internal: call %s at position %d has an intent but no result and FindExecutionGap did not report it", call.ID, eventPos)
		}
	}

	// Re-park check: a never-attempted call may be Ask-classified or a
	// contact_human whose intent was never recorded. Classifying against each
	// call's true batch position keeps verifyItemBinding's "seq is the call's
	// position in the original batch" invariant for a later resume.
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
		a.dispatchOneCall(agentCtx, sess, call, neverAttemptedEventPositions[j], st)
	}
	if leaseErr := sessionLeaseLost(ctx); leaseErr != nil {
		return nil, leaseErr
	}

	// Every call in the tail batch is now resolved (backfilled, or
	// dispatched), so the step carries no further replay value once its rows
	// are durable.
	a.pruneStepIfResolved(ctx, sessionID, step, !st.rowNotPersisted)

	return a.finishResumedTurn(ctx, agentCtx, sess, handleCollector, &parkedAgain)
}

// finishResumedTurn runs the conversation loop to the turn's reply, records
// the reply in session history, and clears the session's remaining execution
// events once that reply is durable.
func (a *Agent) finishResumedTurn(ctx context.Context, agentCtx *agentContext, sess *Session, handleCollector *mcpadapter.HandleCollector, parkedAgain *bool) (*Response, error) {
	response, err := a.runConversationLoop(agentCtx)
	if err != nil {
		var parked *TurnParkedError
		if errors.As(err, &parked) {
			*parkedAgain = true
			a.parkHandles(sess.ID, handleCollector)
			if perr := a.memory.PersistSession(ctx, sess); perr != nil {
				zap.L().Warn("failed to persist session at resume-park", zap.String("session_id", sess.ID), zap.Error(perr))
			}
			return nil, err
		}
		return nil, fmt.Errorf("conversation loop after resume: %w", err)
	}

	// runConversationLoop never appends its own final assistant message, so
	// every caller must (as Chat and ResumeChat do). Without it the batch
	// never reaches a terminal state and a second resume would re-run the loop.
	_, replyPersisted := a.appendMessage(ctx, sess, Message{
		Role:       "assistant",
		Content:    response.Content,
		AgentID:    a.id,
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}, false)
	a.pruneFinishedTurn(ctx, sess.ID, replyPersisted)

	if perr := a.memory.PersistSession(ctx, sess); perr != nil {
		zap.L().Warn("failed to persist session after resume", zap.String("session_id", sess.ID), zap.Error(perr))
	}
	return response, nil
}

// FindExecutionGap returns the gap ResumeSession reports for sess, or nil when
// there is none: the first call in the tail batch that has no tool row yet
// and whose recorded tool_call_intent has no matching tool_call_result, at the
// step the batch was recorded under.
//
// A call that already has a tool row is not a gap even if its result event is
// missing: the row is the durable record that it ran and its output was kept.
// Deciding that needs the session, which is why this takes it rather than
// only the event log, and why the CLI uses this instead of scanning events.
func FindExecutionGap(sess *Session, events []eventlog.ExecutionEvent) *ExecutionGap {
	batch, rowless, positions, err := locateParkedBatch(sess, nil)
	if err != nil {
		return nil
	}
	step, eventPositions, recorded := recordedStepForBatch(events, batch)
	if !recorded {
		return nil
	}
	for j, call := range rowless {
		eventPos := eventPositions[positions[j]]
		intentEvt, resultEvt := findEventPair(events, step, eventPos)
		if intentEvt != nil && resultEvt == nil {
			return &ExecutionGap{
				SessionID: sess.ID, StepIndex: step, BatchPosition: eventPos,
				ToolName: call.Name, ToolUseID: call.ID,
			}
		}
	}
	return nil
}

// recordedStepForBatch finds the step whose recorded LLM response requested
// batch's tool calls, newest step first. It also returns, for each call in
// batch.ToolCalls, that call's index in the recorded response: the
// batch_position its events were written under. ok is false when no recorded
// response matches, meaning the turn was not recorded (or its record lost).
//
// The newest matching step is not always the latest step: if a batch's tool
// row failed to persist, the loop went on to record later steps before the
// crash, and the batch's own events sit at an earlier step.
func recordedStepForBatch(events []eventlog.ExecutionEvent, batch Message) (step int, positions []int, ok bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.EventType != eventlog.EventLLMCallResult {
			continue
		}
		var resp LLMResponse
		if err := json.Unmarshal(e.Payload, &resp); err != nil {
			continue
		}
		if pos, ok := matchRecordedCalls(resp.ToolCalls, batch.ToolCalls); ok {
			return e.StepIndex, pos, true
		}
	}
	return 0, nil, false
}

// matchRecordedCalls maps each call in saved (the batch as the session holds
// it) to its index in recorded (the LLM response as the event log holds it).
// Calls match on name and input, in order. The saved batch may lack
// query_tool_result calls, which PersistMessage drops; any other difference
// means the two are different batches.
func matchRecordedCalls(recorded, saved []ToolCall) ([]int, bool) {
	positions := make([]int, 0, len(saved))
	r := 0
	for _, c := range saved {
		for r < len(recorded) && !sameToolCall(recorded[r], c) {
			if recorded[r].Name != queryToolResultName {
				return nil, false
			}
			r++
		}
		if r == len(recorded) {
			return nil, false
		}
		positions = append(positions, r)
		r++
	}
	for ; r < len(recorded); r++ {
		if recorded[r].Name != queryToolResultName {
			return nil, false
		}
	}
	return positions, len(saved) > 0
}

// queryToolResultName is the built-in call PersistMessage leaves out of saved
// assistant messages.
const queryToolResultName = "query_tool_result"

// sameToolCall compares a recorded call with a saved one. The saved message
// holds the very ToolCall the log recorded, so id, name and input all agree
// for a true match; the id keeps two identical batches from different LLM
// responses apart.
func sameToolCall(a, b ToolCall) bool {
	return a.ID == b.ID && a.Name == b.Name && canonicalJSON(a.Input) == canonicalJSON(b.Input)
}

// unansweredTurnTail reports whether msgs ends in a user message that opened
// a turn and has no assistant or tool message after it: the crash came
// before the turn saved anything past the user's message.
func unansweredTurnTail(msgs []Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msgs[i].Role {
		case "assistant", "tool":
			return false
		case "user":
			// A user row in the same turn as the assistant message before it
			// is in-turn machinery, not a new turn.
			for j := i - 1; j >= 0; j-- {
				if msgs[j].Role == "assistant" {
					return msgs[i].Turn > msgs[j].Turn
				}
			}
			return true
		}
	}
	return false
}

// unsavedBatchStep reports a step at which tools were dispatched (it has a
// tool_call_intent) for a batch that no assistant message in msgs holds. With
// no saved batch to resume from, re-running the turn would ask the LLM again
// and could run those tools twice. A step whose LLM response is missing from
// the log cannot be checked and is reported too.
//
// Each saved assistant message vouches for at most one step, and steps are
// taken in order, so an earlier identical batch cannot also stand in for a
// later one that never saved. Calls match on tool-call id as well as name and
// input (see sameToolCall); a provider that reuses ids across responses can
// still confuse two identical batches.
func unsavedBatchStep(events []eventlog.ExecutionEvent, msgs []Message) (int, bool) {
	dispatched := make(map[int]bool)
	responses := make(map[int][]ToolCall)
	for _, e := range events {
		switch e.EventType {
		case eventlog.EventToolCallIntent:
			// query_tool_result only reads this turn's in-memory results and is
			// never saved with its batch, so a step holding only those has
			// nothing a re-run could do twice.
			if !isQueryToolResultIntent(e) {
				dispatched[e.StepIndex] = true
			}
		case eventlog.EventLLMCallResult:
			var resp LLMResponse
			if err := json.Unmarshal(e.Payload, &resp); err == nil {
				responses[e.StepIndex] = resp.ToolCalls
			}
		}
	}
	steps := make([]int, 0, len(dispatched))
	for step := range dispatched {
		steps = append(steps, step)
	}
	sort.Ints(steps)

	claimed := make(map[int]bool)
	for _, step := range steps {
		recorded, ok := responses[step]
		if !ok {
			return step, true
		}
		saved := false
		for i, m := range msgs {
			if claimed[i] || m.Role != "assistant" || len(m.ToolCalls) == 0 {
				continue
			}
			if _, match := matchRecordedCalls(recorded, m.ToolCalls); match {
				claimed[i] = true
				saved = true
				break
			}
		}
		if !saved {
			return step, true
		}
	}
	return 0, false
}

// isQueryToolResultIntent reports whether a tool_call_intent event is for the
// built-in query_tool_result call. An intent whose payload cannot be read is
// not, so the caller treats it as a real dispatch.
func isQueryToolResultIntent(e eventlog.ExecutionEvent) bool {
	var p struct {
		ToolName string `json:"tool_name"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return false
	}
	return p.ToolName == queryToolResultName
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
// tool. It uses the same persistence calls dispatchOneCall uses, so a
// backfilled call looks like a normally completed one in every later read.
// persisted reports whether the tool row was durably saved.
func (a *Agent) backfillFromResultEvent(ctx Context, sess *Session, call ToolCall, resultEvt eventlog.ExecutionEvent) (persisted bool, err error) {
	var recorded struct {
		Result *shuttle.Result `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(resultEvt.Payload, &recorded); err != nil {
		return false, fmt.Errorf("unmarshal recorded tool_call_result payload: %w", err)
	}

	execution := ToolExecution{ToolName: call.Name, Input: call.Input, Result: recorded.Result}
	if recorded.Error != "" {
		execution.Error = fmt.Errorf("%s", recorded.Error)
	}
	if persistErr := a.memory.PersistToolExecution(ctx, sess.ID, execution); persistErr != nil {
		zap.L().Warn("failed to persist backfilled tool execution", zap.String("session_id", sess.ID), zap.String("tool", call.Name), zap.Error(persistErr))
	}

	_, persisted = a.appendMessage(ctx, sess, Message{
		Role:       "tool",
		Content:    a.formatToolResult(ctx, sess.ID, call.Name, recorded.Result, execution.Error),
		ToolUseID:  call.ID,
		ToolResult: recorded.Result,
		AgentID:    a.id,
		Timestamp:  time.Now(),
	}, false)
	return persisted, nil
}

// ErrNoExecutionGap is returned by ResolveExecutionGap when the session has
// no call whose outcome is unknown.
var ErrNoExecutionGap = errors.New("the session has no execution gap to resolve")

// GapOutcome is an operator's finding for an execution gap.
type GapOutcome int

const (
	// GapSucceeded: the call ran and succeeded; its result is supplied.
	GapSucceeded GapOutcome = iota + 1
	// GapFailed: the call did not take effect.
	GapFailed
)

// ResolveExecutionGap records an operator's finding for the session's current
// execution gap (the one ResumeSession reports) as that call's result, so a
// following ResumeSession backfills it instead of stopping. resultJSON is the
// call's result data and is required for GapSucceeded. It holds the session
// lease, so it cannot run while a resume or turn is driving the session.
//
// The recorded result is the caller's assertion and the agent treats it as
// real tool output. It is marked manually_resolved with the caller's user id,
// but any caller who can open the session may resolve its gap; there is no
// operator-only check.
func (a *Agent) ResolveExecutionGap(ctx context.Context, sessionID string, outcome GapOutcome, resultJSON []byte) (*ExecutionGap, error) {
	if a.eventLog == nil {
		return nil, ErrEventLogDisabled
	}

	// The result is the resolver's assertion, not something the tool returned.
	// It is marked so the tool row can be told apart from a real execution, and
	// names the caller (empty when the deployment has no user identity).
	marker := map[string]interface{}{"manually_resolved": true, "resolved_by": types.UserIDFromContext(ctx)}

	var payload map[string]interface{}
	switch outcome {
	case GapSucceeded:
		var data interface{}
		if err := json.Unmarshal(resultJSON, &data); err != nil {
			return nil, fmt.Errorf("result for a succeeded call must be JSON: %w", err)
		}
		payload = map[string]interface{}{"result": shuttle.Result{Success: true, Data: data, Metadata: marker}, "error": ""}
	case GapFailed:
		const msg = "operator resolved this execution gap as failed"
		payload = map[string]interface{}{
			"result": shuttle.Result{Success: false, Error: &shuttle.Error{Code: "manually_resolved_failed", Message: msg}, Metadata: marker},
			"error":  msg,
		}
	default:
		return nil, fmt.Errorf("unknown gap outcome %d", outcome)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("building the resolved result: %w", err)
	}

	ctx = session.WithSessionID(ctx, sessionID)
	unlock := a.lockSession(sessionID)
	defer unlock()
	ctx, releaseLease, err := a.holdSessionLease(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	defer releaseLease()

	ctx, span := a.tracer.StartSpan(ctx, "agent.conversation.resolve_execution_gap")
	defer a.tracer.EndSpan(span)
	span.SetAttribute(observability.AttrSessionID, sessionID)

	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")
	events, err := a.eventLog.LoadEvents(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading execution events: %w", err)
	}
	// The gap to resolve is the one ResumeSession reports; asking the same
	// function is what guarantees it.
	gap := FindExecutionGap(sess, events)
	if gap == nil {
		return nil, ErrNoExecutionGap
	}
	if err := a.eventLog.RecordResult(ctx, sessionID, gap.StepIndex, gap.BatchPosition, eventlog.EventToolCallResult, gap.ToolUseID, payloadJSON); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("recording the resolution: %w", err)
	}
	span.SetAttribute("gap.tool_name", gap.ToolName)
	return gap, nil
}
