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
// Package transport implements streamable-http transport for MCP servers.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ErrSessionExpired indicates the server session has expired (HTTP 404).
var ErrSessionExpired = errors.New("session expired")

// StreamableHTTPTransport implements the MCP streamable-http transport with
// legacy session management (pre-2026 revisions).
type StreamableHTTPTransport struct {
	endpoint string
	client   *http.Client

	// Session management
	sessionMgr *SessionManager

	// Message channels
	messages chan []byte
	errors   chan error

	// Lifecycle
	mu     sync.Mutex
	closed bool
	logger *zap.Logger

	// Stream management
	activeStreams sync.WaitGroup
	streamCancel  context.CancelFunc
	streamCtx     context.Context

	// Configuration
	enableSessions bool
	headers        map[string]string // custom headers (e.g. Authorization) sent on every request
}

// StreamableHTTPConfig configures streamable-http transport.
type StreamableHTTPConfig struct {
	Endpoint       string            // MCP endpoint URL
	Headers        map[string]string // Custom headers
	EnableSessions bool              // Enable session management
	// EnableResumption is a no-op. SSE resumption never had a read path in
	// Loom (no Last-Event-ID was ever sent) and the 2026-07-28 revision
	// removes resumption from the protocol; the field is retained only so
	// existing configs keep loading. It is removed after the deprecation
	// window (2027-07-28).
	EnableResumption bool
	Logger           *zap.Logger // Logger
}

// NewStreamableHTTPTransport creates a new streamable-http transport.
func NewStreamableHTTPTransport(config StreamableHTTPConfig) (*StreamableHTTPTransport, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}

	logger := config.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())

	if config.EnableResumption {
		logger.Warn("enable_resumption is deprecated and has no effect: SSE resumption was removed by MCP 2026-07-28 and never had a read path in this client")
	}

	t := &StreamableHTTPTransport{
		endpoint:       config.Endpoint,
		client:         &http.Client{},
		sessionMgr:     NewSessionManager(),
		messages:       make(chan []byte, 100),
		errors:         make(chan error, 1),
		logger:         logger,
		streamCtx:      streamCtx,
		streamCancel:   streamCancel,
		enableSessions: config.EnableSessions,
		headers:        config.Headers,
	}

	logger.Info("Streamable HTTP transport created", zap.String("endpoint", config.Endpoint))

	return t, nil
}

// Send implements Transport by sending a JSON-RPC message via POST.
func (t *StreamableHTTPTransport) Send(ctx context.Context, message []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("transport closed")
	}
	t.mu.Unlock()

	// Build POST request
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// Custom headers (e.g. Authorization for an authenticated remote server).
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	// Standard MCP request headers (2026-07-28, SEP-2243). Required on the new
	// revision so gateways can route on Mcp-Method without parsing bodies;
	// older servers ignore unknown headers, so they are sent unconditionally.
	if method, name := requestHeaderFields(message); method != "" {
		req.Header.Set("Mcp-Method", method)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}

	// Per-request headers from the client layer: MCP-Protocol-Version and
	// Mcp-Param-* values mirrored from x-mcp-header tool parameters.
	for k, v := range ExtraHeadersFromContext(ctx) {
		req.Header.Set(k, v)
	}

	// Add session ID if we have one
	if sessionID := t.sessionMgr.GetSessionID(); sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	t.logger.Debug("Sending POST request",
		zap.String("endpoint", t.endpoint),
		zap.Int("message_size", len(message)),
		zap.Bool("has_session", t.sessionMgr.HasSession()))

	// Send request
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request failed: %w", err)
	}
	// Body ownership: the SSE path transfers the live response body to the
	// stream goroutine, which closes it when the stream ends; every other
	// path closes it here.
	bodyOwned := false
	defer func() {
		if !bodyOwned {
			_ = resp.Body.Close()
		}
	}()

	// Handle HTTP errors
	if handled, err := t.handleHTTPStatus(ctx, resp); handled || err != nil {
		return err
	}

	// Adopt a session ID whenever a legacy server mints one and none is held.
	// Only the initialize response carries this header (2026-07-28 servers
	// never send it), but which POST that is depends on probe order — the
	// server/discover probe precedes initialize on auto-negotiated
	// connections, so gating on "first request" would discard the session and
	// break every subsequent call against strict legacy session servers. This
	// also re-adopts a fresh session after ErrSessionExpired cleared the old
	// one and the client re-initialized.
	if t.enableSessions && !t.sessionMgr.HasSession() {
		if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
			if err := t.sessionMgr.SetSessionID(sessionID); err != nil {
				t.logger.Warn("Invalid session ID from server", zap.Error(err))
			} else {
				t.logger.Info("Session established", zap.String("session_id", sessionID))
			}
		}
	}

	// Handle response based on Content-Type. Parse the media type properly:
	// servers legitimately send parameters such as
	// "text/event-stream; charset=utf-8".
	contentType := resp.Header.Get("Content-Type")
	mediaType := contentType
	if parsed, _, mimeErr := mime.ParseMediaType(contentType); mimeErr == nil {
		mediaType = parsed
	}
	t.logger.Debug("Received HTTP response",
		zap.String("content-type", contentType),
		zap.Int("status", resp.StatusCode))

	switch mediaType {
	case "text/event-stream":
		// SSE response stream. The body must be parsed live, never buffered:
		// a subscriptions/listen response intentionally stays open for the
		// stream's lifetime, so reading until close would block forever and
		// no notification would ever be delivered.
		t.logger.Debug("Handling SSE stream response")
		bodyOwned = true
		return t.handleSSEStream(ctx, resp.Body, requestID(message))

	case "application/json":
		// Single JSON response
		t.logger.Debug("Handling JSON response")
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
		t.logger.Debug("Read JSON response data", zap.Int("bytes", len(data)))

		// Skip empty JSON responses (HTTP acknowledgments for notifications)
		// Empty responses with 202 Accepted are valid acknowledgments per MCP spec
		if len(data) == 0 {
			t.logger.Debug("Skipping empty JSON response (notification acknowledgment)")
			return nil
		}

		select {
		case t.messages <- data:
			t.logger.Debug("JSON response sent to channel")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}

	default:
		return fmt.Errorf("unexpected Content-Type: %s", contentType)
	}
}

