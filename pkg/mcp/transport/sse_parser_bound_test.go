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
	"errors"
	"strings"
	"testing"
)

// An empty "data:" line costs one byte of budget, not zero: the newline that
// joins it to the previous line. Without that charge the budget never shrinks
// and a server can grow the event without limit while staying under any cap.
func TestSSEParser_EmptyDataLinesAreBounded(t *testing.T) {
	const limit = 64
	stream := strings.Repeat("data:\n", 100000)

	p := NewSSEParserWithLimit(strings.NewReader(stream), limit)
	ev, err := p.ParseEvent()

	if err == nil {
		t.Fatalf("empty data lines bypassed the bound: accepted an event with len(Data)=%d under limit %d", len(ev.Data), limit)
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != limit {
		t.Fatalf("want *ResponseTooLargeError{Limit:%d}, got %#v", limit, err)
	}
}

// The refusal must land at the bound, not merely somewhere: limit+1 empty data
// lines are one byte over, because the first line is free of a joining newline.
func TestSSEParser_EmptyDataLinesRefuseAtTheBound(t *testing.T) {
	const limit = 8

	// limit+1 lines => limit joining newlines => exactly at the bound.
	atBound := strings.Repeat("data:\n", limit+1) + "\n"
	ev, err := NewSSEParserWithLimit(strings.NewReader(atBound), limit).ParseEvent()
	if err != nil {
		t.Fatalf("exactly at the bound must parse, got %v", err)
	}
	if got := int64(len(ev.Data)); got != limit {
		t.Fatalf("want %d joined bytes, got %d", limit, got)
	}

	// One more line is one byte over.
	over := strings.Repeat("data:\n", limit+2) + "\n"
	if _, err := NewSSEParserWithLimit(strings.NewReader(over), limit).ParseEvent(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("one byte over the bound must be refused, got %v", err)
	}
}

// The bound is on the DATA, so the field prefix and the terminator must not be
// charged to it. A message whose data is exactly at the limit has to parse,
// under either terminator, which is the rule the JSON path already follows.
func TestSSEParser_ExactlyAtLimitParses(t *testing.T) {
	payload := strings.Repeat("x", 100)
	limit := int64(len(payload))

	for _, tc := range []struct{ name, term string }{
		{"LF", "\n\n"},
		{"CRLF", "\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSSEParserWithLimit(strings.NewReader("data: "+payload+tc.term), limit)
			ev, err := p.ParseEvent()
			if err != nil {
				t.Fatalf("data of exactly the limit (%d) must parse, got %v", limit, err)
			}
			if string(ev.Data) != payload {
				t.Fatalf("payload corrupted: got %d bytes, want %d", len(ev.Data), len(payload))
			}
		})
	}

	t.Run("one byte over is refused", func(t *testing.T) {
		p := NewSSEParserWithLimit(strings.NewReader("data: "+payload+"y\n\n"), limit)
		if _, err := p.ParseEvent(); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("one byte over must be refused, got %v", err)
		}
	})
}

// A multi-line event is bounded on its joined total, including the newlines
// that join it, so the bound means the JSON-RPC message's own size.
func TestSSEParser_MultiLineBoundIsTheJoinedSize(t *testing.T) {
	// Three lines of 10 bytes joined by 2 newlines = 32 bytes.
	line := strings.Repeat("a", 10)
	body := "data: " + line + "\ndata: " + line + "\ndata: " + line + "\n\n"

	ev, err := NewSSEParserWithLimit(strings.NewReader(body), 32).ParseEvent()
	if err != nil {
		t.Fatalf("joined size 32 under limit 32 must parse, got %v", err)
	}
	if len(ev.Data) != 32 {
		t.Fatalf("want 32 joined bytes, got %d", len(ev.Data))
	}

	if _, err := NewSSEParserWithLimit(strings.NewReader(body), 31).ParseEvent(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("joined size 32 under limit 31 must be refused, got %v", err)
	}
}

