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
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/observability"
)

// The task timeline is a READ MODEL. It has no storage of its own.
//
// Everything a human wants to see about a task is already persisted durably,
// by writers that were going to run anyway:
//
//	messages         tool_calls_json, tool_result_json, tool_use_id, content,
//	                 agent_id, timestamp — the full tool call and its result
//	task_history     every lifecycle transition, with old and new status
//	human_requests   question, status, response, responded_by, responded_at
//
// The only thing those tables lacked was a task_id to join on. Adding one
// nullable indexed column to each is the whole mechanism; this file assembles
// the result into one ordered sequence.
//
// This is deliberately NOT a new event log. An earlier design stored a second,
// truncated, droppable copy of the same facts in a dedicated table. That was
// strictly worse: it duplicated durable data under weaker guarantees, added a
// write path to the agent's hot loop, and needed its own drop accounting and
// retention policy. Reading what is already written costs nothing at write time
// and cannot be incomplete.

// TimelineEventKind classifies a timeline entry for rendering.
type TimelineEventKind string

const (
	// TimelineKindLifecycle is a task status transition (from task_history).
	TimelineKindLifecycle TimelineEventKind = "lifecycle"
	// TimelineKindToolCall is an agent invoking a tool (from messages).
	TimelineKindToolCall TimelineEventKind = "tool_call"
	// TimelineKindToolResult is a tool's return value (from messages).
	TimelineKindToolResult TimelineEventKind = "tool_result"
	// TimelineKindAssistant is agent-authored narrative text (from messages).
	TimelineKindAssistant TimelineEventKind = "assistant"
	// TimelineKindUser is a user message (from messages).
	TimelineKindUser TimelineEventKind = "user"
	// TimelineKindHumanRequest is a human-in-the-loop question.
	TimelineKindHumanRequest TimelineEventKind = "human_request"
	// TimelineKindHumanResponse is a human's answer, or a timeout.
	TimelineKindHumanResponse TimelineEventKind = "human_response"
)

// TimelineEvent is one entry in a task's timeline, projected from whichever
// table already recorded it.
//
// Detail carries the FULL payload from the source row — this model does not
// truncate, because the source did not. Presentation layers decide how much to
// show; a caller that needs a bounded excerpt calls Excerpt.
type TimelineEvent struct {
	Kind       TimelineEventKind
	OccurredAt time.Time

	// Summary is a one-line description suitable for a collapsed timeline row.
	Summary string

	// Detail is the full payload as stored (tool input, tool output, message
	// content, HITL question or answer).
	Detail string

	AgentID   string
	SessionID string

	// Tool fields, set for TimelineKindToolCall and TimelineKindToolResult.
	ToolName  string
	ToolUseID string

	// Outcome fields. Success is a pointer because "not applicable" and
	// "failed" are different states and a bool cannot express both.
	Success    *bool
	Error      string
	DurationMs int64

	// Lifecycle fields, set for TimelineKindLifecycle.
	OldStatus string
	NewStatus string

	// HITL fields, set for the human_request and human_response kinds.
	HumanRequestID   string
	HumanRequestType string
	HumanOutcome     string

	// Provenance of this row, so a reader can always tell where a fact came
	// from and drill into the authoritative record.
	SourceTable string
	SourceID    string

	// SourceOrder is the event's position in its own source's natural order,
	// set by the projection.
	//
	// It exists because SourceID is not an ordering. task_history rows carry a
	// random UUID and a SECOND-resolution timestamp, so two transitions in the
	// same second tie on time and then sort by UUID — which produced
	// "closed" before "created" in a real run. Each source knows its own
	// order (SQL ORDER BY, insertion sequence); preserving it as the tie-break
	// keeps the merged timeline logically coherent.
	SourceOrder int

	// Evicted marks an event whose source row the context-relief pass shed from
	// the agent's replay window under memory pressure. Folded marks one folded
	// into a session summary the agent replays instead.
	//
	// Both are surfaced rather than filtered, because they explain agent
	// behavior a reader would otherwise find baffling: a repeated query, a
	// re-run tool call, or a forgotten earlier decision usually means the
	// original was evicted or folded. The payload is not destroyed by either
	// operation — only flagged — so the human timeline stays MORE complete than
	// the agent's own memory and says where the two diverge.
	//
	// A row's flag applies to every event derived from it: if an assistant
	// message carrying three tool calls was evicted, the agent lost all three.
	Evicted bool
	Folded  bool

	// Turn is the session turn the source row belongs to, letting a reader
	// group a task's activity by turn instead of reading a flat list.
	Turn int64

	// TokenCount and CostUSD are the source row's own accounting, already
	// recorded per message by the conversation loop. Carried through so a task
	// can be totalled without a second query against the message table.
	TokenCount int64
	CostUSD    float64
}

// Excerpt returns Detail truncated to at most maxBytes, on a UTF-8 boundary,
// plus whether it was cut. Truncation is a presentation concern, so it happens
// here on read rather than being baked into storage.
func (e TimelineEvent) Excerpt(maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(e.Detail) <= maxBytes {
		return e.Detail, false
	}
	cut := maxBytes
	// Back off to the start of the last complete rune: a byte whose top bits
	// are 10 is a continuation byte.
	for cut > 0 && e.Detail[cut]&0xC0 == 0x80 {
		cut--
	}
	return e.Detail[:cut], true
}

