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

	"github.com/r3labs/sse/v2"
	"go.uber.org/zap"
)

// HTTPTransport implements Transport over HTTP/SSE
type HTTPTransport struct {
	endpoint   string
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

// NewHTTPTransport creates a new HTTP/SSE transport
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
		endpoint:   config.Endpoint,
		sseClient:  sseClient,
		httpClient: &http.Client{},
		events:     make(chan []byte, 100),
		errors:     make(chan error, 1),
		logger:     logger,
	}

	// Subscribe to SSE events
	err := sseClient.SubscribeWithContext(context.Background(), "message", func(msg *sse.Event) {
		t.events <- msg.Data
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to SSE: %w", err)
	}

	sseClient.OnDisconnect(func(c *sse.Client) {
		t.logger.Warn("SSE disconnected")
		select {
		case t.errors <- fmt.Errorf("SSE disconnected"):
		default:
		}
	})

	logger.Info("HTTP/SSE transport connected", zap.String("endpoint", config.Endpoint))

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

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
	case err := <-h.errors:
		return nil, err
	case data := <-h.events:
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

	// Close channels
	close(h.events)
	close(h.errors)

	return nil
}
