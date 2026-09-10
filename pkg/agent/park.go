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
	"sync"
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

// Typed terminals for ResumeChat callers. These are terminal: the caller must
// finish the request's lifecycle and never retry the resume.
// ErrClaimNotConfirmed, declared below, is the exception — it is retryable.
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
	// ErrDecisionExpired: the row lapsed before the decision could be claimed.
	// Nothing ran, and the row is now closed as "timeout" — which the next
	// resume reads as an embedder-recorded refusal, so retrying DOES finish the
	// turn with refusal rows. Without a retry the batch stays rowless.
	ErrDecisionExpired = errors.New("parked request expired before the decision was applied")
)

// ErrClaimNotConfirmed: the decision could not be claimed and could not be
// confirmed as claimed by anyone else. RETRYABLE, unlike the terminals above —
// the row may still be pending, and nothing has been executed.
var ErrClaimNotConfirmed = errors.New("parked decision claim could not be confirmed")

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

	// Results maps resource-item ToolCall.IDs to the payload injected verbatim
	// as that call's successful result Data on an approved resume — the
	// embedder composes it from the awaited resource's terminal content
	// (resource-await park, park_resource.go). Ignored for approval/question
	// items. Like Answers, it rides the caller's payload in both flows; the
	// row's status still decides Approved.
	Results map[string]interface{}
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
// resume applies that recorded verdict.
//
// The standalone flow CLAIMS the row before applying anything, so it is
// guarded against double application across agents and processes
// (claimParkedRequest). The embedder-recorded flow has no row left to claim,
// so its only guards are the per-Agent session lock and the tail walk (an
// applied batch has its tool rows, so a later replay lands in
// ErrNothingParked/ErrStaleDecision) — neither of which covers two Agent
// instances racing. That is the embedder's obligation.
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
	case "responded":
		// The documented status for an input/decision request, and the natural
		// one for a QUESTION park — the human answered, which is that card's
		// whole verdict. Refusing it stranded the turn: the row is no longer
		// pending, so the tail guard used to admit a new turn that buried the
		// batch. On an APPROVAL card "responded" says nothing about approval,
		// so it stays refused rather than being read as consent.
		if hr.Kind == "question" {
			return hr, true, nil
		}
		return nil, false, ErrUnknownRequest
	default:
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
		// The ARGUMENTS the human saw must be the arguments about to run.
		// Tool and seq alone can carry no information at all: on Gemini
		// ToolCall.ID IS the function name (pkg/llm/gemini/client.go —
		// "Gemini doesn't provide call IDs"), so for a single-call batch id,
		// tool and seq collapse into one fact and every same-tool batch looks
		// identical. Without this a spent decision — which still loads, since
		// the embedder-recorded flow accepts a decided row — re-binds to a
		// LATER batch of the same tool and executes arguments nobody approved.
		if !sameParkedParams(desc, batch.ToolCalls[idx]) {
			return ErrStaleDecision
		}
	}
	return nil
}

