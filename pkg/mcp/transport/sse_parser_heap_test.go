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

package transport

import (
	"runtime"
	"strings"
	"testing"
)

// allocatedBy reports how many bytes were allocated while f ran. TotalAlloc is
// cumulative and never decreases, so it measures the allocation WORK done, not
// what happened to survive a GC - which is the number that matters here,
// because the failure mode is a pod killed while a response is being parsed.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The bound exists to protect the heap, so what it measures must be what is
// held. An earlier revision bounded the JOINED size while keeping every line
// plus a slice addressing them plus the joined copy, so an event could stay
// "under the bound" and still cost orders of magnitude more memory than it.
//
// Many empty data lines is the cheapest way to show the gap: on the wire they
// are 6 bytes each and the bound counts 1 each, so the bound is reached only
// after a huge number of lines.
func TestSSEParser_ManyEmptyDataLinesStayWithinTheBound(t *testing.T) {
	const limit = 1 << 20 // 1 MiB

	// limit+1 empty data lines => limit joining newlines => exactly at the
	// bound, so this is the largest event the limit admits in this shape.
	body := strings.Repeat("data:\n", limit+1) + "\n"

	var ev *SSEEvent
	var err error
	allocated := allocatedBy(func() {
		ev, err = NewSSEParserWithLimit(strings.NewReader(body), limit).ParseEvent()
	})

	if err != nil {
		t.Fatalf("an event exactly at the bound must parse: %v", err)
	}
	if got := int64(len(ev.Data)); got != limit {
		t.Fatalf("want %d joined bytes, got %d", limit, got)
	}

	// A small multiple of the limit. The event itself costs `limit`, and the
	// buffer reaches it by doubling, so ~2-3x is expected. The pre-fix
	// implementation cost ~50x this limit in the same shape.
	const ceiling = 16 * limit
	t.Logf("wire bytes %d, bound %d, allocated %d (%.1fx the bound)",
		len(body), limit, allocated, float64(allocated)/float64(limit))
	if allocated > ceiling {
		t.Fatalf("parsing an event at a %d byte bound allocated %d bytes (%.1fx the bound); want <= %dx",
			limit, allocated, float64(allocated)/float64(limit), ceiling/limit)
	}
}

// The same property for the other shape: one enormous data line. The line is
// buffered once and copied into the event once, so a small constant multiple
// is the most it may cost.
func TestSSEParser_OneLargeDataLineStaysWithinTheBound(t *testing.T) {
	const limit = 1 << 20 // 1 MiB

	body := "data: " + strings.Repeat("x", limit) + "\n\n"

	var ev *SSEEvent
	var err error
	allocated := allocatedBy(func() {
		ev, err = NewSSEParserWithLimit(strings.NewReader(body), limit).ParseEvent()
	})

	if err != nil {
		t.Fatalf("a single line exactly at the bound must parse: %v", err)
	}
	if len(ev.Data) != limit {
		t.Fatalf("want %d bytes, got %d", limit, len(ev.Data))
	}

	const ceiling = 16 * limit
	t.Logf("one %d byte line, allocated %d (%.1fx the bound)",
		limit, allocated, float64(allocated)/float64(limit))
	if allocated > ceiling {
		t.Fatalf("one %d byte line allocated %d bytes (%.1fx the bound); want <= %dx",
			limit, allocated, float64(allocated)/float64(limit), ceiling/limit)
	}
}

// A long stream of small events must not accumulate anything across events:
// the line buffer is reused, and each event's data buffer is released with the
// event. This is what keeps a subscriptions/listen stream bounded for its
// lifetime.
func TestSSEParser_ManyEventsDoNotAccumulate(t *testing.T) {
	const events = 20000
	body := strings.Repeat("data: {\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n\n", events)

	parser := NewSSEParserWithLimit(strings.NewReader(body), 1<<20)

	var parsed int
	allocated := allocatedBy(func() {
		for {
			ev, err := parser.ParseEvent()
			if err != nil {
				return
			}
			if len(ev.Data) > 0 {
				parsed++
			}
		}
	})

	if parsed != events {
		t.Fatalf("want %d events parsed, got %d", events, parsed)
	}

	// Each event's payload is 35 bytes. Allowing a generous 16x the wire size
	// still catches per-line or per-event buffers that are never reused.
	ceiling := uint64(len(body)) * 16
	t.Logf("%d events, wire %d bytes, allocated %d (%.1fx the wire)",
		events, len(body), allocated, float64(allocated)/float64(len(body)))
	if allocated > ceiling {
		t.Fatalf("%d events over %d wire bytes allocated %d bytes (%.1fx); want <= 16x",
			events, len(body), allocated, float64(allocated)/float64(len(body)))
	}
}

