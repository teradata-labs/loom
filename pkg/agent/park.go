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

// HITL park-and-resume. A tool batch that needs a human decision ends the
// turn instead of holding it open: the pre-scan (maybeParkBatch) runs before
// anything in the batch executes, persists ONE grouped human request for the
// whole batch, and returns TurnParkedError. The turn's durable tail is then
// …, user, assistant(ToolCalls) with no tool rows for the parked batch.
// ResumeChat is the missing entry point that continues that turn without
// appending a user message: it completes (or refuses) the parked batch under
// the human's decision and re-enters the conversation loop.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	mcpadapter "github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// hitlParkConfig is the park wiring an embedder installs via WithHITLPark.
type hitlParkConfig struct {
	store    shuttle.HumanRequestStore
	ttl      time.Duration
	notifier shuttle.Notifier
}

// defaultParkTTL bounds how long a parked request may wait for a decision
// when the embedder passes a non-positive ttl. A zero-expiry pending row is
// uninsertable and undecidable at the stores' guards, so "no expiry" is not
// offered.
const defaultParkTTL = 168 * time.Hour

// WithHITLPark enables park-and-resume for governed calls and contact_human.
// store persists the grouped request; ttl bounds how long a parked request
// may wait for a decision (non-positive → 168h); notifier emits the pending
// event. A nil store leaves park disabled and the legacy in-turn resolver
// path intact.
func WithHITLPark(store shuttle.HumanRequestStore, ttl time.Duration, notifier shuttle.Notifier) Option {
	return func(a *Agent) {
		if store == nil {
			return
		}
		if ttl <= 0 {
			ttl = defaultParkTTL
		}
		a.hitlPark = &hitlParkConfig{store: store, ttl: ttl, notifier: notifier}
	}
}

// TurnParkedError ends a turn that needs a human decision. The turn's tail in
// the session store is …, user, assistant(ToolCalls) — no tool rows for the
// parked batch. ResumeChat completes the pair set and continues the loop.
// Usage carries the parking LLM response's usage (the same last-response
// semantics Response.Usage has) so the embedder can complete its execution
// accounting.
type TurnParkedError struct {
	RequestID string
	SessionID string
	ExpiresAt time.Time
	Usage     Usage
}

func (e *TurnParkedError) Error() string {
	return fmt.Sprintf("turn parked awaiting human decision (request %s)", e.RequestID)
}

// SessionParkedError refuses a NEW user turn while a pending parked decision
// owns the session tail. Appending the turn would bury the parked batch and
// the decision could never be applied. Raised at the append point — the
// authoritative last moment — because the embedder's admission-time probe
// races the park row, which is written mid-turn.
type SessionParkedError struct {
	RequestID string
	SessionID string
	ExpiresAt time.Time
}

func (e *SessionParkedError) Error() string {
	return fmt.Sprintf("session is waiting for a human decision (request %s)", e.RequestID)
}

// Typed terminals for ResumeChat callers. All three are terminal: the caller
// must finish the request's lifecycle and never retry the resume.
var (
	// ErrNothingParked: the tail turn is fully complete — there is no batch to
	// finish and no loop to re-enter.
	ErrNothingParked = errors.New("nothing parked: the turn is already complete")
	// ErrNotParkedTail: a user message of a later turn follows the parked
	// batch — history moved on and the decision can no longer be applied.
	ErrNotParkedTail = errors.New("parked batch is no longer the session tail")
	// ErrStaleDecision: the decision's item IDs do not all appear in the tail
	// batch — it belongs to a batch that is no longer the tail.
	ErrStaleDecision = errors.New("decision does not match the parked batch")
)

// ParkDecision is the human's verdict for one parked batch. ItemIDs are the
// request's params keys — the ToolCall.IDs of the items the human saw;
// ResumeChat refuses to apply the decision to any other batch. Reason is used
// VERBATIM as the refusal text on every synthesized denial (the caller
// composes it: "rejected by user: …", "approval timed out"). Answers maps
// question-item ToolCall.IDs to answer text.
type ParkDecision struct {
	RequestID string
	ItemIDs   []string
	Approved  bool
	Reason    string
	Answers   map[string]string
}