// TimelineSource projects one existing table into timeline events for a task.
//
// Implementations live with the table they read, which keeps this package free
// of dependencies on the agent and shuttle packages (and avoids an import
// cycle, since both of those already depend on this one).
type TimelineSource interface {
	// SourceName identifies the backing table, used for provenance and for
	// stable ordering when two events share a timestamp.
	SourceName() string

	// TimelineEvents returns every event this source holds for the task, in any
	// order. An unknown task returns an empty slice, not an error.
	TimelineEvents(ctx context.Context, taskID string) ([]TimelineEvent, error)
}

// TimelineOpts filters and bounds a timeline read.
type TimelineOpts struct {
	// Kinds restricts the result to these kinds. Empty returns all kinds.
	Kinds []TimelineEventKind

	// Since and Until bound the window. Zero values mean unbounded.
	Since time.Time
	Until time.Time

	// Limit caps the number of events returned, keeping the OLDEST when
	// Newest is false and the newest when it is true. Zero means the package
	// default; a timeline over a long-running task is unbounded, so an
	// uncapped read is never performed.
	Limit int

	// Newest returns the most recent events instead of the oldest. The result
	// is still in ascending time order.
	Newest bool
}

// DefaultTimelineLimit bounds a timeline read when the caller gives no limit.
const DefaultTimelineLimit = 500

// MaxTimelineLimit is the hard ceiling on a single timeline read.
const MaxTimelineLimit = 2000

// TimelineReader merges several sources into one ordered timeline.
type TimelineReader struct {
	sources []TimelineSource
	tracer  observability.Tracer
	logger  *zap.Logger
}

// NewTimelineReader builds a reader over the given sources. Nil sources are
// ignored, so a caller can pass optional sources without pre-filtering.
func NewTimelineReader(tracer observability.Tracer, logger *zap.Logger, sources ...TimelineSource) *TimelineReader {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	live := make([]TimelineSource, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			live = append(live, s)
		}
	}
	return &TimelineReader{sources: live, tracer: tracer, logger: logger}
}

// TimelineResult is a read of a task's timeline.
type TimelineResult struct {
	Events []TimelineEvent

	// Truncated is true when Limit cut events from the result.
	Truncated bool

	// TotalMatched is how many events matched the filter before Limit applied.
	TotalMatched int

	// PartialSources names sources that failed. Their events are missing from
	// Events. Reported rather than returned as an error, because a timeline
	// missing its HITL rows is still worth showing — but the reader must know
	// it is incomplete rather than assuming an empty source means nothing
	// happened.
	PartialSources []string
}

// Read assembles the timeline for a task.
//
// Sources are queried concurrently: they hit different tables and none depends
// on another, so the read costs the slowest source rather than their sum.
func (r *TimelineReader) Read(ctx context.Context, taskID string, opts TimelineOpts) (*TimelineResult, error) {
	ctx, span := r.tracer.StartSpan(ctx, "task.timeline.read")
	defer r.tracer.EndSpan(span)

	if taskID == "" {
		return nil, ErrTimelineTaskIDRequired
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultTimelineLimit
	}
	if limit > MaxTimelineLimit {
		limit = MaxTimelineLimit
	}

	type sourceResult struct {
		name   string
		events []TimelineEvent
		err    error
	}

	results := make([]sourceResult, len(r.sources))
	var wg sync.WaitGroup
	for i, src := range r.sources {
		wg.Add(1)
		go func(i int, src TimelineSource) {
			defer wg.Done()
			events, err := src.TimelineEvents(ctx, taskID)
			results[i] = sourceResult{name: src.SourceName(), events: events, err: err}
		}(i, src)
	}
	wg.Wait()

	out := &TimelineResult{}
	merged := make([]TimelineEvent, 0, 64)
	for _, res := range results {
		if res.err != nil {
			out.PartialSources = append(out.PartialSources, res.name)
			r.logger.Warn("timeline source failed",
				zap.String("source", res.name),
				zap.String("task_id", taskID),
				zap.Error(res.err))
			continue
		}
		for _, e := range res.events {
			if !matchesTimelineOpts(e, opts) {
				continue
			}
			if e.SourceTable == "" {
				e.SourceTable = res.name
			}
			merged = append(merged, e)
		}
	}

	sortTimeline(merged)
	out.TotalMatched = len(merged)

	if len(merged) > limit {
		out.Truncated = true
		if opts.Newest {
			merged = merged[len(merged)-limit:]
		} else {
			merged = merged[:limit]
		}
	}
	out.Events = merged

	span.SetAttribute("task_id", taskID)
	return out, nil
}

// matchesTimelineOpts applies the kind and time filters.
func matchesTimelineOpts(e TimelineEvent, opts TimelineOpts) bool {
	if len(opts.Kinds) > 0 {
		found := false
		for _, k := range opts.Kinds {
			if e.Kind == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if !opts.Since.IsZero() && e.OccurredAt.Before(opts.Since) {
		return false
	}
	if !opts.Until.IsZero() && e.OccurredAt.After(opts.Until) {
		return false
	}
	return true
}

// sortTimeline orders events by time, breaking ties deterministically.
//
// Ties are the common case, not the exception: message timestamps are
// second-resolution, and a lifecycle transition usually lands in the same second
// as the message that caused it.
//
// The tie-break is (source table, source order, source id). Source order comes
// before source id deliberately — id is a random UUID for task_history, so
// ordering by it put "closed" ahead of "created" in a real run. Id remains the
// last resort so the comparison is still total when a projection leaves order
// unset.
func sortTimeline(events []TimelineEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		if a.SourceTable != b.SourceTable {
			return a.SourceTable < b.SourceTable
		}
		if a.SourceOrder != b.SourceOrder {
			return a.SourceOrder < b.SourceOrder
		}
		return a.SourceID < b.SourceID
	})
}
