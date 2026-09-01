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
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/task"
)

// Compile-time check: SessionStore projects messages into task timelines.
var _ task.TimelineSource = (*SessionStore)(nil)

// messageTimelineSource names the backing table for provenance and tie-breaking.
const messageTimelineSource = "messages"

// SourceName implements task.TimelineSource.
func (s *SessionStore) SourceName() string { return messageTimelineSource }

// messageRowContext holds the facts a message row carries about itself rather
// than about the event it describes: whether context relief shed it, which turn
// it belongs to, and what it cost.
//
// It exists so the projection reads these columns once per row and stamps them
// onto every event derived from that row, instead of each event-construction
// site having to remember five extra fields.
type messageRowContext struct {
	evicted    bool
	folded     bool
	turn       int64
	tokenCount int64
	costUSD    float64
}

// applyTo stamps the row's context onto every event projected from that row.
func (c messageRowContext) applyTo(events []task.TimelineEvent) {
	for i := range events {
		events[i].Evicted = c.evicted
		events[i].Folded = c.folded
		events[i].Turn = c.turn
		events[i].TokenCount = c.tokenCount
		events[i].CostUSD = c.costUSD
	}
}

// TimelineEvents projects a task's conversation messages into timeline events.
//
// This is the read half of the task-attribution design: instead of writing a
// second copy of every tool call into a dedicated log, the message rows that
// already hold them are stamped with task_id and read back here. One tool call
// yields two events — the invocation and its result — reconstructed from
// tool_calls_json on the assistant message and tool_result_json on the matching
// tool message, correlated by tool_use_id exactly as the LLM providers require.
//
// The query is an index range scan on (task_id, timestamp), so cost is
// proportional to the task's own messages rather than the session's.
func (s *SessionStore) TimelineEvents(ctx context.Context, taskID string) ([]task.TimelineEvent, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.timeline_events")
	defer s.tracer.EndSpan(span)

	if taskID == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// evicted, folded, turn, token_count and cost_usd are read even though the
	// projection below does not branch on them: they are per-row facts the
	// conversation loop already wrote, and dropping them here is what made the
	// timeline narrower than the record it reads from. COALESCE because these
	// columns arrive by ALTER TABLE (SessionStore owns this table and
	// self-migrates), so rows written before a given column existed read NULL.
	// The owner predicate is not optional here, for a structural reason: messages
	// is user-scoped but tasks carries no user_id at all, so filtering on task_id
	// alone joins from an unscoped namespace into a scoped one and returns
	// whatever session that task happens to touch. Any caller holding a task id —
	// guessed, logged, or belonging to someone else — would read another user's
	// conversation. Same predicate loadMessagesLocked uses, and for the same
	// stated reason: this helper must not become a cross-user read if a caller
	// forgets to guard.
	//
	// A non-owner gets an empty result, which is deliberately indistinguishable
	// from an unknown task id — the absence of a row must not confirm that the
	// task exists.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, content, tool_calls_json, tool_use_id, tool_result_json, agent_id, timestamp,
		       COALESCE(evicted, 0), COALESCE(folded, 0), COALESCE(turn, 0),
		       COALESCE(token_count, 0), COALESCE(cost_usd, 0)
		FROM messages
		WHERE task_id = ?
		  AND EXISTS (SELECT 1 FROM sessions WHERE id = messages.session_id AND user_id = ?)
		ORDER BY timestamp ASC, id ASC`, taskID, storeUserID(ctx))
	if err != nil {
		return nil, fmt.Errorf("timeline: query messages for task %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var events []task.TimelineEvent
	order := 0
	for rows.Next() {
		var (
			msgID                                   int64
			role, content                           string
			toolCallsJSON, toolUseID, toolResultRaw sql.NullString
			agentID                                 sql.NullString
			ts                                      int64
			evicted, folded                         int
			turn, tokenCount                        int64
			costUSD                                 float64
		)
		if err := rows.Scan(&msgID, &role, &content, &toolCallsJSON,
			&toolUseID, &toolResultRaw, &agentID, &ts,
			&evicted, &folded, &turn, &tokenCount, &costUSD); err != nil {
			return nil, fmt.Errorf("timeline: scan message: %w", err)
		}

		// Per-row context facts, stamped onto every event this row produces.
		// One assistant message can yield a narrative event plus several tool
		// calls; if the row was evicted, the agent lost all of them together.
		rowCtx := messageRowContext{
			evicted:    evicted != 0,
			folded:     folded != 0,
			turn:       turn,
			tokenCount: tokenCount,
			costUSD:    costUSD,
		}

		occurred := time.Unix(ts, 0).UTC()
		sourceID := fmt.Sprintf("%d", msgID)
		// The query is ORDER BY timestamp, id — so the scan order IS the
		// conversation order. Preserve it: message timestamps are
		// second-resolution and tie constantly.
		rowOrder := order
		order++

		// Everything appended from here to the end of this iteration came from
		// this one message row, so it all carries the row's context facts.
		rowStart := len(events)

		switch role {
		case "assistant":
			// Narrative text, when the assistant said something rather than
			// only calling tools.
			if strings.TrimSpace(content) != "" {
				events = append(events, task.TimelineEvent{
					Kind:        task.TimelineKindAssistant,
					OccurredAt:  occurred,
					Summary:     firstLine(content, 120),
					Detail:      content,
					AgentID:     agentID.String,
					SourceTable: messageTimelineSource,
					SourceID:    sourceID,
					SourceOrder: rowOrder,
				})
			}
			// One event per tool invocation on this message.
			if toolCallsJSON.Valid && toolCallsJSON.String != "" {
				events = append(events,
					toolCallEvents(toolCallsJSON.String, occurred, agentID.String, sourceID, rowOrder)...)
			}

		case "tool":
			events = append(events,
				toolResultEvent(toolResultRaw, toolUseID, content, occurred, agentID.String, sourceID, rowOrder))

		case "user":
			events = append(events, task.TimelineEvent{
				Kind:        task.TimelineKindUser,
				OccurredAt:  occurred,
				Summary:     firstLine(content, 120),
				Detail:      content,
				SourceTable: messageTimelineSource,
				SourceID:    sourceID,
				SourceOrder: rowOrder,
			})
		}

		rowCtx.applyTo(events[rowStart:])
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timeline: iterate messages: %w", err)
	}

	span.SetAttribute("task_id", taskID)
	span.SetAttribute("event_count", fmt.Sprintf("%d", len(events)))
	return events, nil
}

// toolCallEvents expands an assistant message's tool_calls_json into one event
// per invocation. A malformed blob yields no events rather than failing the
// whole timeline — a single unparseable row should not blank the view.
func toolCallEvents(raw string, occurred time.Time, agentID, sourceID string, rowOrder int) []task.TimelineEvent {
	var calls []struct {
		ID    string                 `json:"ID"`
		Name  string                 `json:"Name"`
		Input map[string]interface{} `json:"Input"`
	}
	if err := json.Unmarshal([]byte(raw), &calls); err != nil {
		return nil
	}

	out := make([]task.TimelineEvent, 0, len(calls))
	for _, c := range calls {
		detail := ""
		if len(c.Input) > 0 {
			if b, err := json.Marshal(c.Input); err == nil {
				detail = string(b)
			}
		}
		out = append(out, task.TimelineEvent{
			Kind:        task.TimelineKindToolCall,
			OccurredAt:  occurred,
			Summary:     fmt.Sprintf("Called %s", c.Name),
			Detail:      detail,
			AgentID:     agentID,
			ToolName:    c.Name,
			ToolUseID:   c.ID,
			SourceTable: messageTimelineSource,
			SourceID:    sourceID,
			SourceOrder: rowOrder,
		})
	}
	return out
}

// toolResultEvent projects a role='tool' message into a result event.
//
// tool_result_json is a serialized shuttle.Result, which already carries
// success, the error, and the execution time — so the timeline gets the outcome
// and duration without any new instrumentation. When it is absent or
// unparseable, the message content is used as the payload and the outcome is
// reported as unknown rather than guessed.
func toolResultEvent(raw, toolUseID sql.NullString, content string, occurred time.Time, agentID, sourceID string, rowOrder int) task.TimelineEvent {
	ev := task.TimelineEvent{
		Kind:        task.TimelineKindToolResult,
		OccurredAt:  occurred,
		AgentID:     agentID,
		ToolUseID:   toolUseID.String,
		Detail:      content,
		SourceTable: messageTimelineSource,
		SourceID:    sourceID,
		SourceOrder: rowOrder,
	}

	if raw.Valid && raw.String != "" {
		var res shuttle.Result
		if err := json.Unmarshal([]byte(raw.String), &res); err == nil {
			success := res.Success
			ev.Success = &success
			ev.DurationMs = res.ExecutionTimeMs
			if res.Error != nil {
				ev.Error = res.Error.Message
			}
			if res.Data != nil {
				if b, err := json.Marshal(res.Data); err == nil {
					ev.Detail = string(b)
				}
			}
		}
	}

	switch {
	case ev.Success != nil && !*ev.Success:
		ev.Summary = "Tool failed"
		if ev.Error != "" {
			ev.Summary = "Tool failed: " + firstLine(ev.Error, 80)
		}
	case ev.Success != nil:
		ev.Summary = "Tool returned"
	default:
		ev.Summary = "Tool result"
	}
	return ev
}

// firstLine returns the first line of s, capped at maxLen bytes on a rune
// boundary, for use as a one-line timeline summary.
func firstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// AttributeTurnMessages retroactively stamps a task ID on this turn's already
// written, unattributed messages. Returns how many rows it claimed.
//
// This closes a gap that only showed up when the flow was run end to end: the
// implicit task is minted LAZILY, on the first tool call, but the user message
// that asked for the work was written before that — so it landed with a NULL
// task_id and the timeline began at the agent's first action, silently missing
// the request that caused it. A task whose timeline does not show what was
// asked is close to useless to a human.
//
// The alternative — mint the task at the start of every turn — would put a
// board row on "thanks, that worked". So the task stays lazy and this back-fills
// instead: one UPDATE, once per turn, only when a task is actually minted.
//
// `since` bounds it to the current turn. Only rows with a NULL task_id are
// touched, so a message already attributed to a real claimed task is never
// stolen.
func (s *SessionStore) AttributeTurnMessages(ctx context.Context, sessionID, taskID string, since time.Time) (int64, error) {
	ctx, span := s.tracer.StartSpan(ctx, "session_store.attribute_turn_messages")
	defer s.tracer.EndSpan(span)

	if sessionID == "" || taskID == "" {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Owner-scoped for the same reason as the read above, and it matters more
	// here: a write needs no task id to be discovered first. Without the
	// predicate, any caller could stamp its own task onto another user's
	// unattributed rows and then read them back through the timeline it now
	// legitimately owns — turning a write it was allowed to make into a read it
	// was not.
	res, err := s.db.ExecContext(ctx, `
		UPDATE messages SET task_id = ?
		WHERE session_id = ? AND task_id IS NULL AND timestamp >= ?
		  AND EXISTS (SELECT 1 FROM sessions WHERE id = messages.session_id AND user_id = ?)`,
		taskID, sessionID, since.Unix(), storeUserID(ctx))
	if err != nil {
		span.RecordError(err)
		return 0, fmt.Errorf("attribute turn messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // driver did not report; the update still applied
	}
	span.SetAttribute("rows_attributed", fmt.Sprintf("%d", n))
	return n, nil
}