// CarriesRequestHeaders implements RequestHeaderCarrier: Streamable HTTP
// mirrors body fields into per-request headers and scopes each request to
// its own response stream.
func (t *StreamableHTTPTransport) CarriesRequestHeaders() bool { return true }

// Receive implements Transport by receiving the next message.
func (t *StreamableHTTPTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-t.errors:
		return nil, err
	case msg := <-t.messages:
		t.logger.Debug("Received message from transport", zap.Int("size", len(msg)))
		return msg, nil
	}
}

// Close implements Transport.
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	t.logger.Info("Closing streamable HTTP transport")

	// Cancel all streams
	t.streamCancel()

	// Wait for streams to finish
	t.activeStreams.Wait()

	// Terminate session if enabled
	if t.enableSessions && t.sessionMgr.HasSession() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = t.terminateSession(ctx) // Best effort
	}

	// Close channels
	close(t.messages)
	close(t.errors)

	return nil
}

// handleSSEStream processes an SSE response stream. expectedID is the
// JSON-RPC id of the request that opened the stream (nil for notifications);
// if the stream ends without delivering that request's final response, a
// CodeStreamLost error response is synthesized so the pending request fails
// promptly instead of hanging until its context deadline. Resumption was
// removed by the 2026-07-28 revision, so re-issuing is the only recovery.
func (t *StreamableHTTPTransport) handleSSEStream(ctx context.Context, body io.ReadCloser, expectedID json.RawMessage) error {
	t.logger.Debug("Starting SSE stream handler")
	t.activeStreams.Add(1)
	go func() {
		sawFinal := expectedID == nil
		defer t.activeStreams.Done()
		defer func() { _ = body.Close() }()
		defer func() {
			if !sawFinal {
				t.synthesizeStreamLost(ctx, expectedID)
			}
		}()

		// The parser blocks in a body read between events; closing the body
		// is the only way to interrupt it. Without this, Close() would wait
		// on activeStreams until the server ended the stream — indefinitely
		// for a subscriptions/listen stream. Cancelling the request context
		// is also how a client cancels an HTTP subscription (closing the SSE
		// stream is the cancellation signal under 2026-07-28).
		unblockDone := make(chan struct{})
		defer close(unblockDone)
		go func() {
			select {
			case <-t.streamCtx.Done():
				_ = body.Close()
			case <-ctx.Done():
				_ = body.Close()
			case <-unblockDone:
			}
		}()

		parser := NewSSEParser(body)

		for {
			t.logger.Debug("Parsing SSE event")
			event, err := parser.ParseEvent()
			if err != nil {
				// Deliberate teardown — transport close or request
				// cancellation — is not a lost stream: no synthesis.
				if t.streamCtx.Err() != nil || ctx.Err() != nil {
					t.logger.Debug("SSE stream ended by shutdown or cancellation")
					sawFinal = true
					return
				}
				if err == io.EOF {
					t.logger.Debug("SSE stream closed normally")
					return
				}
				// Check if error is due to closed body (normal for single-response streams)
				errMsg := err.Error()
				// Use strings.Contains to catch variations of the error message
				if errMsg == "http: read on closed response body" ||
					errMsg == "read on closed response body" ||
					(errMsg != "" && (bytes.Contains([]byte(errMsg), []byte("read on closed")) || bytes.Contains([]byte(errMsg), []byte("closed response body")))) {
					t.logger.Debug("SSE stream closed by server", zap.String("error", errMsg))
					return
				}
				t.logger.Warn("SSE stream error", zap.Error(err))
				return
			}

			// Skip empty events (no data)
			if len(event.Data) == 0 {
				t.logger.Debug("Skipping empty SSE event")
				continue
			}

			t.logger.Debug("SSE event parsed successfully",
				zap.String("event_id", event.ID),
				zap.ByteString("data", event.Data))

			if !sawFinal && isResponseForID(event.Data, expectedID) {
				sawFinal = true
			}

			// Send message to channel
			select {
			case t.messages <- event.Data:
				t.logger.Debug("Message sent to channel")
			case <-t.streamCtx.Done():
				t.logger.Debug("Stream context cancelled")
				sawFinal = true // shutdown, not a stream loss
				return
			case <-ctx.Done():
				t.logger.Debug("Request context cancelled")
				sawFinal = true // caller gave up; no synthesis needed
				return
			}
		}
	}()

	return nil
}

