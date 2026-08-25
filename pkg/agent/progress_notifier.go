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

import (
	"context"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// progressNotifier is the shuttle.Notifier→ProgressEvent bridge. It lives in
// pkg/agent because pkg/shuttle cannot import pkg/agent (import cycle), while
// pkg/agent already depends on both shuttle and types. Both pending origins —
// the ask resolver and contact_human — fire their post-Store Notify through
// this bridge so a single id-bearing HITL card reaches the run's progress
// stream in one shape.
type progressNotifier struct{}

// NewProgressNotifier returns the bridge that converts a pending HumanRequest
// into a StageHumanInTheLoop ProgressEvent delivered on the run's installed
// ProgressCallback.
func NewProgressNotifier() shuttle.Notifier { return progressNotifier{} }

// The bridge is also a Heartbeater: the ask resolver's waiter pokes it while a
// hold is still pending. Asserted here so the capability cannot be lost silently
// (it is reached only through an interface assertion at the call site).
var _ shuttle.Heartbeater = progressNotifier{}

// Notify delivers the pending request as a StageHumanInTheLoop ProgressEvent on
// the callback carried in ctx. The callback is read from ctx (not captured at
// build time) because it enters ctx only at run start, after the agent is
// built. With no callback installed the notification is a no-op — fail-open on
// notification: a missing progress stream never blocks or fails the hold.
func (progressNotifier) Notify(ctx context.Context, req *shuttle.HumanRequest) error {
	cb := ProgressCallbackFromContext(ctx)
	if cb == nil {
		return nil
	}
	cb(types.ProgressEvent{
		Stage: StageHumanInTheLoop,
		HITLRequest: &types.HITLRequestInfo{
			RequestID:       req.ID,
			Kind:            req.Kind,
			Summary:         req.Summary,
			Params:          req.Params,
			ParamsTruncated: req.ParamsTruncated,
			ExpiresAt:       req.ExpiresAt,
			RequestType:     req.RequestType,
			Priority:        req.Priority,
			Question:        req.Question,
		},
		Message:   "Waiting for human response",
		Timestamp: time.Now(),
	})
	return nil
}

// Heartbeat delivers an id-less StageHumanInTheLoop ProgressEvent on the
// callback carried in ctx, so a hold that is otherwise byte-silent for its whole
// window keeps the run's progress stream producing traffic.
//
// It deliberately carries NO HITLRequest. A consumer keys the human-facing card
// off the id-bearing event (an id-less event is already the documented
// pre-creation ping and is ignored), so a heartbeat cannot duplicate a card —
// including on a consumer built before heartbeats existed, which simply drops
// it. Fail-open like Notify: with no callback installed this is a no-op, and a
// missing progress stream never blocks or fails the hold.
func (progressNotifier) Heartbeat(ctx context.Context) error {
	cb := ProgressCallbackFromContext(ctx)
	if cb == nil {
		return nil
	}
	cb(types.ProgressEvent{
		Stage:     StageHumanInTheLoop,
		Message:   "Still waiting for human response",
		Timestamp: time.Now(),
	})
	return nil
}