// Reuse of the line buffer bounds allocation work, but not what is RETAINED:
// the buffer keeps the capacity of the largest line the parser ever read, for
// the parser's life. That is bounded, not a leak, but a subscriptions/listen
// stream is long-lived, so one near-limit message followed by silence would
// hold about limit+64 bytes per open stream - roughly 16 MiB at the default
// bound, multiplied by every open subscription.
//
// The assertion is on cap(lineBuf), which IS the retained capacity and is
// deterministic. A HeapInuse figure is logged alongside it as corroboration;
// it is not asserted on, because it also counts each parser's bufio buffer and
// its reader, and would be flaky.
func TestSSEParser_LargeLineCapacityIsNotRetained(t *testing.T) {
	const limit = 1 << 20 // 1 MiB

	// One near-limit event, then many small ones: the shape a subscription
	// takes after a single large message.
	stream := func() string {
		var b strings.Builder
		b.WriteString("data: " + strings.Repeat("A", limit-16) + "\n\n")
		for i := 0; i < 200; i++ {
			b.WriteString("data: {\"small\":1}\n\n")
		}
		return b.String()
	}

	drain := func(p *SSEParser) int {
		n := 0
		for {
			if _, err := p.ParseEvent(); err != nil {
				return n
			}
			n++
		}
	}

	p := NewSSEParserWithLimit(strings.NewReader(stream()), limit)
	if got := drain(p); got != 201 {
		t.Fatalf("want 201 events parsed, got %d", got)
	}

	if cap(p.lineBuf) > shrinkLineBufBytes {
		t.Fatalf("a near-limit line's capacity is still held after the event finished: "+
			"cap(lineBuf) = %d bytes, above the %d byte release threshold (limit was %d). "+
			"A quiet subscription would hold this for its lifetime.",
			cap(p.lineBuf), shrinkLineBufBytes, limit)
	}
	t.Logf("one near-limit event + 200 small: cap(lineBuf) retained = %d bytes (limit %d, limit+overhead %d)",
		cap(p.lineBuf), limit, limit+sseLineOverheadBytes)

	// Many open streams multiply whatever one stream holds.
	const streams = 20
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	parsers := make([]*SSEParser, 0, streams)
	for i := 0; i < streams; i++ {
		q := NewSSEParserWithLimit(strings.NewReader(stream()), limit)
		drain(q)
		parsers = append(parsers, q)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	total := 0
	for _, q := range parsers {
		total += cap(q.lineBuf)
	}
	runtime.KeepAlive(parsers)

	if int64(total) > int64(streams)*shrinkLineBufBytes {
		t.Fatalf("%d idle parsers retain %d bytes of line buffer; each near-limit line's "+
			"capacity is being held for the parser's life", streams, total)
	}
	t.Logf("%d idle parsers: line buffers total %d bytes; HeapInuse delta %d bytes "+
		"(includes each parser's bufio buffer and its reader, so not asserted on)",
		streams, total, int64(after.HeapInuse)-int64(before.HeapInuse))
}

// The release threshold has two jobs, and the test above pins only one. This
// pins the other: a buffer at or below the threshold must be KEPT, because
// reuse across many small lines is what makes a stream of them allocation-free
// in the first place. Releasing unconditionally would still pass every bound
// above - it measured 4.2x instead of 2.9x on the 20k-event stream, inside the
// ceiling - so only a direct check on reuse catches it.
func TestSSEParser_SmallLineBufferIsReusedNotReleased(t *testing.T) {
	const limit = 1 << 20

	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("data: {\"small\":1}\n\n")
	}

	p := NewSSEParserWithLimit(strings.NewReader(b.String()), limit)
	for {
		if _, err := p.ParseEvent(); err != nil {
			break
		}
	}

	// Small lines never reach the threshold, so the buffer must still be there
	// for the next line to append into.
	if cap(p.lineBuf) == 0 {
		t.Fatal("the line buffer was released after a small line; a stream of small " +
			"lines then allocates one buffer per line, which is what reusing it avoids")
	}
	if cap(p.lineBuf) > shrinkLineBufBytes {
		t.Fatalf("a small-line stream should not grow the buffer past the threshold: cap = %d", cap(p.lineBuf))
	}
	t.Logf("500 small events: cap(lineBuf) = %d bytes, kept for reuse", cap(p.lineBuf))
}
