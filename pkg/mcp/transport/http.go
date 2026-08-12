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
// Package transport implements HTTP/SSE transport for MCP servers.
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
)

// HTTPTransport implements Transport over HTTP/SSE.
//
// Deprecated: frozen legacy MCP surface (docs/architecture/mcp-2026-07-28-migration.md §9.2);
// removal no earlier than 2027-07-28. The HTTP+SSE transport (2024-11-05) is
// Deprecated under the protocol's feature lifecycle policy (SEP-2596);
// migrate to StreamableHTTPTransport.
type HTTPTransport struct {
	endpoint   string
	headers    map[string]string
	sseClient  *sse.Client
	httpClient *http.Client

	events chan []byte
	errors chan error

	mu     sync.Mutex
	closed bool

	logger *zap.Logger
}

// HTTPConfig configures HTTP transport
type HTTPConfig struct {
	Endpoint string            // HTTP endpoint
	Headers  map[string]string // Custom headers
	SSEPath  string            // SSE endpoint path (default: /sse)
	Logger   *zap.Logger       // Logger
}

// NewHTTPTransport creates a new HTTP/SSE transport.
//
// Deprecated: frozen legacy MCP surface (docs/architecture/mcp-2026-07-28-migration.md §9.2);
// removal no earlier than 2027-07-28. Use NewStreamableHTTPTransport.
func NewHTTPTransport(config HTTPConfig) (*HTTPTransport, error) {
	if config.SSEPath == "" {
		config.SSEPath = "/sse"
	}

	logger := config.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	sseClient := sse.NewClient(config.Endpoint + config.SSEPath)

	// Set custom headers
	for k, v := range config.Headers {
		sseClient.Headers[k] = v
	}

	t := &HTTPTransport{
		endpoint:  config.Endpoint,
		headers:   config.Headers,
		sseClient: sseClient,
		httpClient: &http.Client{
			Timeout: 10 * time.Second, // Prevent hanging on unreachable servers
		},
		events: make(chan []byte, 100),
		errors: make(chan error, 1),
		logger: logger,
	}

	// Setup disconnect handler
	sseClient.OnDisconnect(func(c *sse.Client) {
		t.logger.Warn("SSE disconnected")
		select {
		case t.errors <- fmt.Errorf("SSE disconnected"):
		default:
		}
	})

	// Subscribe to SSE events asynchronously with timeout
	// This prevents blocking if the server is unreachable
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		logger.Debug("Attempting SSE subscription", zap.String("endpoint", config.Endpoint+config.SSEPath))

		err := sseClient.SubscribeWithContext(ctx, "message", func(msg *sse.Event) {
			select {
			case t.events <- msg.Data:
			case <-ctx.Done():
				return
			}
		})

		if err != nil {
			logger.Warn("Failed to subscribe to SSE (will retry on first message)",
				zap.String("endpoint", config.Endpoint),
				zap.Error(err))
			// Don't send to errors channel - let it fail on first actual use
			// This allows the server to start even if this MCP server is down
		} else {
			logger.Info("HTTP/SSE transport connected", zap.String("endpoint", config.Endpoint))
		}
	}()

	logger.Debug("HTTP/SSE transport created (connecting in background)", zap.String("endpoint", config.Endpoint))

	return t, nil
}

// Send implements Transport (POST request)
func (h *HTTPTransport) Send(ctx context.Context, message []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return fmt.Errorf("transport closed")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", h.endpoint+"/messages", bytes.NewReader(message))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	for key, value := range h.headers {
		req.Header.Set(key, value)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP error %d: %s", resp.StatusCode, body)
	}

	return nil
}

// Receive implements Transport (SSE event)
func (h *HTTPTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err, ok := <-h.errors:
		if !ok {
			return nil, io.EOF // Channel closed
		}
		return nil, err
	case data, ok := <-h.events:
		if !ok {
			return nil, io.EOF // Channel closed
		}
		return data, nil
	}
}

// Close implements Transport
func (h *HTTPTransport) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil
	}
	h.closed = true

	h.logger.Info("closing HTTP/SSE transport")

	// The channels are deliberately never closed: the SSE subscription
	// callback sends into h.events from the sse library's goroutine, which
	// Close does not wait for, so closing here races an in-flight event into
	// a send-on-closed-channel panic. Receive callers are select-guarded by
	// their contexts (the client's receive loop exits via its own cancelled
	// context before the transport closes) and need no closure signal.

	return nil
}
