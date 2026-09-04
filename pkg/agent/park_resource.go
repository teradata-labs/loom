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
// This file implements resource-await park: the SUCCESS-path counterpart of
// HITL park. A tool that starts a long-running job returns immediately with a
// result asking to be awaited (shuttle.Result.AwaitResource — stamped by the
// MCP adapter from the protocol.MetaAwaitResource result _meta, or by any
// embedder-owned tool bridge). Instead of handing that placeholder result to
// the model, the turn parks on the same durable human_requests machinery HITL
// park uses (kind "resource"), and the embedder resumes it with the
// resource's terminal content injected as the call's tool result
// (ParkDecision.Results). The parked call's tool body is NEVER re-executed on
// resume — re-running a job starter would start a second job.
package agent

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ResourceAwaitRequest names one held call and the resource whose terminal
// state carries its real outcome.
type ResourceAwaitRequest struct {
	SessionID string
	CallID    string
	Tool      string
	URI       string
}

// ResourceAwaitHandler is the embedder's watcher seam. PrepareWait is called
// BEFORE the turn commits to parking — subscribe to the resource there, and
// return an error to refuse (the call's original result then passes to the
// model unchanged, the graceful degradation for a resource nobody can watch).
// The parked request row, whose params name the same URIs (descriptor key
// "uri"), is persisted only after every PrepareWait of the batch succeeded;
// correlate wait→row via the park notifier or the request store. AbandonWait
// is the best-effort undo for a PrepareWait whose park never materialized
// (the row failed to persist).
//
// A nil handler (no WithResourceAwait) disables the feature entirely:
// AwaitResource results pass through as ordinary successes.
type ResourceAwaitHandler interface {
	PrepareWait(ctx context.Context, req ResourceAwaitRequest) error
	AbandonWait(req ResourceAwaitRequest)
}

// WithResourceAwait enables resource-await park. It is inert unless
// WithHITLPark is also configured — the park row and the resume path are the
// HITL park machinery, so there is nothing durable to park against without it.
func WithResourceAwait(h ResourceAwaitHandler) Option {
	return func(a *Agent) {
		a.resourceAwait = h
	}
}

// heldAwait is one dispatched call whose successful result asked to be
// awaited: the result is withheld from the transcript (no tool row) so the
// batch tail stays rowless for that call — the parked state locateParkedBatch
// reads — and the original result is kept for the fail-safe un-hold.
type heldAwait struct {
	call   ToolCall
	seq    int
	uri    string
	result *shuttle.Result
}

// maybeHoldForResourceAwait intercepts a just-executed call whose successful
// result carries AwaitResource. On a hold it returns true and the caller
// skips the commit ceremony — no tool row, no persisted execution — leaving
// the call rowless for maybeParkResourceBatch. Every refusal path returns
// false, and the result then commits exactly as today.
func (a *Agent) maybeHoldForResourceAwait(ctx Context, session *Session, toolCall ToolCall, i int, st *batchState, result *shuttle.Result, err error) bool {
	if err != nil || result == nil || !result.Success || result.AwaitResource == nil {
		return false
	}
	// parkableTail carries the pre-scan's own durability gate (hitlPark wired,
	// assistant row durable, store present) — set only by the conversation
	// loop, never by the resume path, so a resumed batch's remaining calls
	// pass their AwaitResource results through instead of nesting a park.
	if a.resourceAwait == nil || a.hitlPark == nil || !st.parkableTail {
		return false
	}
	// The park descriptor is keyed by ToolCall.ID, and locateParkedBatch marks
	// a call rowed when ANY row carries its ID — an empty or batch-duplicated
	// ID makes the held call's rowless state unrepresentable (same class of
	// refusal as checkParkItemIDs).
	if toolCall.ID == "" || st.batchIDCount[toolCall.ID] != 1 {
		zap.L().Warn("not holding a resource-await result whose call ID cannot bind a park; passing it through",
			zap.String("session_id", session.ID), zap.String("tool", toolCall.Name))
		return false
	}
	req := ResourceAwaitRequest{
		SessionID: session.ID,
		CallID:    toolCall.ID,
		Tool:      toolCall.Name,
		URI:       result.AwaitResource.URI,
	}
	if perr := a.resourceAwait.PrepareWait(ctx, req); perr != nil {
		zap.L().Warn("resource-await watcher refused the wait; passing the result through",
			zap.String("session_id", session.ID),
			zap.String("tool", toolCall.Name),
			zap.String("uri", req.URI),
			zap.Error(perr))
		return false
	}
	st.awaitHeld = append(st.awaitHeld, heldAwait{
		call:   toolCall,
		seq:    i,
		uri:    result.AwaitResource.URI,
		result: result,
	})
	return true
}

// maybeParkResourceBatch runs after the batch dispatch loop: with held calls
// it persists ONE grouped parked request (kind "resource", one descriptor per
// held call carrying its uri) and ends the turn with TurnParkedError. A
// persist failure un-holds fail-safe: every held call's ORIGINAL result is
// committed as its tool row — nothing strands rowless without a park row to
// resume it — and the watcher is told to abandon each wait.
func (a *Agent) maybeParkResourceBatch(ctx Context, sess *Session, st *batchState, llmResp *LLMResponse) error {
	if len(st.awaitHeld) == 0 {
		return nil
	}
	held := st.awaitHeld
	st.awaitHeld = nil

	items := make([]parkItem, 0, len(held))
	for _, h := range held {
		items = append(items, parkItem{call: h.call, seq: h.seq, kind: "resource", uri: h.uri})
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
		zap.L().Warn("resource-await park row failed to persist; committing the held results instead",
			zap.String("session_id", sess.ID), zap.Error(err))
		for _, h := range held {
			a.resourceAwait.AbandonWait(ResourceAwaitRequest{
				SessionID: sess.ID, CallID: h.call.ID, Tool: h.call.Name, URI: h.uri,
			})
			a.commitToolRow(ctx, sess, h.call, st, h.result, nil, nil)
		}
		return nil
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

// parkedItemKinds reads each descriptor's recorded kind off the request row —
// the authority at resume time, mirroring how the row's status overrides the
// caller's payload. A resource item must never take the approval arms (its
// tool already ran; re-execution would double a job start), whatever the
// decision payload claims.
func parkedItemKinds(hr *shuttle.HumanRequest) map[string]string {
	kinds := make(map[string]string, len(hr.Params))
	for id, raw := range hr.Params {
		if desc, ok := raw.(map[string]interface{}); ok {
			if k, _ := desc["kind"].(string); k != "" {
				kinds[id] = k
			}
		}
	}
	return kinds
}

// completeResourceItem finishes one resource-parked call on resume. Approved
// with a recorded payload → the payload IS the call's successful result Data,
// verbatim (the embedder composed it from the resource's terminal content).
// Everything else synthesizes a failure the model can re-plan from. No arm
// runs the tool body.
func (a *Agent) completeResourceItem(ctx Context, sess *Session, call ToolCall, decision ParkDecision, st *batchState) {
	if !decision.Approved {
		reason := decision.Reason
		if reason == "" {
			reason = "the awaited resource did not complete"
		}
		a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "resource_wait_failed", Message: reason, Retryable: false},
		}, st)
		return
	}
	payload, ok := decision.Results[call.ID]
	if !ok || payload == nil {
		a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:      "MISSING_RESULT",
				Message:   "no terminal resource state was recorded for this call",
				Retryable: false,
			},
		}, st)
		return
	}
	a.synthesizeParkedResult(ctx, sess, call, &shuttle.Result{
		Success: true,
		Data:    payload,
	}, st)
}
