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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	mcpadapter "github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/taskctx"
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

// Typed terminals for ResumeChat callers. All are terminal: the caller must
// finish the request's lifecycle and never retry the resume.
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
	// ErrParkDisabled: ResumeChat was called on an agent with no park wiring.
	// Completing a rowless tail batch under a grant would bypass whatever
	// approval path that agent DOES have, so the entry point is closed.
	ErrParkDisabled = errors.New("park is not enabled on this agent")
	// ErrUnknownRequest: no pending parked request with that ID owns this
	// session. Covers a missing row, a row from another session, and a
	// non-parked request type — all indistinguishable to a caller by design.
	ErrUnknownRequest = errors.New("no pending parked request for this session")
)

// ParkDecision is the human's verdict for one parked batch.
//
// RequestID is the binding: ResumeChat loads that row and takes the batch's
// item IDs from ITS params — a decision can only ever describe the batch its
// request describes. ItemIDs is an OPTIONAL cross-check; when non-empty it
// must name exactly the row's items or the resume is refused. It is never the
// authority, because an empty or partial list would otherwise widen the
// decision to calls the human never saw.
//
// Reason is used VERBATIM as the refusal text on every synthesized denial
// (the caller composes it: "rejected by user: …"). Answers maps question-item
// ToolCall.IDs to answer text.
type ParkDecision struct {
	RequestID string
	ItemIDs   []string
	Approved  bool
	Reason    string
	Answers   map[string]string
}

// loadParkedRequest resolves the decision's request to a parked row that
// belongs to this session. Absence and mismatch collapse to one error: a
// caller holding a wrong ID learns nothing about other sessions' requests.
// Both a nil request and an error mean possibly-absent (the store interface's
// documented contract — the postgres store returns (nil, nil) for a miss).
//
// Two row states resume. A PENDING row is the standalone flow: ResumeChat is
// the decision channel and closes the row after applying. A DECIDED row
// (approved/rejected/timeout) is the embedder-recorded flow: the embedder's
// respond door decided the row first — under its own expiry CAS — and the
// resume applies that recorded verdict. Double-application is not guarded
// here for either state; the tail walk is the guard (an applied batch has its
// tool rows, so a replay lands in ErrNothingParked/ErrStaleDecision).
func (a *Agent) loadParkedRequest(ctx context.Context, sessionID, requestID string) (hr *shuttle.HumanRequest, preDecided bool, err error) {
	if requestID == "" {
		return nil, false, ErrUnknownRequest
	}
	hr, gerr := a.hitlPark.store.Get(ctx, requestID)
	if gerr != nil || hr == nil {
		return nil, false, ErrUnknownRequest
	}
	if hr.SessionID != sessionID || hr.RequestType != "parked" {
		return nil, false, ErrUnknownRequest
	}
	switch hr.Status {
	case "pending":
		return hr, false, nil
	case "approved", "rejected", "timeout":
		return hr, true, nil
	default:
		// "responded" or anything else is not a park verdict.
		return nil, false, ErrUnknownRequest
	}
}

// parkedItemIDs returns the request's item IDs — its params keys, which
// buildParkParams writes one per park item, keyed by ToolCall.ID.
func parkedItemIDs(hr *shuttle.HumanRequest) []string {
	out := make([]string, 0, len(hr.Params))
	for k := range hr.Params {
		out = append(out, k)
	}
	return out
}

// verifyItemBinding checks that every request item still DESCRIBES the tail
// call it names: same tool, and same batch position when the descriptor
// carries one. The subset check upstream only proves the IDs exist in the
// batch — with per-response counter IDs two different batches can share IDs,
// and an old decision must never execute a new batch's calls by collision.
func verifyItemBinding(hr *shuttle.HumanRequest, batch Message) error {
	byID := make(map[string]int, len(batch.ToolCalls))
	for i, c := range batch.ToolCalls {
		byID[c.ID] = i
	}
	for id, raw := range hr.Params {
		desc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		idx, inBatch := byID[id]
		if !inBatch {
			continue // subset check upstream already refused if required
		}
		if tool, _ := desc["tool"].(string); tool != "" && tool != batch.ToolCalls[idx].Name {
			return ErrStaleDecision
		}
		switch seq := desc["seq"].(type) {
		case int:
			if seq != idx {
				return ErrStaleDecision
			}
		case float64: // JSON round-trip through a persistent store
			if int(seq) != idx {
				return ErrStaleDecision
			}
		}
	}
	return nil
}

