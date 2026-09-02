// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeSource is a TimelineSource backed by a fixed slice, optionally failing.
type fakeSource struct {
	name   string
	events []TimelineEvent
	err    error
	calls  int
}

func (f *fakeSource) SourceName() string { return f.name }
func (f *fakeSource) TimelineEvents(context.Context, string) ([]TimelineEvent, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func at(sec int) time.Time {
	return time.Unix(int64(1700000000+sec), 0).UTC()
}

func ev(kind TimelineEventKind, sec int, summary string) TimelineEvent {
	return TimelineEvent{Kind: kind, OccurredAt: at(sec), Summary: summary}
}

func TestTimeline_MergesSourcesInTimeOrder(t *testing.T) {
	msgs := &fakeSource{name: "messages", events: []TimelineEvent{
		ev(TimelineKindToolCall, 20, "call"),
		ev(TimelineKindAssistant, 10, "thinking"),
	}}
	hist := &fakeSource{name: "task_history", events: []TimelineEvent{
		ev(TimelineKindLifecycle, 5, "claimed"),
		ev(TimelineKindLifecycle, 30, "closed"),
	}}

	r := NewTimelineReader(nil, nil, msgs, hist)
	got, err := r.Read(context.Background(), "task-1", TimelineOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := []string{"claimed", "thinking", "call", "closed"}
	if len(got.Events) != len(want) {
		t.Fatalf("got %d events, want %d", len(got.Events), len(want))
	}
	for i, w := range want {
		if got.Events[i].Summary != w {
			t.Errorf("position %d: got %q, want %q", i, got.Events[i].Summary, w)
		}
	}
	if got.Truncated {
		t.Error("should not be truncated")
	}
}

// TestTimeline_TieBreakIsStable matters because message rows share a
// second-resolution timestamp constantly, and a lifecycle transition usually
// lands on the same second as the message that caused it. Order must not shift
// between reads.
func TestTimeline_TieBreakIsStable(t *testing.T) {
	mk := func() *TimelineReader {
		a := &fakeSource{name: "messages", events: []TimelineEvent{
			{Kind: TimelineKindToolCall, OccurredAt: at(10), Summary: "m2", SourceTable: "messages", SourceID: "2"},
			{Kind: TimelineKindToolCall, OccurredAt: at(10), Summary: "m1", SourceTable: "messages", SourceID: "1"},
		}}
		b := &fakeSource{name: "task_history", events: []TimelineEvent{
			{Kind: TimelineKindLifecycle, OccurredAt: at(10), Summary: "h1", SourceTable: "task_history", SourceID: "1"},
		}}
		return NewTimelineReader(nil, nil, a, b)
	}

	var first []string
	for run := 0; run < 5; run++ {
		res, err := mk().Read(context.Background(), "task-1", TimelineOpts{})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, e := range res.Events {
			order = append(order, e.Summary)
		}
		if run == 0 {
			first = order
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("ordering changed between reads: %v vs %v", first, order)
		}
	}
	// messages sorts before task_history, and within messages by SourceID.
	if strings.Join(first, ",") != "m1,m2,h1" {
		t.Errorf("unexpected tie-break order: %v", first)
	}
}

func TestTimeline_PartialSourceIsReportedNotFatal(t *testing.T) {
	ok := &fakeSource{name: "messages", events: []TimelineEvent{ev(TimelineKindToolCall, 10, "call")}}
	bad := &fakeSource{name: "human_requests", err: errors.New("table locked")}

	r := NewTimelineReader(nil, nil, ok, bad)
	got, err := r.Read(context.Background(), "task-1", TimelineOpts{})
	if err != nil {
		t.Fatalf("a failing source must not fail the read: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("expected the healthy source's events, got %d", len(got.Events))
	}
	if len(got.PartialSources) != 1 || got.PartialSources[0] != "human_requests" {
		t.Fatalf("the failure must be reported so the reader knows the timeline is incomplete, got %v", got.PartialSources)
	}
}

func TestTimeline_KindAndTimeFilters(t *testing.T) {
	src := &fakeSource{name: "messages", events: []TimelineEvent{
		ev(TimelineKindToolCall, 10, "call"),
		ev(TimelineKindAssistant, 20, "text"),
		ev(TimelineKindToolResult, 30, "result"),
	}}
	r := NewTimelineReader(nil, nil, src)

	got, err := r.Read(context.Background(), "t", TimelineOpts{
		Kinds: []TimelineEventKind{TimelineKindToolCall, TimelineKindToolResult},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("kind filter: got %d, want 2", len(got.Events))
	}

	got, err = r.Read(context.Background(), "t", TimelineOpts{Since: at(15), Until: at(25)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Summary != "text" {
		t.Fatalf("time window filter failed: %+v", got.Events)
	}
}

func TestTimeline_LimitKeepsOldestOrNewest(t *testing.T) {
	src := &fakeSource{name: "messages"}
	for i := 0; i < 10; i++ {
		src.events = append(src.events, ev(TimelineKindToolCall, i, string(rune('a'+i))))
	}
	r := NewTimelineReader(nil, nil, src)

	oldest, err := r.Read(context.Background(), "t", TimelineOpts{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !oldest.Truncated || oldest.TotalMatched != 10 {
		t.Fatalf("expected truncation with TotalMatched=10, got %+v", oldest)
	}
	if oldest.Events[0].Summary != "a" || oldest.Events[2].Summary != "c" {
		t.Errorf("oldest-first limit wrong: %v %v", oldest.Events[0].Summary, oldest.Events[2].Summary)
	}

	newest, err := r.Read(context.Background(), "t", TimelineOpts{Limit: 3, Newest: true})
	if err != nil {
		t.Fatal(err)
	}
	// Still ascending, but the last three.
	if newest.Events[0].Summary != "h" || newest.Events[2].Summary != "j" {
		t.Errorf("newest limit wrong: %v..%v", newest.Events[0].Summary, newest.Events[2].Summary)
	}
}

func TestTimeline_LimitIsCapped(t *testing.T) {
	src := &fakeSource{name: "messages"}
	for i := 0; i < MaxTimelineLimit+50; i++ {
		src.events = append(src.events, ev(TimelineKindToolCall, i, "e"))
	}
	r := NewTimelineReader(nil, nil, src)

	got, err := r.Read(context.Background(), "t", TimelineOpts{Limit: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != MaxTimelineLimit {
		t.Fatalf("an uncapped read must never be permitted: got %d", len(got.Events))
	}
}

func TestTimeline_RequiresTaskID(t *testing.T) {
	r := NewTimelineReader(nil, nil, &fakeSource{name: "messages"})
	if _, err := r.Read(context.Background(), "", TimelineOpts{}); !errors.Is(err, ErrTimelineTaskIDRequired) {
		t.Fatalf("expected ErrTimelineTaskIDRequired, got %v", err)
	}
}

func TestTimeline_NilSourcesIgnored(t *testing.T) {
	// Callers pass optional sources without pre-filtering.
	r := NewTimelineReader(nil, nil, nil, &fakeSource{name: "messages",
		events: []TimelineEvent{ev(TimelineKindToolCall, 1, "call")}}, nil)
	got, err := r.Read(context.Background(), "t", TimelineOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("got %d events", len(got.Events))
	}
}

func TestTimeline_ExcerptTruncatesOnRuneBoundary(t *testing.T) {
	e := TimelineEvent{Detail: strings.Repeat("→", 8)} // 3 bytes each
	got, truncated := e.Excerpt(10)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(got) > 10 {
		t.Fatalf("exceeded cap: %d bytes", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("invalid UTF-8 in excerpt: %q", got)
		}
	}

	full, truncated := TimelineEvent{Detail: "short"}.Excerpt(128)
	if truncated || full != "short" {
		t.Fatalf("short payload altered: %q %v", full, truncated)
	}

	// Zero cap means no truncation — presentation layers opt in.
	all, truncated := e.Excerpt(0)
	if truncated || all != e.Detail {
		t.Error("zero cap should return the full detail")
	}
}

func TestAttribution_RoundTripAndAbsence(t *testing.T) {
	ctx := context.Background()

	if _, ok := AttributionFromContext(ctx); ok {
		t.Error("bare context must report no attribution")
	}
	if id := TaskIDFromContext(ctx); id != "" {
		t.Errorf("expected empty task ID, got %q", id)
	}

	want := Attribution{TaskID: "t1", BoardID: "b1", SessionID: "s1", AgentID: "a1", ParentAgentID: "p1"}
	ctx = ContextWithAttribution(ctx, want)

	got, ok := AttributionFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("round trip failed: %+v ok=%v", got, ok)
	}
	if TaskIDFromContext(ctx) != "t1" {
		t.Error("TaskIDFromContext disagrees with AttributionFromContext")
	}

	// An empty TaskID makes the attribution inert rather than partially set.
	inert := ContextWithAttribution(context.Background(), Attribution{SessionID: "s1"})
	if _, ok := AttributionFromContext(inert); ok {
		t.Error("attribution with no TaskID must be reported as absent")
	}
}