// isResponseForID reports whether data is a JSON-RPC response (result or
// error) whose id matches expectedID.
func isResponseForID(data []byte, expectedID json.RawMessage) bool {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	if len(probe.Result) == 0 && len(probe.Error) == 0 {
		return false
	}
	return string(probe.ID) == string(expectedID)
}

// synthesizeStreamLost delivers a CodeStreamLost error response for a request
// whose response stream ended before its final response arrived.
func (t *StreamableHTTPTransport) synthesizeStreamLost(ctx context.Context, id json.RawMessage) {
	t.logger.Warn("response stream lost before completion; synthesizing stream-lost error",
		zap.ByteString("request_id", id))
	synth, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    CodeStreamLost,
			"message": "response stream lost before completion; re-issue the request",
		},
	})
	if err != nil {
		return
	}
	select {
	case t.messages <- synth:
	case <-t.streamCtx.Done():
	case <-ctx.Done():
	}
}

// handleHTTPStatus handles HTTP status codes per MCP spec. The boolean is
// true when the response was fully consumed here (an error body delivered as
// a protocol message); the caller must not read the body further in that case.
func (t *StreamableHTTPTransport) handleHTTPStatus(ctx context.Context, resp *http.Response) (bool, error) {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return false, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Legacy (2025-03-26..2025-11-25) session expiry: a 404 while holding a
	// session means the server dropped it.
	if resp.StatusCode == http.StatusNotFound && t.sessionMgr.HasSession() {
		t.logger.Warn("Session expired (404), clearing session")
		t.sessionMgr.ClearSession()
		return true, ErrSessionExpired
	}

	// 2026-07-28 servers carry JSON-RPC error responses in HTTP 4xx bodies
	// (unknown method → 404 with -32601, version problems → 400 with -32022,
	// header mismatch → 400 with -32020). Deliver them as protocol messages
	// so the pending request receives the typed JSON-RPC error instead of a
	// transport failure.
	if isJSONRPCErrorResponse(body) {
		select {
		case t.messages <- body:
			return true, nil
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}

	return true, &HTTPStatusError{Code: resp.StatusCode, Body: body}
}

// isJSONRPCErrorResponse reports whether body is a routable JSON-RPC error
// response: correct version, an id to route on, and an error member.
func isJSONRPCErrorResponse(body []byte) bool {
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.JSONRPC == "2.0" && len(probe.ID) > 0 && string(probe.ID) != "null" && len(probe.Error) > 0
}

// terminateSession sends DELETE request to terminate session.
func (t *StreamableHTTPTransport) terminateSession(ctx context.Context) error {
	if !t.sessionMgr.HasSession() {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", t.endpoint, nil)
	if err != nil {
		return err
	}

	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", t.sessionMgr.GetSessionID())

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// 405 means server doesn't allow client termination, which is okay
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.logger.Debug("Server doesn't support session termination")
		return nil
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to terminate session: HTTP %d", resp.StatusCode)
	}

	t.logger.Info("Session terminated")
	return nil
}

// SetSessionID sets the session ID (used after initialization).
func (t *StreamableHTTPTransport) SetSessionID(id string) error {
	return t.sessionMgr.SetSessionID(id)
}

// GetSessionID returns the current session ID.
func (t *StreamableHTTPTransport) GetSessionID() string {
	return t.sessionMgr.GetSessionID()
}
