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
	"fmt"
	"io"
	"strings"
)

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
			// size shrinks the next line's budget, so a multi-line event is
			// bounded in total and not merely line by line. readLine is the
			// single refusal point; no check is needed here, because a line
			// that would carry size past the limit is never read.
			size += int64(len(value))
			dataLines = append(dataLines, value)
		}
	}
}

// readLine reads one SSE line, refusing to buffer a single line that would
// carry this event past its bound.
//
// The per-line guard matters because a streamable-http server sends an entire
// JSON-RPC message as ONE "data:" line: without it, bufio would buffer that
// whole line before the accumulated-size check above ever ran, which is
// exactly the allocation the bound exists to prevent.
func (p *SSEParser) readLine(accumulated int64) (string, error) {
	if p.limit <= 0 {
		return p.reader.ReadString('\n')
	}
	remaining := p.limit - accumulated
	if remaining < 0 {
		remaining = 0
	}
	// +2 admits the delimiter and a CR; +1 more lets an exactly-at-limit line
	// be read so the size check, not the read, decides.
	lr := io.LimitedReader{R: p.reader, N: remaining + 3}
	var b strings.Builder
	for {
		c, err := readByte(&lr)
		if err != nil {
			if err == io.EOF && b.Len() > 0 {
				// A final line with no trailing newline.
				return b.String(), nil
			}
			return "", err
		}
		b.WriteByte(c)
		if c == '\n' {
			return b.String(), nil
		}
		if lr.N <= 0 {
			// More line to come, but no budget for it.
			return "", &ResponseTooLargeError{Limit: p.limit}
		}
	}
}

// readByte reads a single byte from r.
func readByte(r io.Reader) (byte, error) {
	var buf [1]byte
	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			return buf[0], nil
		}
		if err != nil {
			return 0, err
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
