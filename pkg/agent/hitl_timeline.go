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
	"fmt"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/task"
)

// Compile-time check.
var _ task.TimelineSource = (*HITLTimelineSource)(nil)

const hitlTimelineSource = "human_requests"

// TaskHumanRequestLister reads human-in-the-loop requests by task.
//
// Declared here as a one-method interface rather than taken from pkg/shuttle so
// that this adapter depends on the capability, not the concrete store. The
// adapter lives in pkg/agent because pkg/shuttle cannot import pkg/task —
// pkg/task -> pkg/communication -> pkg/types -> pkg/shuttle is a cycle — and
// pkg/agent already depends on both.
type TaskHumanRequestLister interface {
	ListByTask(ctx context.Context, taskID string) ([]*shuttle.HumanRequest, error)
}

// HITLTimelineSource projects human_requests rows into timeline events.
//
// The human_requests table has recorded the question, status, response,
// responder, and timestamps since v1.0.0 — it is the authoritative record of a
// human decision and this projection does not copy it anywhere. The only thing
// that was ever missing is the task_id join, which is what lets a pending
// approval be shown on the task it blocks.
//
// One request yields one or two events: the question, and — once answered,
// rejected, or timed out — the outcome.
type HITLTimelineSource struct {
	store TaskHumanRequestLister
}

// NewHITLTimelineSource builds a HITL timeline source. A nil store yields a
// source that reports nothing, so callers can wire it unconditionally.
func NewHITLTimelineSource(store TaskHumanRequestLister) *HITLTimelineSource {
	return &HITLTimelineSource{store: store}
}

// SourceName implements task.TimelineSource.
func (h *HITLTimelineSource) SourceName() string { return hitlTimelineSource }

// TimelineEvents implements task.TimelineSource.
func (h *HITLTimelineSource) TimelineEvents(ctx context.Context, taskID string) ([]task.TimelineEvent, error) {
	if h == nil || h.store == nil || taskID == "" {
		return nil, nil
	}

	requests, err := h.store.ListByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("timeline: human requests for %s: %w", taskID, err)
	}

	events := make([]task.TimelineEvent, 0, len(requests)*2)
	for _, r := range requests {
		if r == nil {
			continue
		}

		events = append(events, task.TimelineEvent{
			Kind:             task.TimelineKindHumanRequest,
			OccurredAt:       r.CreatedAt,
			Summary:          fmt.Sprintf("Asked human (%s): %s", r.RequestType, firstLine(r.Question, 100)),
			Detail:           r.Question,
			AgentID:          r.AgentID,
			SessionID:        r.SessionID,
			HumanRequestID:   r.ID,
			HumanRequestType: r.RequestType,
			HumanOutcome:     r.Status,
			SourceTable:      hitlTimelineSource,
			SourceID:         r.ID,
		})

		// A second event only once the exchange actually resolved. A pending
		// request must NOT produce an outcome event — a timeline that implies a
		// human answered when they have not is worse than a gap.
		if r.RespondedAt == nil {
			continue
		}
		events = append(events, task.TimelineEvent{
			Kind:             task.TimelineKindHumanResponse,
			OccurredAt:       *r.RespondedAt,
			Summary:          hitlOutcomeSummary(r),
			Detail:           r.Response,
			SessionID:        r.SessionID,
			HumanRequestID:   r.ID,
			HumanRequestType: r.RequestType,
			HumanOutcome:     r.Status,
			SourceTable:      hitlTimelineSource,
			// Distinct from the request event so tie-breaking is total.
			SourceID: r.ID + ":response",
		})
	}
	return events, nil
}

// hitlOutcomeSummary renders how a request resolved.
func hitlOutcomeSummary(r *shuttle.HumanRequest) string {
	who := r.RespondedBy
	if who == "" {
		who = "human"
	}
	switch r.Status {
	case "approved":
		return fmt.Sprintf("Approved by %s", who)
	case "rejected":
		return fmt.Sprintf("Rejected by %s", who)
	case "timeout":
		return "Request timed out with no response"
	default:
		if r.Response != "" {
			return fmt.Sprintf("%s answered: %s", who, firstLine(r.Response, 90))
		}
		return fmt.Sprintf("%s responded", who)
	}
}
