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

// SSEParser parses Server-Sent Events from an HTTP response body.
type SSEParser struct {
	reader *bufio.Reader
}

// NewSSEParser creates a new SSE parser.
func NewSSEParser(r io.Reader) *SSEParser {
	return &SSEParser{
		reader: bufio.NewReader(r),
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

	for {
		line, err := p.reader.ReadString('\n')
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
			if value != "message" && value != "" {
				// Non-message events can be ignored or logged
			}
		case "data":
			dataLines = append(dataLines, value)
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