// sameIDSet reports whether two id lists describe the same set.
func sameIDSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			return false
		}
	}
	return true
}

// closeParkedRequest terminally closes the row whose decision was just
// applied. This is what keeps a session usable: guardParkedTail refuses a new
// turn while a parked row is pending, so a decided batch whose row stays
// pending would wedge the session for every later message.
//
// An expired row cannot be resolved — the store's expiry guard is its own and
// no status payload lifts it — so it is closed through ExpireRequest, the one
// path allowed to close past expiry. Close failures are logged, not returned:
// the human's decision has already been applied to the batch, and failing the
// resume would strand the turn it just completed.
func (a *Agent) closeParkedRequest(ctx context.Context, hr *shuttle.HumanRequest, decision ParkDecision, expired bool) {
	var err error
	switch {
	case expired:
		err = a.hitlPark.store.ExpireRequest(ctx, hr.ID, "system:expiry")
	case decision.Approved:
		err = a.hitlPark.store.RespondToRequest(ctx, hr.ID, "approved", decision.Reason, "human", nil)
	default:
		err = a.hitlPark.store.RespondToRequest(ctx, hr.ID, "rejected", decision.Reason, "human", nil)
	}
	if err != nil {
		zap.L().Error("parked request could not be closed; the session will refuse new turns until it is",
			zap.String("session_id", hr.SessionID),
			zap.String("request_id", hr.ID),
			zap.Error(err))
	}
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

// resumedTurnKey marks a context as continuing a turn that already ran the
// loop's entry work once, before it parked.
type resumedTurnKey struct{}

// contextWithResumedTurn marks ctx as a resume. Per-TURN entry work in
// runConversationLoop — graph-memory context injection — must not run twice
// for one turn just because the loop is entered twice.
func contextWithResumedTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, resumedTurnKey{}, true)
}

// isResumedTurn reports whether this loop entry is continuing a parked turn.
func isResumedTurn(ctx context.Context) bool {
	v, _ := ctx.Value(resumedTurnKey{}).(bool)
	return v
}

// parkHandles hands the unfinished turn's session handles to whoever resumes
// it in this process. A parked turn spans two Go calls, and releasing at the
// park would kill handles the SAME turn is still going to use after the
// human decides. One slot per session — guardParkedTail admits one parked
// turn at a time; a re-park by a resume replaces its own adopted collector.
func (a *Agent) parkHandles(sessionID string, c *mcpadapter.HandleCollector) {
	a.parkedHandlesMu.Lock()
	defer a.parkedHandlesMu.Unlock()
	if a.parkedHandles == nil {
		a.parkedHandles = make(map[string]*mcpadapter.HandleCollector)
	}
	a.parkedHandles[sessionID] = c
}

// adoptParkedHandles installs the parked turn's collector on ctx when one is
// waiting, so handles minted before the park stay live through the resume and
// are released once, at the end of the turn that actually finishes. With none
// waiting it plants a fresh collector — a resume in a fresh Agent (pooled
// embedder) or of a turn that minted no handles behaves exactly like chat().
func (a *Agent) adoptParkedHandles(ctx context.Context, sessionID string) (context.Context, *mcpadapter.HandleCollector) {
	a.parkedHandlesMu.Lock()
	c := a.parkedHandles[sessionID]
	delete(a.parkedHandles, sessionID)
	a.parkedHandlesMu.Unlock()

	if c == nil {
		return mcpadapter.WithHandleCollector(ctx)
	}
	return mcpadapter.ContextWithHandleCollector(ctx, c), c
}