// The bounded read must cost allocations in proportion to the number of bufio
// chunks, not to the number of bytes. A byte-at-a-time loop cost one heap
// allocation per byte, which is ~1M for this input.
func TestSSEParser_BoundedReadDoesNotAllocatePerByte(t *testing.T) {
	const size = 1 << 20
	payload := strings.Repeat("x", size)
	body := "data: " + payload + "\n\n"

	allocs := testing.AllocsPerRun(3, func() {
		p := NewSSEParserWithLimit(strings.NewReader(body), int64(size))
		if _, err := p.ParseEvent(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Generous ceiling: the chunked read is O(log n) reallocations plus the
	// bufio buffer and the final string. Per-byte allocation would be ~1e6.
	const ceiling = 1000
	if allocs > ceiling {
		t.Fatalf("bounded read allocated %.0f times for %d bytes; want <= %d (per-byte would be ~%d)", allocs, size, ceiling, size)
	}
	t.Logf("allocs/op for a %d-byte line: %.0f", size, allocs)
}

// A single oversized line is still refused before it is buffered, which is the
// reason the guard sits at line level at all.
func TestSSEParser_SingleOversizedLineRefused(t *testing.T) {
	body := "data: " + strings.Repeat("x", 1<<20) + "\n\n"
	if _, err := NewSSEParserWithLimit(strings.NewReader(body), 1024).ParseEvent(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("an oversized single line must be refused, got %v", err)
	}
}

// A non-data line is never charged to the data bound, so it must not be
// refused by it. An earlier revision subtracted the accumulated data from the
// line budget, which made a line's admissibility depend on what came before:
// with data already at the limit the budget was 0, and a following `id:` line
// of any length was refused - while the same event with the `id:` line FIRST
// parsed. Order decided the outcome.
func TestSSEParser_NonDataLineIsNotChargedToTheDataBound(t *testing.T) {
	const limit = 100
	data := strings.Repeat("x", limit)
	id := strings.Repeat("i", 80)

	t.Run("data at the limit, then a long id line", func(t *testing.T) {
		ev, err := NewSSEParserWithLimit(
			strings.NewReader("data: "+data+"\nid: "+id+"\n\n"), limit).ParseEvent()
		if err != nil {
			t.Fatalf("a non-data line after data at the limit must parse, got %v", err)
		}
		if string(ev.Data) != data {
			t.Fatalf("data corrupted: got %d bytes, want %d", len(ev.Data), len(data))
		}
		if ev.ID != id {
			t.Fatalf("the id must survive: got %d bytes, want %d", len(ev.ID), len(id))
		}
	})

	t.Run("the same event in the other order", func(t *testing.T) {
		ev, err := NewSSEParserWithLimit(
			strings.NewReader("id: "+id+"\ndata: "+data+"\n\n"), limit).ParseEvent()
		if err != nil {
			t.Fatalf("order must not decide the outcome, got %v", err)
		}
		if string(ev.Data) != data {
			t.Fatalf("data corrupted: got %d bytes", len(ev.Data))
		}
	})

	t.Run("the data bound itself still holds", func(t *testing.T) {
		// One byte more of DATA is still refused: moving the line allowance
		// must not have loosened the bound on data.
		_, err := NewSSEParserWithLimit(
			strings.NewReader("data: "+data+"y\n\n"), limit).ParseEvent()
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("one byte of data over the bound must be refused, got %v", err)
		}
	})

	t.Run("one line may still not be buffered without limit", func(t *testing.T) {
		// The per-line guard is what stops an enormous single line being
		// buffered before ParseEvent could charge it.
		_, err := NewSSEParserWithLimit(
			strings.NewReader("id: "+strings.Repeat("i", 10*limit)+"\n\n"), limit).ParseEvent()
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("a line far over the bound must still be refused, got %v", err)
		}
	})
}
