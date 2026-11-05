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
