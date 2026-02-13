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
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
)

// readResult holds the result of a single line read from the reader.
type readResult struct {
	data []byte
	err  error
}

// StdioServerTransport implements the Transport interface for server-side
// stdio communication. It reads JSON-RPC messages from a reader (typically
// os.Stdin) and writes responses to a writer (typically os.Stdout).
//
// Each message is a single line of JSON terminated by a newline.
// A persistent reader goroutine runs for the transport's lifetime,
// preventing goroutine leaks when Receive calls are cancelled via context.
type StdioServerTransport struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex // protects writer and closed
	closed bool

	readCh chan readResult // persistent channel from reader goroutine
	once   sync.Once       // ensures reader goroutine starts exactly once
}

// NewStdioServerTransport creates a new server-side stdio transport
// using the provided reader and writer.
func NewStdioServerTransport(r io.Reader, w io.Writer) *StdioServerTransport {
	return &StdioServerTransport{
		reader: bufio.NewReaderSize(r, 1024*1024), // 1MB buffer
		writer: w,
		readCh: make(chan readResult, 1),
	}
}

// startReader launches a persistent goroutine that reads lines from the
// underlying reader and sends them to readCh. The goroutine exits when
// it encounters an error (including io.EOF) or when the reader is closed.
// It is safe to call multiple times; only the first call starts the goroutine.
func (t *StdioServerTransport) startReader() {
	t.once.Do(func() {
		go func() {
			defer close(t.readCh)
			for {
				line, err := t.reader.ReadBytes('\n')
				t.readCh <- readResult{data: line, err: err}
				if err != nil {
					return
				}
			}
		}()
	})
}

// Send writes a JSON-RPC message followed by a newline to the writer.
func (t *StdioServerTransport) Send(_ context.Context, message []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return fmt.Errorf("transport closed")
	}

	// Write message + newline
	if _, err := t.writer.Write(message); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if _, err := t.writer.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}

	return nil
}

// Receive reads the next JSON-RPC message line from the reader.
// Blocks until a message is available or the context is cancelled.
// The underlying reader goroutine is started on the first call and
// persists for the transport's lifetime, avoiding goroutine leaks
// when contexts are cancelled.
func (t *StdioServerTransport) Receive(ctx context.Context) ([]byte, error) {
	t.startReader()

	for {
		// Check if transport is closed
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return nil, fmt.Errorf("transport closed")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result, ok := <-t.readCh:
			if !ok {
				// Channel closed means reader goroutine exited.
				// This happens after an error (including EOF) was already
				// delivered. Subsequent reads return EOF.
				return nil, io.EOF
			}
			if result.err != nil {
				if result.err == io.EOF {
					return nil, io.EOF
				}
				return nil, fmt.Errorf("read message: %w", result.err)
			}
			// Trim trailing newline and carriage return
			line := result.data
			if len(line) > 0 && line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				// Skip empty lines, loop to read the next one
				continue
			}
			return line, nil
		}
	}
}

// Close marks the transport as closed. It does not close the underlying
// reader/writer since those are typically os.Stdin/os.Stdout.
// The persistent reader goroutine will exit naturally when the underlying
// reader is closed or returns an error.
func (t *StdioServerTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}
