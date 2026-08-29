// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
)

func timelineStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "timeline.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestTimeline_AttributionStampedFromContext is the load-bearing test for the
// whole redesign: a message written while a task is claimed must land with that
// task's ID, with no change to the calling code beyond the context it already
// passes.
func TestTimeline_AttributionStampedFromContext(t *testing.T) {
	store := timelineStore(t)
	ctx := context.Background()

	sess := &Session{ID: "sess-1", AgentID: "agent-1", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	// Under a claimed task: the write inherits the attribution.
	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{
		TaskID:    "task-1",
		SessionID: "sess-1",
		AgentID:   "agent-1",
	})
	require.NoError(t, store.SaveMessage(taskCtx, "sess-1", &Message{
		Role:      "assistant",
		Content:   "Working on it",
		AgentID:   "agent-1",
		Timestamp: time.Now(),
	}, false))

	// Outside any task: task_id stays NULL. This is the common case and must
	// not be an error.
	require.NoError(t, store.SaveMessage(ctx, "sess-1", &Message{
		Role:      "assistant",
		Content:   "Unattributed chatter",
		Timestamp: time.Now(),
	}, false))

	loaded, err := store.LoadMessages(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, loaded, 2)

	byContent := map[string]string{}
	for _, m := range loaded {
		byContent[m.Content] = m.TaskID
	}
	assert.Equal(t, "task-1", byContent["Working on it"], "message written under a claimed task must carry its ID")
	assert.Equal(t, "", byContent["Unattributed chatter"], "message written outside a task must have no task ID")

	// And the timeline sees only the attributed one.
	events, err := store.TimelineEvents(ctx, "task-1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, task.TimelineKindAssistant, events[0].Kind)
	assert.Equal(t, "Working on it", events[0].Detail)
}

// TestTimeline_ToolCallAndResultReconstructed proves the central claim of the
// redesign: a full tool call — name, input, output, success, duration — can be
// recovered from the message rows that were already being written, with no
// dedicated event log.
func TestTimeline_ToolCallAndResultReconstructed(t *testing.T) {
	store := timelineStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{
		ID: "sess-2", AgentID: "agent-1", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{
		TaskID: "task-2", SessionID: "sess-2", AgentID: "agent-1",
	})
	base := time.Now()

	// The assistant calls a tool.
	require.NoError(t, store.SaveMessage(taskCtx, "sess-2", &Message{
		Role:    "assistant",
		Content: "Let me check the schema",
		AgentID: "agent-1",
		ToolCalls: []types.ToolCall{{
			ID:    "call-abc",
			Name:  "describe_table",
			Input: map[string]interface{}{"table": "orders"},
		}},
		Timestamp: base,
	}, false))

	// The tool returns.
	require.NoError(t, store.SaveMessage(taskCtx, "sess-2", &Message{
		Role:      "tool",
		ToolUseID: "call-abc",
		Content:   "columns: id, total",
		ToolResult: &shuttle.Result{
			Success:         true,
			Data:            map[string]interface{}{"columns": []string{"id", "total"}},
			ExecutionTimeMs: 37,
		},
		Timestamp: base.Add(time.Second),
	}, false))

	events, err := store.TimelineEvents(ctx, "task-2")
	require.NoError(t, err)

	var call, result *task.TimelineEvent
	for i := range events {
		switch events[i].Kind {
		case task.TimelineKindToolCall:
			call = &events[i]
		case task.TimelineKindToolResult:
			result = &events[i]
		}
	}

	require.NotNil(t, call, "tool call must be recoverable from tool_calls_json")
	assert.Equal(t, "describe_table", call.ToolName)
	assert.Equal(t, "call-abc", call.ToolUseID)
	assert.Contains(t, call.Detail, "orders", "the tool input must survive round-trip")
	assert.Equal(t, "Called describe_table", call.Summary)

	require.NotNil(t, result, "tool result must be recoverable from tool_result_json")
	assert.Equal(t, "call-abc", result.ToolUseID, "result correlates to its call via tool_use_id")
	require.NotNil(t, result.Success)
	assert.True(t, *result.Success)
	assert.Equal(t, int64(37), result.DurationMs, "duration comes free from the stored shuttle.Result")
	assert.Contains(t, result.Detail, "total")
}

// TestTimeline_FailedToolSurfacesError verifies the outcome fields distinguish
// failure from success from not-applicable.
func TestTimeline_FailedToolSurfacesError(t *testing.T) {
	store := timelineStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{
		ID: "sess-3", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{TaskID: "task-3", SessionID: "sess-3"})

	require.NoError(t, store.SaveMessage(taskCtx, "sess-3", &Message{
		Role:      "tool",
		ToolUseID: "call-fail",
		ToolResult: &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Message: "table not found"},
			ExecutionTimeMs: 4,
		},
		Timestamp: time.Now(),
	}, false))

	events, err := store.TimelineEvents(ctx, "task-3")
	require.NoError(t, err)
	require.Len(t, events, 1)

	require.NotNil(t, events[0].Success)
	assert.False(t, *events[0].Success)
	assert.Equal(t, "table not found", events[0].Error)
	assert.Contains(t, events[0].Summary, "table not found")
}

