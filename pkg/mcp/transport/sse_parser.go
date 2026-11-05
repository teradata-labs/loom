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
// Package transport implements SSE parsing for streamable-http transport.
package transport

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SSE field names, as bytes, so a line can be dispatched without converting
// it to a string first.
var (
	sseFieldID    = []byte("id")
	sseFieldEvent = []byte("event")
	sseFieldData  = []byte("data")
)

// sseLineOverheadBytes is the slack readLine allows ABOVE the data bound when
// deciding how long a single line may be, for the parts of a line that are not
// data: the field prefix ("data: " is 6 bytes, "event: " is 7) and the
// terminator ("\r\n").
//
// The allowance is a fixed addition to the WHOLE bound, not to what is left of
// it. An earlier revision subtracted the data accumulated so far, which made a
// line's admissibility depend on what came before it: with the data already at
// the limit, the remaining budget was 0 and a following `id:` line of any
// length was refused, while the same event with the `id:` line FIRST parsed.
// A non-data line is not charged to the data bound at all, so it must not be
// refused by it.
//
// What this bounds is therefore how much of ONE line may sit in memory before
// a refusal: at most limit+64 bytes. It never decides how much data is
// accepted - ParseEvent charges the data exactly and is the authoritative
// bound. Peak memory for one event is consequently its data (up to the limit)
// plus the line being read (up to limit+64), so a small constant multiple of
// the limit rather than the limit itself.
const sseLineOverheadBytes = 64

// shrinkLineBufBytes is the capacity above which readLine's reusable line
// buffer is released when an event finishes, instead of being kept for the
// next one.
//
// Reuse exists so that a stream of many small lines does not allocate one
// buffer per line. It has a cost the reuse alone does not bound: the buffer
// keeps the capacity of the LARGEST line the parser has ever read, for the
// parser's life. A subscriptions/listen stream is long-lived, so one
// near-limit message followed by silence would hold about limit+64 bytes -
// roughly 16 MiB at the default bound - per open stream, multiplied by every
// open subscription. Measured at a 1 MiB bound before this release: 1.06 MiB
// still held after one near-limit event and 200 small ones.
//
// Releasing above a threshold keeps both properties. Small lines, the shape
// reuse is for, stay far below it and still reuse the buffer. A near-limit
// line is handed back as soon as its event completes, so a quiet stream holds
// nothing. A stream of consecutive large messages regrows the buffer once per
// event, which is one allocation per message and the same order as the
// message itself.
const shrinkLineBufBytes = 64 << 10

// SSEEvent is one parsed Server-Sent Events message.
type SSEEvent struct {
	ID   string // SSE event ID (informational; resumption was removed by MCP 2026-07-28)
	Data []byte // JSON-RPC message
}

// SSEParser parses Server-Sent Events from an HTTP response body.
type SSEParser struct {
	reader *bufio.Reader
	// limit bounds ONE event, not the stream. A streamable-http server
	// delivers a whole JSON-RPC message as a single event, so per-event is
	// the bound that matches a message; bounding the stream would break
	// subscriptions/listen, which stays open for its lifetime and delivers
	// many events. Zero means unbounded.
	limit int64
	// lineBuf is reused by readLine for every line of every event, so a
	// stream of many small lines does not allocate one buffer per line. The
	// slice readLine returns is a view into it, valid only until the next
	// readLine call: callers must copy anything they keep.
	lineBuf []byte
}

// NewSSEParser creates a new SSE parser with no size bound.
//
// Prefer NewSSEParserWithLimit on any stream a remote server controls: an
// unbounded parser will buffer an event of whatever size the server sends.
// This constructor is retained for internal re-parsing of an already-bounded
// buffer, and for callers that have bounded the reader themselves.
func NewSSEParser(r io.Reader) *SSEParser {
	return &SSEParser{
		reader: bufio.NewReader(r),
	}
}

// NewSSEParserWithLimit creates a parser that rejects any single event larger
// than maxBytes with *ResponseTooLargeError. A non-positive maxBytes means
// unbounded.
//
// The bound is on the event's accumulated data, so it is effectively the
// maximum JSON-RPC message size: the parser stops reading at the limit rather
// than returning a truncated message, because a truncated JSON-RPC message
// cannot be decoded and silently losing the tail is worse than a clear error.
func NewSSEParserWithLimit(r io.Reader, maxBytes int64) *SSEParser {
	return &SSEParser{
		reader: bufio.NewReader(r),
		limit:  maxBytes,
	}
}