// guardParkedTail refuses a new user turn while the session holds a PENDING
// parked request. Store errors fail open with a warn — the embedder's own
// admission probe is the primary gate; this guard closes its race with a park
// landing mid-turn, and must not turn a store hiccup into a dead session.
func (a *Agent) guardParkedTail(ctx context.Context, sessionID string) error {
	if a.hitlPark == nil {
		return nil
	}
	reqs, err := a.hitlPark.store.ListBySession(ctx, sessionID)
	if err != nil {
		zap.L().Warn("parked-tail guard: listing session requests failed; admitting turn",
			zap.String("session_id", sessionID), zap.Error(err))
		return nil
	}
	for _, r := range reqs {
		if r != nil && r.RequestType == "parked" && r.Status == "pending" {
			return &SessionParkedError{RequestID: r.ID, SessionID: sessionID, ExpiresAt: r.ExpiresAt}
		}
	}
	return nil
}

// isParkQuestion reproduces the pre-scan's question classification at
// completion time: a registered contact_human whose preflight verdict is not
// Deny. A contact_human that fails this — policy-denied, or unregistered —
// was deliberately NOT a park item and must complete through the normal
// dispatch ceremony (policy denial, "tool not found"), never through answer
// synthesis: a caller-supplied answer for it would lift a Deny.
func (a *Agent) isParkQuestion(ctx Context, call ToolCall) bool {
	if call.Name != "contact_human" {
		return false
	}
	if _, ok := a.tools.Get("contact_human"); !ok {
		return false
	}
	return a.executor.Preflight(ctx, call.Name, call.Input).Kind != shuttle.Deny
}

// parkItem is one batch call needing a human: an approval-gated call or a
// contact_human question.
type parkItem struct {
	call     ToolCall
	seq      int
	kind     string // "approval" | "question"
	question string
}

// maybeParkBatch is the pre-scan: every call of the batch — contact_human
// included — is preflighted; a Deny is never lifted (the call falls to the
// dispatch loop and denies as today), a contact_human survives as a question
// item, an Ask as an approval item. With no items it returns nil and the
// serial dispatch loop runs exactly as today. With items it persists ONE
// grouped request and ends the turn with TurnParkedError.
func (a *Agent) maybeParkBatch(ctx Context, sess *Session, llmResp *LLMResponse) error {
	var items []parkItem
	for i, call := range llmResp.ToolCalls {
		d := a.executor.Preflight(ctx, call.Name, call.Input)
		if d.Kind == shuttle.Deny {
			continue
		}
		if call.Name == "contact_human" {
			// Unregistered contact_human keeps today's "tool not found" at
			// execution.
			if _, ok := a.tools.Get("contact_human"); !ok {
				continue
			}
			info := extractHITLInfo(call.Input)
			items = append(items, parkItem{call: call, seq: i, kind: "question", question: info.Question})
			continue
		}
		if d.Kind == shuttle.Ask {
			items = append(items, parkItem{call: call, seq: i, kind: "approval"})
		}
	}
	if len(items) == 0 {
		return nil
	}

	now := time.Now()
	params, truncated := buildParkParams(items)
	hr := &shuttle.HumanRequest{
		ID:              uuid.New().String(),
		AgentID:         a.id,
		SessionID:       sess.ID,
		Question:        fmt.Sprintf("Approve %d pending action(s)?", len(items)),
		Context:         map[string]interface{}{"kind": "parked"},
		RequestType:     "parked",
		Priority:        "normal",
		Kind:            "approval",
		Summary:         parkSummary(items),
		Timeout:         a.hitlPark.ttl,
		CreatedAt:       now,
		ExpiresAt:       now.Add(a.hitlPark.ttl),
		Params:          params,
		ParamsTruncated: truncated,
		Status:          "pending",
	}
	if err := a.hitlPark.store.Store(ctx, hr); err != nil {
		// A park that isn't durable must not pretend to park.
		return fmt.Errorf("parking turn: persisting human request: %w", err)
	}
	if a.hitlPark.notifier != nil {
		_ = a.hitlPark.notifier.Notify(ctx, hr)
	}
	return &TurnParkedError{
		RequestID: hr.ID,
		SessionID: sess.ID,
		ExpiresAt: hr.ExpiresAt,
		Usage:     llmResp.Usage,
	}
}

