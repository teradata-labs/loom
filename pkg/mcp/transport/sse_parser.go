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

// sseLineOverheadBytes is the slack readLine allows ABOVE the remaining data
// bound, for the parts of a line that are not data: the field prefix
// ("data: " is 6 bytes, "event: " is 7) and the terminator ("\r\n").
//
// It exists because charging those bytes to the data bound refused a message
// whose data was exactly at the limit. The data is charged exactly, in
// ParseEvent, so this constant decides only how much of ONE line may sit in
// memory before the refusal (at most limit+64 bytes), never how much data is
// accepted.
const sseLineOverheadBytes = 64

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
	var dataLines []string
	// size accumulates this event's data bytes. Reset per call, so the bound
	// is per message and a long-lived stream is not capped in total.
	var size int64

	for {
		line, err := p.readLine(size)
		if err != nil {
			if err == io.EOF && len(dataLines) > 0 {
				// Process partial event before EOF
				event.Data = []byte(strings.Join(dataLines, "\n"))
				return event, nil
			}
			return nil, err
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		// Empty line terminates event
		if line == "" {
			if len(dataLines) > 0 {
				event.Data = []byte(strings.Join(dataLines, "\n"))
				return event, nil
			}
			continue
		}

		// Skip comments (lines starting with :)
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse field
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue // Invalid line, skip
		}

		field := line[:colonIdx]
		value := line[colonIdx+1:]

		// Remove optional leading space after colon
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "id":
			event.ID = value
		case "event":
			// We expect "message" events, ignore others
			// Non-message events are silently ignored
			_ = value
		case "data":
			// size is exactly what strings.Join(dataLines, "\n") will cost,
			// so the bound is the JSON-RPC message's own size.
			//
			// The joining newline is charged too. Without it an empty "data:"
			// line costs nothing: the budget never shrinks, and a server that
			// sends "data:\n" forever grows dataLines and the joined result
			// without limit, which is the allocation this bound exists to
			// prevent.
			cost := int64(len(value))
			if len(dataLines) > 0 {
				cost++ // the "\n" joining this line to the previous one
			}
			if p.limit > 0 && size+cost > p.limit {
				return nil, &ResponseTooLargeError{Limit: p.limit}
			}
			size += cost
			dataLines = append(dataLines, value)
		}
	}
}

// readLine reads one SSE line, refusing to buffer a single line that would
// carry this event past its bound.
//
// The per-line guard matters because a streamable-http server sends an entire
// JSON-RPC message as ONE "data:" line: without it, bufio would buffer that
// whole line before ParseEvent's accumulated-size check ever ran, which is
// exactly the allocation the bound exists to prevent.
//
// The budget is what is LEFT of the data bound plus sseLineOverheadBytes, so
// the field prefix and the terminator are not charged to the data itself.
// Reading proceeds in bufio-sized chunks; a byte-at-a-time loop cost one heap
// allocation per byte on every SSE response.
func (p *SSEParser) readLine(accumulated int64) (string, error) {
	if p.limit <= 0 {
		return p.reader.ReadString('\n')
	}
	budget := p.limit - accumulated
	if budget < 0 {
		budget = 0
	}
	budget += sseLineOverheadBytes

	var buf []byte
	for {
		// ReadSlice returns a view into bufio's buffer, valid only until the
		// next read, so the chunk is copied before looping.
		chunk, err := p.reader.ReadSlice('\n')
		if int64(len(buf))+int64(len(chunk)) > budget {
			// More line than the budget admits. Refuse rather than buffer it.
			return "", &ResponseTooLargeError{Limit: p.limit}
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return string(buf), nil
		case errors.Is(err, bufio.ErrBufferFull):
			// bufio's buffer filled before the delimiter; keep reading.
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			// A final line with no trailing newline.
			return string(buf), nil
		default:
			return "", err
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