// ReleaseParkedHandles is the pooled-embedder seam: an embedder whose Agent
// instances do not outlive the call adopts nothing at resume, so a parked
// collector would be unreachable and its handles would leak. Such an embedder
// calls this at each park terminal to keep handles call-scoped (the resumed
// turn re-mints on demand). Same-process embedders never call it and keep
// handle continuity across the gap. No-op when nothing is parked.
func (a *Agent) ReleaseParkedHandles(sessionID string) {
	a.parkedHandlesMu.Lock()
	c := a.parkedHandles[sessionID]
	delete(a.parkedHandles, sessionID)
	a.parkedHandlesMu.Unlock()
	if c != nil {
		a.leases.apply(sessionID, c.ReleaseAll(zap.L()), nil)
	}
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

	// Record this turn's task NOW, on the HUMAN_REQUEST trigger.
	//
	// A turn whose first action needs a human decision returns from here before
	// dispatchOneCall ever runs, so its TOOL_CALL trigger never fires and the
	// turn recorded nothing at all — no task, and therefore no timeline for the
	// one turn shape a human is most likely to go looking for, because they were
	// the one asked to decide something in it. dispatchOneCall's TOOL_CALL
	// comment already assumes this trigger fires here; it simply never did.
	//
	// Before the Store call, not after: the park row is written under the turn's
	// ambient attribution, so the human request the person sees hangs off the
	// same task as the work around it. A store failure below fails the turn with
	// a task already recorded, which is accurate — the turn ended, and it ended
	// badly.
	//
	// A no-op when the turn already recorded a task (work-then-park): the
	// binding is filled and every trigger after the first short circuits.
	a.maybeRecordImplicitTask(ctx, loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST)

	now := time.Now()
	params, truncated := buildParkParams(items)
	kind, question := parkKindAndQuestion(items)
	hr := &shuttle.HumanRequest{
		ID:              uuid.New().String(),
		AgentID:         a.id,
		SessionID:       sess.ID,
		Question:        question,
		Context:         map[string]interface{}{"kind": "parked"},
		RequestType:     "parked",
		Priority:        "normal",
		Kind:            kind,
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

// parkKindAndQuestion renders the card's Kind and headline. A card renderer
// keys its shape off Kind, so a park whose items are ALL questions must not
// arrive as an approval prompt — the human would be asked to approve/reject
// where the model asked them something. A single question card carries the
// model's own question verbatim; anything mixed is an approval over the batch.
func parkKindAndQuestion(items []parkItem) (kind, question string) {
	questions := 0
	for _, it := range items {
		if it.kind == "question" {
			questions++
		}
	}
	if questions == len(items) {
		if len(items) == 1 && items[0].question != "" {
			return "question", items[0].question
		}
		return "question", fmt.Sprintf("Answer %d pending question(s)", len(items))
	}
	return "approval", fmt.Sprintf("Approve %d pending action(s)?", len(items))
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
	if a.hitlPark == nil {
		return nil, ErrParkDisabled
	}
	ctx = session.WithSessionID(ctx, sessionID)

	// Session-handle lifecycle: the resumed turn ADOPTS the handles its parked
	// half minted (same-process embedders), so a handle the model is about to
	// use is still live. A nested park re-parks them; any other exit releases
	// them, closing out the whole turn's handles exactly once. Pooled
	// embedders adopt nothing (fresh Agent) and drain each park's slot via
	// ReleaseParkedHandles instead.
	ctx, handleCollector := a.adoptParkedHandles(ctx, sessionID)
	parkedAgain := false
	defer func() {
		if parkedAgain {
			return
		}
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
	ctx = contextWithResumedTurn(ctx)

	// Re-establish the parked turn's task identity.
	//
	// A resume built its context with no binding, no turn index and no user
	// message, so everything written after the human's decision — the approved
	// tool's own row above all — landed with a NULL task_id. The row that fell
	// out of the record was the one a person had explicitly authorised, which is
	// the opposite of the ordering a timeline should have.
	//
	// The binding starts empty and is filled below, once the batch is known to
	// be resumable. Turn index and user message come from the parked tail, and
	// they matter beyond decoration: the turn index is half the emitter's
	// idempotency key, so recovering the SAME index is what makes the resume
	// rebind to the task the parked half recorded instead of describing a
	// different turn.
	turnIndex, turnUserMessage := parkedTurnIdentity(sess)
	ctx, taskBinding := taskctx.ContextWithBinding(ctx)
	agentCtx := &agentContext{
		Context:          ctx,
		session:          sess,
		tracer:           a.tracer,
		progressCallback: progressCallback,
		taskBinding:      taskBinding,
		turnIndex:        turnIndex,
		userMessage:      turnUserMessage,
	}

	// The request row — not the caller's payload — is the batch binding. Its
	// params keys ARE the items the human saw; a caller-supplied ItemIDs list
	// is honored only as a cross-check.
	hr, preDecided, err := a.loadParkedRequest(ctx, sessionID, decision.RequestID)
	if err != nil {
		span.AddEvent("resume.refused", map[string]interface{}{"reason": err.Error()})
		return nil, err
	}
	itemIDs := parkedItemIDs(hr)
	if len(decision.ItemIDs) > 0 && !sameIDSet(decision.ItemIDs, itemIDs) {
		span.AddEvent("resume.refused", map[string]interface{}{"reason": ErrStaleDecision.Error()})
		return nil, ErrStaleDecision
	}

	effective := decision
	expired := false
	if preDecided {
		// Embedder-recorded flow: the row IS the decision record — its status
		// overrides the caller's payload, so a mismatched payload can never
		// execute against a rejected row. Expiry was already judged by the
		// respond door's decide-time CAS; a decision recorded in time is not
		// re-judged at apply time, however much later the resume runs.
		effective.Approved = hr.Status == "approved"
		if !effective.Approved && effective.Reason == "" {
			if hr.Status == "timeout" {
				effective.Reason = "approval timed out"
			} else {
				effective.Reason = "rejected by user"
			}
		}
	} else {
		// Standalone flow: the decision arrives NOW, so apply time is decide
		// time — one that arrives past the row's expiry is applied as a
		// refusal whatever it says: the window the human was granted has
		// closed, and an approval must never execute on a lapsed
		// authorization.
		expired = !hr.ExpiresAt.IsZero() && time.Now().After(hr.ExpiresAt)
		if expired {
			effective.Approved = false
			if effective.Reason == "" {
				effective.Reason = "approval timed out"
			}
		}
	}
	span.SetAttribute("resume.expired", expired)
	span.SetAttribute("resume.pre_decided", preDecided)

	batch, rowless, err := locateParkedBatch(sess, itemIDs)
	if err != nil {
		span.AddEvent("resume.refused", map[string]interface{}{"reason": err.Error()})
		return nil, err
	}
	// Content binding, not ID binding alone: LLM-assigned call IDs can
	// collide across batches (per-response counters), so an old request's
	// item could name a NEW batch's call by accident. Each item's recorded
	// tool (and seq, when present) must match the tail call it names, or the
	// decision belongs to a different batch.
	if bindErr := verifyItemBinding(hr, batch); bindErr != nil {
		span.AddEvent("resume.refused", map[string]interface{}{"reason": bindErr.Error()})
		return nil, bindErr
	}

	// Fill the turn's binding before anything the decision produces is written.
	//
	// HUMAN_REQUEST is the honest trigger — a human decision is what resumed
	// this turn — and it is the same trigger the park fired, so the emitter's
	// key resolves to the task the parked half recorded: in-process through the
	// per-turn memo the park deliberately did not release, and after a restart
	// through CreateTaskIdempotent, which looks the key up before it creates.
	// Either way this binds rather than mints, so the rows on both sides of the
	// gap carry one task id.
	//
	// After the guards, not before them: a resume that is about to be refused
	// (stale decision, moved-on history) must not leave a task behind for a turn
	// it never continued.
	a.maybeRecordImplicitTask(agentCtx, loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_HUMAN_REQUEST)

	if len(rowless) > 0 {
		a.completeParkedBatch(agentCtx, sess, batch, rowless, effective, itemIDs)
	}

	// Close the row the moment its decision has been applied — before the
	// loop re-entry that may park a NEW request. A row left pending here
	// wedges every later turn at guardParkedTail. A pre-decided row is
	// already closed (the embedder's respond door decided it).
	if !preDecided {
		a.closeParkedRequest(ctx, hr, effective, expired)
	}

	response, err := a.runConversationLoop(agentCtx)

	// The mirror of chat()'s deferred close, which a park skips: this is where
	// the turn finally ends, so this is where its recorded task closes and its
	// per-turn memo is released.
	//
	// A NESTED park is skipped for exactly the reason the first one was — the
	// turn is still unfinished, and the next resume rebinds through the memo.
	var parkedTerminal *TurnParkedError
	if !errors.As(err, &parkedTerminal) {
		defer a.completeImplicitTask(ctx, taskBinding, sessionID, int(turnIndex), implicitCloseReason(response, err))
	}

	duration := time.Since(startTime)
	if err != nil {
		var parked *TurnParkedError
		if errors.As(err, &parked) {
			// A nested park is a clean exit, not a failure (same contract as
			// chat()). The turn is still unfinished — its handles park with
			// it rather than being released out from under the next resume.
			parkedAgain = true
			a.parkHandles(sessionID, handleCollector)
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
	span.Status = observability.Status{Code: observability.StatusOK}
	a.recordConversationMetrics(sessionID, response, duration)
	return response, nil
}

// rawSessionMessages returns the session's L1 rows in append order.
//
// It reaches past types.Session.GetMessages on purpose: that one delegates to
// GetMessagesForLLM when segmented memory is configured, which RENDERS the
// context window (folding, pair synthesis). The tail walk must see the rows
// as appended — a synthesized pair would read as a tool row that no call
// actually produced. SegmentedMemory's own GetMessages is that raw view.
func rawSessionMessages(sess *Session) []Message {
	if sess.SegmentedMem != nil {
		if sm, ok := sess.SegmentedMem.(*SegmentedMemory); ok && sm != nil {
			return sm.GetMessages()
		}
	}
	return sess.GetMessages()
}

// parkedTurnIdentity recovers the turn number and opening user message of the
// turn that parked, so a resume can describe the same turn its parked half did.
//
// The turn number comes from the tail assistant batch — the same row
// locateParkedBatch anchors on, walked the same way, so the two cannot disagree
// about which turn is being resumed.
//
// The user message is the FIRST user row of that turn, not the last. The loop
// appends further user-role rows WITHIN a turn (sidecar drain, empty-response
// nudge, hygiene fixup, synthesis prompt), all stamped with the same turn; the
// request that opened the turn is the one a board row should be named after.
//
// A session with no assistant batch yields turn 0 and no message. Nothing acts
// on that: the callers' guards refuse such a resume before the identity is used.
func parkedTurnIdentity(sess *Session) (turnIndex int64, userMessage string) {
	msgs := rawSessionMessages(sess)

	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			turnIndex = msgs[i].Turn
			break
		}
	}
	for _, m := range msgs {
		if m.Role == "user" && m.Turn == turnIndex {
			return turnIndex, m.Content
		}
	}
	return turnIndex, ""
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

	// A final assistant reply means the turn ALREADY ended and the user has
	// its answer — whether or not every call left a row. The loop can finish a
	// turn with rowless calls (the MaxToolExecutions break stops dispatching
	// without appending rows), and those calls were deliberately skipped:
	// executing them now, under a grant, would run tool bodies the finished
	// turn declined to run.
	if finalReply {
		return Message{}, nil, ErrNothingParked
	}

	var rowless []ToolCall
	for _, c := range msgs[k].ToolCalls {
		if !seenRows[c.ID] {
			rowless = append(rowless, c)
		}
	}
	// Every row present but NO final reply: the batch was applied and the
	// turn died before its continuation (LLM outage, crash). Nothing to
	// complete — but this is not "already complete": the loop must re-enter
	// so the model sees the results and produces the turn's answer. Refusing
	// here would strand the turn's answer permanently.
	return msgs[k], rowless, nil
}

// completeParkedBatch finishes the parked batch's tool_use↔tool_result pairs
// in original batch order. Rejected: every rowless call gets a synthesized
// permission_denied carrying the human's reason — no tool bodies run,
// allow-classified calls included (block-all covers the refusal). Approved:
// question items synthesize the human's answer as the contact_human result;
// every other call executes through the normal dispatch ceremony.
//
// itemIDs are the calls the human's card actually described. ONLY those run
// under the AskGrant: an approval answers the question it was asked, so a
// call that was not on the card cannot borrow it. Calls outside the set
// dispatch ungranted, so an ask that appeared while the turn waited — a host
// gate that trips on budget, quota, or time of day — parks or fails closed
// exactly as it would in a fresh turn instead of being silently admitted.
func (a *Agent) completeParkedBatch(ctx Context, sess *Session, batch Message, rowless []ToolCall, decision ParkDecision, itemIDs []string) {
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
	// The grant context is a DERIVED context, so it must not lose the turn's
	// task identity on the way. The ambient attribution rides the embedded
	// context.Context and would survive on its own, but the per-turn fields live
	// on the concrete agentContext: without them the approved call's own
	// TOOL_CALL trigger sees no binding and declines, which matters in the one
	// configuration where the HUMAN_REQUEST rebind above did not fire (an
	// operator who excluded that trigger but kept TOOL_CALL).
	if tc, ok := ctx.(taskTurnContext); ok {
		grantCtx.taskBinding = tc.TaskBinding()
		grantCtx.turnIndex = tc.TurnIndex()
		grantCtx.userMessage = tc.UserMessage()
	}
	granted := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		granted[id] = true
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
		case granted[call.ID]:
			a.dispatchOneCall(grantCtx, sess, call, seqOf[call.ID], st)
		default:
			a.dispatchOneCall(ctx, sess, call, seqOf[call.ID], st)
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