// parkParamsMaxBytes bounds the whole grouped-request params map. Per-item
// bodies are bounded at shuttle's per-call 8 KB first; items are never
// dropped — an over-budget item degrades to its display digest.
const parkParamsMaxBytes = 65536

// buildParkParams renders one descriptor per item, keyed by the item's
// ToolCall.ID — the batch↔request binding the resume validates.
func buildParkParams(items []parkItem) (map[string]interface{}, bool) {
	truncated := false
	out := make(map[string]interface{}, len(items))
	for _, it := range items {
		desc := map[string]interface{}{
			"seq":  it.seq,
			"kind": it.kind,
			"tool": it.call.Name,
		}
		bp, cut := shuttle.BoundParams(it.call.Input)
		if cut {
			truncated = true
			desc["params"] = shuttle.SummarizeCall(it.call.Name, it.call.Input)
			desc["params_truncated"] = true
		} else {
			desc["params"] = bp
		}
		if it.kind == "question" {
			desc["question"] = it.question
		}
		out[it.call.ID] = desc
	}
	if enc, err := json.Marshal(out); err == nil && len(enc) <= parkParamsMaxBytes {
		return out, truncated
	}
	// Whole-map overflow: degrade item bodies to digests in batch order until
	// the map fits. Items are never dropped — the card must show every action.
	for _, it := range items {
		desc, ok := out[it.call.ID].(map[string]interface{})
		if !ok {
			continue
		}
		if _, alreadyDigest := desc["params"].(string); alreadyDigest {
			continue
		}
		desc["params"] = shuttle.SummarizeCall(it.call.Name, it.call.Input)
		desc["params_truncated"] = true
		truncated = true
		if enc, err := json.Marshal(out); err == nil && len(enc) <= parkParamsMaxBytes {
			break
		}
	}
	return out, truncated
}

// parkSummary renders the ordered "tool: digest" list, bounded to the same
// 200-rune display cap the single-call resolver uses.
func parkSummary(items []parkItem) string {
	s := ""
	for i, it := range items {
		if i > 0 {
			s += "; "
		}
		s += shuttle.SummarizeCall(it.call.Name, it.call.Input)
	}
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200])
	}
	return s
}