// sameParkedParams reports whether a request item's recorded params still
// describe the call it names. The descriptor holds either the bounded params
// map or, when that overflowed, the display digest — compare like with like.
// A descriptor carrying neither cannot vouch for anything and is refused.
func sameParkedParams(desc map[string]interface{}, call ToolCall) bool {
	switch recorded := desc["params"].(type) {
	case string:
		// Digest form (params_truncated): compare the same rendering.
		return recorded == shuttle.SummarizeCall(call.Name, call.Input)
	case map[string]interface{}:
		bounded, _ := shuttle.BoundParams(call.Input)
		a, aerr := json.Marshal(recorded)
		b, berr := json.Marshal(bounded)
		return aerr == nil && berr == nil && string(a) == string(b)
	default:
		return false
	}
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

// resumeClaimKey is the response-data field carrying the claim token — the
// only thing that identifies WHICH resume closed the row.
const resumeClaimKey = "resume_claim"

// claimParkedRequest takes exclusive ownership of a still-pending decision
// before any of its batch executes, and reports whether this resume is the
// owner. Standalone flow only: a pre-decided row is already closed and has
// nothing left to claim.
func (a *Agent) claimParkedRequest(ctx context.Context, hr *shuttle.HumanRequest, decision ParkDecision, expired bool) error {
	if expired {
		// RespondToRequest cannot close a lapsed row — the store's expiry
		// guard is its own and no status payload lifts it — and ExpireRequest
		// carries no response data to stamp a claim with. Nothing executes on
		// this path (every call is refused), so the close only has to happen.
		if err := a.hitlPark.store.ExpireRequest(ctx, hr.ID, "system:expiry"); err != nil {
			// Nothing ran and the row may still be pending: retryable.
			return fmt.Errorf("closing lapsed parked request: %w: %v", ErrClaimNotConfirmed, err)
		}
		// ExpireRequest is a documented no-op returning nil on a row that is no
		// longer pending, and carries no response data to stamp a token into,
		// so this path CANNOT tell whether it won the close. Nothing executes
		// here — every call is refused — so two claimants cost duplicate
		// synthesized refusal rows, not duplicate side effects. The session
		// lock covers one Agent; a pooled embedder owns the rest.
		return nil
	}

	token := uuid.New().String()
	status := "rejected"
	if decision.Approved {
		status = "approved"
	}
	if err := a.hitlPark.store.RespondToRequest(ctx, hr.ID, status, decision.Reason, "human",
		map[string]interface{}{resumeClaimKey: token}); err != nil {
		// Nothing ran and the row may still be pending: retryable.
		return fmt.Errorf("claiming parked decision: %w: %v", ErrClaimNotConfirmed, err)
	}

	// Judge the write by reading the row back — never by its error, which is
	// nil both when the write landed and when the store refused it as a
	// deliberate no-op (already decided, or expired since we looked).
	after, gerr := a.hitlPark.store.Get(ctx, hr.ID)
	if gerr != nil || after == nil {
		return fmt.Errorf("verifying parked decision claim: %w", errors.Join(gerr, ErrClaimNotConfirmed))
	}

	if after.Status == "pending" {
		// Refused while still pending means the row lapsed between our expiry
		// check and this write. Close it the one way that works past expiry,
		// so the session is not left holding a decided-but-open row.
		if eerr := a.hitlPark.store.ExpireRequest(ctx, hr.ID, "system:expiry"); eerr != nil {
			zap.L().Error("parked request could not be closed; the session will refuse new turns until it is",
				zap.String("session_id", hr.SessionID),
				zap.String("request_id", hr.ID),
				zap.Error(eerr))
			return ErrClaimNotConfirmed
		}
		return ErrDecisionExpired
	}

	// "No longer pending" does NOT mean we are the one who closed it. Under
	// concurrent claims the store's conditional write admits exactly one and
	// silently no-ops the rest, and every loser then reads back a non-pending
	// row. Only the token says who won.
	if got, _ := after.ResponseData[resumeClaimKey].(string); got != token {
		return ErrUnknownRequest
	}
	return nil
}

// abandonParkedRequest terminally closes a row whose TURN can no longer be
// resumed. Without it the row stays pending and guardParkedTail refuses every
// later message on that session.
func (a *Agent) abandonParkedRequest(ctx context.Context, hr *shuttle.HumanRequest, cause error) {
	if err := a.hitlPark.store.ExpireRequest(ctx, hr.ID, "system:unresumable"); err != nil {
		zap.L().Error("unresumable parked request could not be closed; the session will refuse new turns until it is",
			zap.String("session_id", hr.SessionID),
			zap.String("request_id", hr.ID),
			zap.NamedError("cause", cause),
			zap.Error(err))
		return
	}
	// The turn is dead, so its parked handles will never be adopted. Give them
	// back or the collector sits in parkedHandles until the process ends — and
	// because ReleaseAll feeds the lease ledger, an orphaned collector leaves
	// the session marked a lease holder, pinning every LATER turn on it to the
	// RESOURCE_HOLDER scheduling class.
	a.ReleaseParkedHandles(hr.SessionID)

	zap.L().Warn("closed a parked request whose turn can no longer be resumed",
		zap.String("session_id", hr.SessionID),
		zap.String("request_id", hr.ID),
		zap.NamedError("cause", cause))
}

// lockSession serializes resumes of one session within this process, and
// returns the unlock func. One mutex per live session; not pruned, because a
// mutex is one pointer and sessions are bounded by the host's own lifetime.
func (a *Agent) lockSession(sessionID string) func() {
	a.sessionLocksMu.Lock()
	if a.sessionLocks == nil {
		a.sessionLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := a.sessionLocks[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		a.sessionLocks[sessionID] = mu
	}
	a.sessionLocksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// guardParkedTail refuses a new user turn while the session holds a PENDING
// parked request. Store errors fail open with a warn — the embedder's own
// admission probe is the primary gate; this guard closes its race with a park
// landing mid-turn, and must not turn a store hiccup into a dead session.
func (a *Agent) guardParkedTail(ctx context.Context, sessionID string, sess *Session) error {
	if a.hitlPark == nil {
		return nil
	}
	reqs, err := a.hitlPark.store.ListBySession(ctx, sessionID)
	if err != nil {
		zap.L().Warn("parked-tail guard: listing session requests failed; admitting turn",
			zap.String("session_id", sessionID), zap.Error(err))
		return nil
	}
	now := time.Now()
	for _, r := range reqs {
		if r == nil || r.RequestType != "parked" || r.Status != "pending" {
			continue
		}
		// A LAPSED row no longer holds the session. Nothing sweeps parked rows
		// — a park has no waiter by design, and the only ExpireRequest callers
		// are the in-turn waiters and the operator CLI — so matching on
		// "pending" alone lets a park nobody ever decided refuse every future
		// turn on this session, permanently.
		if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			continue
		}
		return &SessionParkedError{RequestID: r.ID, SessionID: sessionID, ExpiresAt: r.ExpiresAt}
	}

	// A DECIDED row does not hold the session by status — but the
	// embedder-recorded flow decides the row BEFORE calling ResumeChat, so
	// between those two moments the batch is still unapplied and matching on
	// "pending" alone would admit a new user turn that buries it. The decision
	// would then be silently discarded and the history would tell the model
	// the call never completed. Ask the TAIL instead: an assistant batch still
	// missing tool rows is an unapplied park, whatever the row says.
	if hr := unappliedParkedTail(sess); hr != nil {
		for _, r := range reqs {
			if r == nil || r.RequestType != "parked" || r.Status == "pending" {
				continue
			}
			// Only a VERDICT awaiting application holds the session. "timeout"
			// is a closure, not a verdict — it is what `looms hitl expire`
			// writes to retire a stranded park, and what an expiry sweep
			// writes. Holding on it would make the operator's one recovery
			// route do nothing, which is worse than the burial this guard
			// exists to prevent: nobody is coming to apply it.
			if r.Status != "approved" && r.Status != "rejected" && r.Status != "responded" {
				continue
			}
			if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
				continue
			}
			return &SessionParkedError{RequestID: r.ID, SessionID: sessionID, ExpiresAt: r.ExpiresAt}
		}
	}
	return nil
}

// unappliedParkedTail reports the tail assistant batch when it still has calls
// with no tool row — the durable shape a park leaves behind. Returns nil when
// the tail is complete or there is no batch. Read-only.
func unappliedParkedTail(sess *Session) *Message {
	if sess == nil {
		return nil
	}
	batch, rowless, err := locateParkedBatch(sess, nil)
	if err != nil || len(rowless) == 0 {
		return nil
	}
	return &batch
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
	kind     string // "approval" | "question" | "resource"
	question string
	uri      string // resource items only: the awaited resource
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
	// The card is keyed by ToolCall.ID, so an empty or duplicated ID makes the
	// binding unrepresentable: buildParkParams would collapse two calls into
	// ONE descriptor (the human approves one action, two run), and an empty ID
	// can never be matched back to its call. Providers really do produce both
	// — Ollama can omit the id, and Gemini parallel calls reuse the function
	// name as the id. Refuse to park; the batch then dispatches inline, where
	// an Ask with no resolver fails closed.
	if err := checkParkItemIDs(items); err != nil {
		zap.L().Warn("not parking a batch whose call IDs cannot bind a decision; dispatching inline",
			zap.String("session_id", sess.ID), zap.Error(err))
		return nil
	}

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
	resources := 0
	for _, it := range items {
		if it.kind == "question" {
			questions++
		}
		if it.kind == "resource" {
			resources++
		}
	}
	// A resource card asks the human for nothing — it reports a wait. Only an
	// all-resource batch renders as one (the pre-scan and the post-dispatch
	// hold can never mix kinds in one row today; defensive all-or-approval).
	if resources == len(items) {
		return "resource", fmt.Sprintf("Waiting for %d background operation(s) to complete", len(items))
	}
	if questions == len(items) {
		if len(items) == 1 && items[0].question != "" {
			return "question", items[0].question
		}
		return "question", fmt.Sprintf("Answer %d pending question(s)", len(items))
	}
	return "approval", fmt.Sprintf("Approve %d pending action(s)?", len(items))
}

// checkParkItemIDs rejects a batch whose park items cannot be named uniquely.
func checkParkItemIDs(items []parkItem) error {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.call.ID == "" {
			return fmt.Errorf("tool call %q has no ID", it.call.Name)
		}
		if seen[it.call.ID] {
			return fmt.Errorf("tool call ID %q appears more than once", it.call.ID)
		}
		seen[it.call.ID] = true
	}
	return nil
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
		if it.kind == "resource" {
			desc["uri"] = it.uri
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

	// One resume at a time per session, PER AGENT INSTANCE. The tail walk
	// cannot be the double-application guard on its own: it reads the batch's
	// rowless calls BEFORE any of them execute, so two overlapping resumes of
	// one decision both see work to do and both do it.
	//
	// Scope matters here, and this lock is the weaker half. sessionLocks lives
	// on the Agent, so two Agents share nothing — and a pooled embedder builds
	// a fresh Agent per call (the pattern ReleaseParkedHandles exists for).
	// The standalone flow does not depend on it: claimParkedRequest below takes
	// the ROW, which holds across agents and processes. The embedder-recorded
	// flow has no row left to claim, so this lock is all it gets — meaning an
	// embedder that pools agents, or spreads resumes across processes, must
	// deliver an already-decided resume at most once itself.
	unlock := a.lockSession(sessionID)
	defer unlock()

	startTime := time.Now()
	ctx, span := a.tracer.StartSpan(ctx, "agent.conversation.resume")
	defer a.tracer.EndSpan(span)
	span.SetAttribute(observability.AttrSessionID, sessionID)
	span.SetAttribute("resume.request_id", decision.RequestID)
	span.SetAttribute("resume.approved", decision.Approved)

	// NO DropTurnPayloads / dropInTurnSQLite — the parked turn is still the
	// current turn. NO user append — resuming is not a new turn. NO graph
	// extraction goroutine — there is no new user message to extract from.

	if progressCallback != nil {
		ctx = ContextWithProgressCallback(ctx, progressCallback)
	}
	ctx = contextWithResumedTurn(ctx)

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

	// The session is loaded only once the request has been validated: a bogus
	// or foreign RequestID must not create and persist a session row.
	sess := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")

	batch, rowless, err := locateParkedBatch(sess, itemIDs)
	if err != nil {
		// Close the row ONLY on positive evidence that the turn is dead:
		// ErrNotParkedTail means a later turn's user row is physically present
		// after the batch, so the decision can never be applied.
		//
		// ErrNothingParked and ErrStaleDecision are absence of evidence, not
		// evidence of death, and closing on them destroys a valid approval. An
		// empty session yields ErrNothingParked, and an empty session is
		// reachable with the park perfectly intact:
		// GetOrCreateSessionWithAgent fails OPEN — on a load error it builds a
		// fresh empty session under the same ID — and LoadSession is
		// user-scoped (`WHERE id = ? AND user_id = ?`), so a transient store
		// error, or a resume whose ctx carries a different identity than the
		// session's owner, both land here. Expiring the row there erases the
		// human's decision record for good.
		//
		// Leaving a row pending is recoverable: guardParkedTail ignores lapsed
		// rows, so it stops holding the session at its TTL, and an operator can
		// close it with `looms hitl expire`. Erasing an approval is not.
		if !preDecided && errors.Is(err, ErrNotParkedTail) {
			a.abandonParkedRequest(ctx, hr, err)
		}
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

	// CLAIM the decision BEFORE executing anything, on the standalone flow
	// where the row is still pending and can therefore serve as the claim.
	// Closing afterwards is too late twice over: two resumes both get past
	// the tail walk and both execute, and a close that the store refuses —
	// because the row lapsed while the batch ran — returns nil, leaving the
	// row pending and the session refused at guardParkedTail forever.
	//
	// Claiming first also makes a parked batch at-most-once: a crash mid-batch
	// leaves the row closed and the remainder unrun, the safe direction for
	// actions a human gated. A pre-decided row is already closed by the
	// embedder's respond door; the session lock is its only guard.
	if !preDecided {
		if err := a.claimParkedRequest(ctx, hr, effective, expired); err != nil {
			span.AddEvent("resume.refused", map[string]interface{}{"reason": err.Error()})
			return nil, err
		}
	}

	// Past this line the decision is ours and the turn is ours to finish.
	// Session-handle lifecycle: adopt the handles the parked half minted
	// (same-process embedders), so a handle the model is about to use is still
	// live. Adoption happens HERE, not at entry: it removes the collector from
	// the parked slot, so adopting before the decision is validated would let
	// a refused resume release the handles of a turn that is still parked.
	// A nested park re-parks them; any other exit releases them, closing out
	// the whole turn's handles exactly once. Pooled embedders adopt nothing
	// (fresh Agent) and drain each park's slot via ReleaseParkedHandles.
	ctx, handleCollector := a.adoptParkedHandles(ctx, sessionID)
	parkedAgain := false
	defer func() {
		if parkedAgain {
			return
		}
		a.leases.apply(sessionID, handleCollector.ReleaseAll(zap.L()), nil)
	}()
	a.seedLeaseHolding(ctx, sessionID)

	// Built HERE so it can only ever carry the collector-bearing ctx —
	// constructing it earlier and re-pointing .Context afterwards works only
	// as long as nothing in between reads it, which is not a property the next
	// person moving code through this function should have to preserve.
	agentCtx := &agentContext{
		Context:          ctx,
		session:          sess,
		tracer:           a.tracer,
		progressCallback: progressCallback,
	}

	if len(rowless) > 0 {
		a.completeParkedBatch(agentCtx, sess, batch, rowless, effective, itemIDs, parkedItemKinds(hr))
	}

	response, err := a.runConversationLoop(agentCtx)

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
func (a *Agent) completeParkedBatch(ctx Context, sess *Session, batch Message, rowless []ToolCall, decision ParkDecision, itemIDs []string, kinds map[string]string) {
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
	granted := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		granted[id] = true
	}

	for _, call := range rowless {
		switch {
		case kinds[call.ID] == "resource":
			// The call's tool body already RAN when the turn parked — every
			// resource arm synthesizes, none dispatches, whatever the decision
			// payload claims (re-execution would double a job start).
			a.completeResourceItem(ctx, sess, call, decision, st)
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
	// A deny must ride every deny, audited or not (shuttle.AdmissionResult.
	// PersistedDecision). These rows never pass through Executor.Execute, so
	// nothing else stamps them — and a NULL verdict here is miscounted by the
	// analytics fallback that keys on admission_decision IS NULL.
	if res != nil && !res.Success && res.Error != nil && res.Error.Code == "permission_denied" {
		execution.AdmissionDecision = "deny"
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
