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
// Package transport implements the communication layer for MCP protocol.
package transport

import (
	"context"
	"io"
)

// Transport defines the communication layer interface for MCP.
// Implementations include stdio (subprocess), HTTP/SSE, and WebSocket.
type Transport interface {
	// Send sends a message
	Send(ctx context.Context, message []byte) error

	// Receive receives the next message (blocking)
	Receive(ctx context.Context) ([]byte, error)

	// Close closes the transport
	Close() error
}

// ReadWriteCloser wraps standard I/O interfaces
type ReadWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}