// ResumeChat continues a parked turn: executes (or refuses) the parked batch
// under decision, then re-enters the conversation loop. It appends NO user
// message; the loop budget restarts (caps bound runaway model loops, not
// human patience). Response.ToolExecutions on a resumed turn covers the
// post-resume loop only — the parked batch's executions are authoritative in
// the persisted tool rows and tool-execution records.
func (a *Agent) ResumeChat(ctx context.Context, sessionID string, decision ParkDecision, progressCallback types.ProgressCallback) (*Response, error) {
	ctx = session.WithSessionID(ctx, sessionID)

	// Session-handle lifecycle: same ownership as chat() — handles minted
	// during the resumed turn are auto-released when it ends.
	ctx, handleCollector := mcpadapter.WithHandleCollector(ctx)
	defer func() {
		a.leases.apply(sessionID, handleCollector.ReleaseAll(zap.L()), nil)
	}()

	startTime := time.Now()
	ctx, span := a.tracer.StartSpan(ctx, "agent.conversation.resume")
	defer a.tracer.EndSpan(span)
	span.SetAttribute(observability.AttrSessionID, sessionID)
	span.SetAttribute("resume.request_id", decision.RequestID)
	span.SetAttribute("resume.approved", decision.Approved)

	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")
	a.seedLeaseHolding(ctx, sessionID)

	// NO DropTurnPayloads / dropInTurnSQLite — the parked turn is still the
	// current turn. NO user append — resuming is not a new turn. NO graph
	// extraction goroutine — there is no new user message to extract from.

	if progressCallback != nil {
		ctx = ContextWithProgressCallback(ctx, progressCallback)
	}
	agentCtx := &agentContext{
		Context:          ctx,
		session:          sess,
		tracer:           a.tracer,
		progressCallback: progressCallback,
	}

	batch, rowless, err := locateParkedBatch(sess, decision.ItemIDs)
	if err != nil {
		span.AddEvent("resume.refused", map[string]interface{}{"reason": err.Error()})
		return nil, err
	}

	if len(rowless) > 0 {
		a.completeParkedBatch(agentCtx, sess, batch, rowless, decision)
	}

	response, err := a.runConversationLoop(agentCtx)

	duration := time.Since(startTime)
	if err != nil {
		var parked *TurnParkedError
		if errors.As(err, &parked) {
			// A nested park is a clean exit, not a failure (same contract as
			// chat()).
			span.AddEvent("conversation.parked", map[string]interface{}{
				"request_id":  parked.RequestID,
				"duration_ms": duration.Milliseconds(),
			})
			if perr := a.memory.PersistSession(ctx, sess); perr != nil {
				zap.L().Warn("Failed to persist session at park",
					zap.String("session_id", sessionID), zap.Error(perr))
			}
			return nil, parked
		}
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		span.AddEvent("conversation.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})
		if progressCallback != nil {
			progressCallback(ProgressEvent{
				Stage:     StageFailed,
				Progress:  0,
				Message:   fmt.Sprintf("Execution failed: %v", err),
				Timestamp: time.Now(),
			})
		}
		return nil, fmt.Errorf("conversation loop failed: %w", err)
	}

	a.appendMessage(ctx, sess, Message{
		Role:       "assistant",
		Content:    response.Content,
		AgentID:    a.id,
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}, false)

	if err := a.memory.PersistSession(ctx, sess); err != nil {
		zap.L().Warn("Failed to persist session",
			zap.String("session_id", sessionID), zap.Error(err))
		span.RecordError(err)
	}

	span.SetAttribute("conversation.duration_ms", duration.Milliseconds())
	return response, nil
}

// rawSessionMessages returns the session's L1 rows in append order — the
// segmented store's raw view when configured, else the flat message list.
func rawSessionMessages(sess *Session) []Message {
	if sess.SegmentedMem != nil {
		if sm, ok := sess.SegmentedMem.(*SegmentedMemory); ok && sm != nil {
			return sm.RawMessages()
		}
	}
	return sess.GetMessages()
}

// locateParkedBatch finds the tail tool batch and validates the decision
// against it. The walk is turn-aware: the loop itself appends user-role rows
// WITHIN a turn (sidecar drain, empty-response nudge, hygiene fixup,
// synthesis prompt), all stamped with the current turn; only a user row of a
// LATER turn — the sole turn-incrementing append — means history moved on.
func locateParkedBatch(sess *Session, itemIDs []string) (Message, []ToolCall, error) {
	msgs := rawSessionMessages(sess)

	k := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			k = i
			break
		}
	}
	if k < 0 {
		return Message{}, nil, ErrNothingParked
	}
	parkedTurn := msgs[k].Turn

	seenRows := make(map[string]bool)
	finalReply := false
	for _, m := range msgs[k+1:] {
		switch m.Role {
		case "user":
			if m.Turn > parkedTurn {
				return Message{}, nil, ErrNotParkedTail
			}
			// Same-turn user rows are in-turn machinery — skip.
		case "tool":
			if m.ToolUseID != "" {
				seenRows[m.ToolUseID] = true
			}
		case "assistant":
			if len(m.ToolCalls) == 0 {
				finalReply = true
			}
		}
	}

	callIDs := make(map[string]bool, len(msgs[k].ToolCalls))
	for _, c := range msgs[k].ToolCalls {
		callIDs[c.ID] = true
	}
	for _, id := range itemIDs {
		if !callIDs[id] {
			return Message{}, nil, ErrStaleDecision
		}
	}

	var rowless []ToolCall
	for _, c := range msgs[k].ToolCalls {
		if !seenRows[c.ID] {
			rowless = append(rowless, c)
		}
	}
	if len(rowless) == 0 && finalReply {
		return Message{}, nil, ErrNothingParked
	}
	return msgs[k], rowless, nil
}

