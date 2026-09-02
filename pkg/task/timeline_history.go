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

package task

import (
	"context"
	"fmt"
)

// Compile-time check.
var _ TimelineSource = (*HistorySource)(nil)

// historyTimelineSource names the backing table.
const historyTimelineSource = "task_history"

// HistorySource projects task_history rows into timeline events.
//
// task_history already records every lifecycle transition with its old and new
// status, the acting agent, and the session — keyed by task_id from the start.
// It needs no schema change at all; it only needed a reader that speaks the
// timeline's shape.
type HistorySource struct {
	store TaskStore
}

// NewHistorySource builds a lifecycle timeline source over a task store.
func NewHistorySource(store TaskStore) *HistorySource {
	return &HistorySource{store: store}
}

// SourceName implements TimelineSource.
func (h *HistorySource) SourceName() string { return historyTimelineSource }

// TimelineEvents implements TimelineSource.
func (h *HistorySource) TimelineEvents(ctx context.Context, taskID string) ([]TimelineEvent, error) {
	if h == nil || h.store == nil || taskID == "" {
		return nil, nil
	}

	entries, err := h.store.GetHistory(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("timeline: task history for %s: %w", taskID, err)
	}

	events := make([]TimelineEvent, 0, len(entries))
	for i, e := range entries {
		if e == nil {
			continue
		}
		events = append(events, TimelineEvent{
			// GetHistory returns rows ORDER BY created_at ASC, i.e. insertion
			// order; preserve it so same-second transitions stay coherent.
			SourceOrder: i,
			Kind:        TimelineKindLifecycle,
			OccurredAt:  e.Timestamp.UTC(),
			Summary:     lifecycleSummary(e),
			Detail:      e.DetailsJSON,
			AgentID:     e.AgentID,
			SessionID:   e.SessionID,
			OldStatus:   e.OldStatus,
			NewStatus:   e.NewStatus,
			SourceTable: historyTimelineSource,
			SourceID:    e.ID,
		})
	}
	return events, nil
}

// lifecycleSummary renders a transition as one human-readable line.
func lifecycleSummary(e *TaskHistoryEntry) string {
	switch {
	case e.OldStatus != "" && e.NewStatus != "" && e.OldStatus != e.NewStatus:
		return fmt.Sprintf("Task %s: %s → %s", e.Action, e.OldStatus, e.NewStatus)
	case e.NewStatus != "":
		return fmt.Sprintf("Task %s (%s)", e.Action, e.NewStatus)
	default:
		return fmt.Sprintf("Task %s", e.Action)
	}
}