// ParseEvent reads and parses the next SSE event from the stream.
// Returns io.EOF when the stream is closed.
//
// SSE format:
//
//	id: <event-id>
//	event: message
//	data: {"jsonrpc":"2.0",...}
//
//	(blank line terminates event)
func (p *SSEParser) ParseEvent() (*SSEEvent, error) {
	event := &SSEEvent{}
	// Released whichever way this returns, a refusal included: the event never
	// aliases the line buffer (Data comes from the buffer below, ID is copied).
	defer p.releaseOversizedLineBuf()
	// data accumulates this event's payload in ONE buffer, as it arrives.
	//
	// An earlier revision kept a []string of data lines and joined them at
	// the end. That held three copies at once - every line, the slice of
	// headers addressing them, and the joined result - so a server sending
	// many small data lines cost far more heap than the bound allowed: at the
	// 16 MiB default, 16.7M empty "data:" lines stayed "under the bound" on
	// the joined size while costing well over a GiB. The bound exists to
	// protect the heap, so what it measures must be what is held.
	//
	// data.Len() IS the joined size, so it doubles as the running total the
	// bound is checked against and the two cannot drift apart.
	var data bytes.Buffer
	// sawData distinguishes "no data line yet" from "one empty data line",
	// which data.Len() alone cannot: both are zero bytes.
	var sawData bool

	for {
		line, err := p.readLine()
		if err != nil {
			if err == io.EOF && sawData {
				// Process partial event before EOF
				event.Data = data.Bytes()
				return event, nil
			}
			return nil, err
		}

		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))

		// Empty line terminates event
		if len(line) == 0 {
			if sawData {
				event.Data = data.Bytes()
				return event, nil
			}
			continue
		}

		// Skip comments (lines starting with :)
		if line[0] == ':' {
			continue
		}

		// Parse field
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx == -1 {
			continue // Invalid line, skip
		}

		field := line[:colonIdx]
		value := line[colonIdx+1:]

		// Remove optional leading space after colon
		value = bytes.TrimPrefix(value, []byte(" "))

		switch {
		case bytes.Equal(field, sseFieldID):
			// Copied: value is a view into the reusable line buffer.
			event.ID = string(value)
		case bytes.Equal(field, sseFieldEvent):
			// We expect "message" events, ignore others.
			// Non-message events are silently ignored.
		case bytes.Equal(field, sseFieldData):
			// The event is charged exactly what it will hold: this line's
			// bytes, plus the newline joining it to the previous line.
			//
			// The joining newline is charged too. Without it an empty "data:"
			// line costs nothing, the budget never shrinks, and a server that
			// sends "data:\n" forever grows the event without limit - which
			// is the allocation this bound exists to prevent.
			cost := int64(len(value))
			if sawData {
				cost++ // the "\n" joining this line to the previous one
			}
			if p.limit > 0 && int64(data.Len())+cost > p.limit {
				return nil, &ResponseTooLargeError{Limit: p.limit}
			}
			if sawData {
				data.WriteByte('\n')
			}
			data.Write(value)
			sawData = true
		}
	}
}

// readLine reads one SSE line, refusing to buffer a single line that would
// carry this event past its bound.
//
// The returned slice is a view into the parser's reusable line buffer and is
// valid only until the next readLine call. Keeping any part of it means
// copying it.
//
// The per-line guard matters because a streamable-http server sends an entire
// JSON-RPC message as ONE "data:" line: without it, bufio would buffer that
// whole line before ParseEvent's accumulated-size check ever ran, which is
// exactly the allocation the bound exists to prevent.
//
// The budget is the data bound plus sseLineOverheadBytes, and deliberately
// does NOT shrink as the event's data accumulates: a line is not data until it
// has been read and its field seen, and a non-data line is never charged to
// the data bound. ParseEvent charges the data exactly and is the authoritative
// bound on it.
//
// Reading proceeds in bufio-sized chunks; a byte-at-a-time loop cost one heap
// allocation per byte, and a fresh buffer per line cost one per line.
func (p *SSEParser) readLine() ([]byte, error) {
	p.lineBuf = p.lineBuf[:0]

	// Negative means unbounded, which is what NewSSEParser asks for.
	budget := int64(-1)
	if p.limit > 0 {
		budget = p.limit + sseLineOverheadBytes
	}

	for {
		// ReadSlice returns a view into bufio's buffer, valid only until the
		// next read, so the chunk is copied before looping.
		chunk, err := p.reader.ReadSlice('\n')
		if budget >= 0 && int64(len(p.lineBuf))+int64(len(chunk)) > budget {
			// More line than the budget admits. Refuse rather than buffer it.
			return nil, &ResponseTooLargeError{Limit: p.limit}
		}
		p.lineBuf = append(p.lineBuf, chunk...)
		switch {
		case err == nil:
			return p.lineBuf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			// bufio's buffer filled before the delimiter; keep reading.
			continue
		case errors.Is(err, io.EOF) && len(p.lineBuf) > 0:
			// A final line with no trailing newline.
			return p.lineBuf, nil
		default:
			return nil, err
		}
	}
}

// ParseAll reads all events from the stream until EOF.
func (p *SSEParser) ParseAll() ([]SSEEvent, error) {
	var events []SSEEvent

	for {
		event, err := p.ParseEvent()
		if err != nil {
			if err == io.EOF {
				break
			}
			return events, err
		}
		events = append(events, *event)
	}

	return events, nil
}

// ParseBatch reads multiple events until a blank line or EOF.
// This is useful for batch responses where multiple events are sent together.
// ParseBatch is not bounded by the parser's limit: it buffers a batch and
// re-parses it with an unbounded parser. It has no callers in this module and
// is kept as-is; a bounded caller should use ParseEvent.
func (p *SSEParser) ParseBatch() ([]SSEEvent, error) {
	var events []SSEEvent
	var buf bytes.Buffer
	blankLines := 0

	for {
		line, err := p.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if buf.Len() > 0 {
					// Parse remaining buffer
					parser := NewSSEParser(&buf)
					remaining, _ := parser.ParseAll()
					events = append(events, remaining...)
				}
				return events, nil
			}
			return events, err
		}

		buf.WriteString(line)

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			blankLines++
			// Two blank lines indicate end of batch
			if blankLines >= 2 {
				parser := NewSSEParser(&buf)
				batchEvents, err := parser.ParseAll()
				if err != nil {
					return events, fmt.Errorf("failed to parse batch: %w", err)
				}
				events = append(events, batchEvents...)
				return events, nil
			}
		} else {
			blankLines = 0
		}
	}
}

// releaseOversizedLineBuf hands back the reusable line buffer once it has
// grown past shrinkLineBufBytes, so a parser that read one very large line
// does not hold that capacity for the rest of its life. Buffers at or below
// the threshold are kept, which is what makes a stream of many small lines
// allocation-free.
func (p *SSEParser) releaseOversizedLineBuf() {
	if cap(p.lineBuf) > shrinkLineBufBytes {
		p.lineBuf = nil
	}
}