// completeParkedBatch finishes the parked batch's tool_use↔tool_result pairs
// in original batch order. Rejected: every rowless call gets a synthesized
// permission_denied carrying the human's reason — no tool bodies run,
// allow-classified calls included (block-all covers the refusal). Approved:
// question items synthesize the human's answer as the contact_human result;
// every other call executes through the normal dispatch ceremony under an
// AskGrant scoped to this batch only — asks resolve to Allow, hard denials
// still deny.
func (a *Agent) completeParkedBatch(ctx Context, sess *Session, batch Message, rowless []ToolCall, decision ParkDecision) {
	maxPerTurn := a.config.MaxIterations
	if maxPerTurn <= 0 {
		maxPerTurn = 10
	}
	resumeExecCount := 0
	var resumeExecs []ToolExecution
	tools := a.advertisedTools(sess)
	var recovery *recoveryOrchestrator
	_, span := ctx.Tracer().StartSpan(ctx, "agent.resume_batch")
	defer ctx.Tracer().EndSpan(span)
	if a.config.EnableSelfHealing {
		recovery = newRecoveryOrchestrator(a.config.RecoveryConfig, span)
	}
	st := &batchState{
		span:               span,
		turnCount:          0,
		batchLen:           len(batch.ToolCalls),
		maxPerTurn:         maxPerTurn,
		toolExecutionCount: &resumeExecCount,
		allToolExecutions:  &resumeExecs,
		tools:              &tools,
		recovery:           recovery,
		turnDedup:          make(map[string]*shuttle.Result),
	}

	seqOf := make(map[string]int, len(batch.ToolCalls))
	for i, c := range batch.ToolCalls {
		seqOf[c.ID] = i
	}

	// The grant lives only on this derived context — step 4's loop re-entry
	// runs on the original ctx, so a NEW ask in the continuation parks again
	// instead of inheriting the old approval.
	grantCtx := &agentContext{
		Context:          shuttle.ContextWithAskGrant(ctx, &shuttle.AskGrant{Approved: true}),
		session:          sess,
		tracer:           ctx.Tracer(),
		progressCallback: ctx.ProgressCallback(),
	}

	for _, call := range rowless {
		switch {
		case !decision.Approved:
			reason := decision.Reason
			if reason == "" {
				reason = "rejected by user"
			}
			a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
				Success: false,
				Error:   &shuttle.Error{Code: "permission_denied", Message: reason, Retryable: false},
			}, st)
		case a.isParkQuestion(ctx, call):
			answer, ok := decision.Answers[call.ID]
			if !ok || answer == "" {
				a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
					Success: false,
					Error:   &shuttle.Error{Code: "MISSING_ANSWER", Message: "no answer was provided for this question", Retryable: false},
				}, st)
				continue
			}
			a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
				Success: true,
				Data: map[string]interface{}{
					"response":     answer,
					"responded_by": "user",
					"status":       "responded",
				},
			}, st)
		default:
			a.dispatchOneCall(grantCtx, sess, call, seqOf[call.ID], st)
		}
	}

	// Drain buffered text_body sidecars AFTER every tool_result of the batch
	// is in place — the same pairing rule the loop's drain protects.
	for _, sidecar := range st.pendingSidecars {
		a.appendMessage(ctx, sess, sidecar, false)
	}
}

// synthesizeParkedResult appends a decision-synthesized result as the call's
// tool row with the normal persistence and progress ceremony, without running
// any tool body.
func (a *Agent) synthesizeParkedResult(ctx Context, sess *Session, call ToolCall, res *shuttle.Result, st *batchState) {
	execution := ToolExecution{
		ToolName: call.Name,
		Input:    call.Input,
		Result:   res,
	}
	*st.allToolExecutions = append(*st.allToolExecutions, execution)
	emitToolCompleted(ctx, 60, call, res, nil)
	if persistErr := a.memory.PersistToolExecution(ctx, sess.ID, execution); persistErr != nil {
		zap.L().Warn("Failed to persist synthesized tool execution",
			zap.String("session_id", sess.ID),
			zap.String("tool", call.Name),
			zap.Error(persistErr))
	}
	a.appendMessage(ctx, sess, Message{
		Role:       "tool",
		Content:    a.formatToolResult(ctx, sess.ID, call.Name, res, nil),
		ToolUseID:  call.ID,
		ToolResult: res,
		AgentID:    a.id,
		Timestamp:  time.Now(),
	}, false)
}