// TestTimeline_MalformedToolPayloadDoesNotBlankTheView verifies one bad row
// degrades to a partial event rather than failing the whole read.
func TestTimeline_MalformedToolPayloadDoesNotBlankTheView(t *testing.T) {
	store := timelineStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{
		ID: "sess-4", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// Write a row with deliberately corrupt JSON, bypassing SaveMessage.
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO messages (session_id, role, content, tool_calls_json, task_id, timestamp)
		VALUES (?, 'assistant', 'broken', '{not json', ?, ?)`,
		"sess-4", "task-4", time.Now().Unix())
	require.NoError(t, err)
	// Plus one good row.
	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{TaskID: "task-4", SessionID: "sess-4"})
	require.NoError(t, store.SaveMessage(taskCtx, "sess-4", &Message{
		Role: "assistant", Content: "fine", Timestamp: time.Now().Add(time.Second),
	}, false))

	events, err := store.TimelineEvents(ctx, "task-4")
	require.NoError(t, err, "a corrupt payload must not fail the whole timeline")

	var contents []string
	for _, e := range events {
		contents = append(contents, e.Detail)
	}
	assert.Contains(t, contents, "fine")
	assert.Contains(t, contents, "broken", "the message text survives even when its tool payload does not")
}

// TestTimeline_UnknownTaskIsEmptyNotAnError pins the contract the reader relies
// on to distinguish "nothing happened" from "source failed".
func TestTimeline_UnknownTaskIsEmptyNotAnError(t *testing.T) {
	store := timelineStore(t)
	events, err := store.TimelineEvents(context.Background(), "no-such-task")
	require.NoError(t, err)
	assert.Empty(t, events)

	events, err = store.TimelineEvents(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, events)
}

// BenchmarkTimelineRead measures the read model's cost on a task with a
// realistic amount of work behind it. This is the number that has to justify
// the redesign: the deleted design paid ~57 µs per event at WRITE time, on
// every tool call, forever. This pays once, on read, only when a human looks.
func BenchmarkTimelineRead(b *testing.B) {
	store, err := NewSessionStore(filepath.Join(b.TempDir(), "bench.db"), observability.NewNoOpTracer())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	if err := store.SaveSession(ctx, &Session{
		ID: "s", AgentID: "a", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		b.Fatal(err)
	}
	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{TaskID: "t", SessionID: "s"})

	// 200 tool calls = 400 message rows, a heavily-worked task.
	base := time.Now()
	for i := 0; i < 200; i++ {
		if err := store.SaveMessage(taskCtx, "s", &Message{
			Role: "assistant", AgentID: "a",
			ToolCalls: []types.ToolCall{{
				ID: "c", Name: "read_file",
				Input: map[string]interface{}{"path": "/some/reasonably/long/path/to/a/file.go"},
			}},
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}, false); err != nil {
			b.Fatal(err)
		}
		if err := store.SaveMessage(taskCtx, "s", &Message{
			Role: "tool", ToolUseID: "c",
			ToolResult: &shuttle.Result{
				Success:         true,
				Data:            map[string]interface{}{"content": "package main"},
				ExecutionTimeMs: 12,
			},
			Timestamp: base.Add(time.Duration(i)*time.Second + 500*time.Millisecond),
		}, false); err != nil {
			b.Fatal(err)
		}
	}

	reader := task.NewTimelineReader(nil, nil, store)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := reader.Read(ctx, "t", task.TimelineOpts{Limit: 500})
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Events) == 0 {
			b.Fatal("empty timeline")
		}
	}
}

// fakeHITLLister stands in for the SQLite human-request store.
type fakeHITLLister struct{ reqs []*shuttle.HumanRequest }

func (f *fakeHITLLister) ListByTask(context.Context, string) ([]*shuttle.HumanRequest, error) {
	return f.reqs, nil
}

// TestTimeline_HITLPendingVsAnswered pins the contract that matters most for
// HITL: a pending request must produce a question event and NO outcome event.
// A timeline implying a human answered when they have not is worse than a gap.
func TestTimeline_HITLPendingVsAnswered(t *testing.T) {
	answered := time.Now().Add(2 * time.Minute)
	src := NewHITLTimelineSource(&fakeHITLLister{reqs: []*shuttle.HumanRequest{
		{
			ID: "req-pending", AgentID: "a1", SessionID: "s1",
			Question: "Approve dropping the staging table?", RequestType: "approval",
			Status: "pending", CreatedAt: time.Now(), TaskID: "task-1",
		},
		{
			ID: "req-done", AgentID: "a1", SessionID: "s1",
			Question: "Proceed with the migration?", RequestType: "approval",
			Status: "approved", CreatedAt: time.Now().Add(time.Minute),
			RespondedAt: &answered, RespondedBy: "josh", TaskID: "task-1",
		},
	}})

	events, err := src.TimelineEvents(context.Background(), "task-1")
	require.NoError(t, err)

	var questions, outcomes int
	for _, e := range events {
		switch e.Kind {
		case task.TimelineKindHumanRequest:
			questions++
		case task.TimelineKindHumanResponse:
			outcomes++
			assert.Equal(t, "req-done", e.HumanRequestID, "only the answered request yields an outcome")
			assert.Contains(t, e.Summary, "josh")
		}
	}
	assert.Equal(t, 2, questions, "both requests are visible as questions")
	assert.Equal(t, 1, outcomes, "a pending request must not produce an outcome event")
}

// TestTimeline_HITLMergesWithConversation is the end-to-end shape a UI needs:
// one ordered sequence spanning two independent tables, joined only by task_id.
func TestTimeline_HITLMergesWithConversation(t *testing.T) {
	store := timelineStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{
		ID: "s5", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	taskCtx := task.ContextWithAttribution(ctx, task.Attribution{TaskID: "task-5", SessionID: "s5"})

	base := time.Now().Truncate(time.Second)
	require.NoError(t, store.SaveMessage(taskCtx, "s5", &Message{
		Role: "assistant", Content: "I need approval before dropping this", Timestamp: base,
	}, false))
	require.NoError(t, store.SaveMessage(taskCtx, "s5", &Message{
		Role: "assistant", Content: "Proceeding now", Timestamp: base.Add(3 * time.Second),
	}, false))

	answered := base.Add(2 * time.Second)
	hitl := NewHITLTimelineSource(&fakeHITLLister{reqs: []*shuttle.HumanRequest{{
		ID: "r1", Question: "Approve?", RequestType: "approval", Status: "approved",
		CreatedAt: base.Add(time.Second), RespondedAt: &answered, RespondedBy: "josh",
	}}})

	reader := task.NewTimelineReader(nil, nil, store, hitl)
	res, err := reader.Read(ctx, "task-5", task.TimelineOpts{})
	require.NoError(t, err)
	require.Empty(t, res.PartialSources)

	var kinds []task.TimelineEventKind
	for _, e := range res.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []task.TimelineEventKind{
		task.TimelineKindAssistant,
		task.TimelineKindHumanRequest,
		task.TimelineKindHumanResponse,
		task.TimelineKindAssistant,
	}, kinds, "the approval must interleave into the conversation at the right point")
}
