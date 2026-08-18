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

// RequestHeaderCarrier is the optional interface of transports that mirror
// JSON-RPC body fields into per-request HTTP headers (Streamable HTTP,
// SEP-2243) and scope each request to its own response stream. Two
// 2026-07-28 client behaviors are conditional on it:
//
//   - x-mcp-header validation and Mcp-Param-* mirroring apply only on such
//     transports — clients on other transports (e.g. stdio) MAY ignore the
//     annotations entirely, and must not hide tools over them;
//   - cancelling a subscription is expressed by closing the listen request's
//     response stream, whereas other transports send notifications/cancelled.
type RequestHeaderCarrier interface {
	CarriesRequestHeaders() bool
}

// ReadWriteCloser wraps standard I/O interfaces
type ReadWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}
