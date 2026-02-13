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
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ErrSessionExpired indicates the server session has expired (HTTP 404).
var ErrSessionExpired = errors.New("session expired")

// StreamableHTTPTransport implements the MCP streamable-http transport.
// This is the modern MCP transport (2025-03-26 spec) with session management
// and stream resumption support.
type StreamableHTTPTransport struct {
	endpoint string
	client   *http.Client

	// Session management
	sessionMgr *SessionManager

	// Stream resumption
	resumption *StreamResumption

	// Message channels
	messages chan []byte
	errors   chan error

	// Lifecycle
	mu      sync.Mutex
	closed  bool
	started bool
	logger  *zap.Logger

	// Stream management
	activeStreams sync.WaitGroup
	streamCancel  context.CancelFunc
	streamCtx     context.Context

	// Configuration
	enableSessions   bool
	enableResumption bool
}

// StreamableHTTPConfig configures streamable-http transport.
type StreamableHTTPConfig struct {
	Endpoint         string            // MCP endpoint URL
	Headers          map[string]string // Custom headers
	EnableSessions   bool              // Enable session management
	EnableResumption bool              // Enable stream resumption
	Logger           *zap.Logger       // Logger
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

	t := &StreamableHTTPTransport{
		endpoint:         config.Endpoint,
		client:           &http.Client{},
		sessionMgr:       NewSessionManager(),
		resumption:       NewStreamResumption(100),
		messages:         make(chan []byte, 100),
		errors:           make(chan error, 1),
		logger:           logger,
		streamCtx:        streamCtx,
		streamCancel:     streamCancel,
		enableSessions:   config.EnableSessions,
		enableResumption: config.EnableResumption,
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
	started := t.started
	t.started = true
	t.mu.Unlock()

	// Build POST request
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

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
	defer resp.Body.Close()

	// Handle HTTP errors
	if err := t.handleHTTPStatus(resp); err != nil {
		return err
	}

	// Extract session ID from response (on first request)
	if !started && t.enableSessions {
		if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
			if err := t.sessionMgr.SetSessionID(sessionID); err != nil {
				t.logger.Warn("Invalid session ID from server", zap.Error(err))
			} else {
				t.logger.Info("Session established", zap.String("session_id", sessionID))
			}
		}
	}

	// Handle response based on Content-Type
	contentType := resp.Header.Get("Content-Type")
	t.logger.Debug("Received HTTP response",
		zap.String("content-type", contentType),
		zap.Int("status", resp.StatusCode),
		zap.Bool("started", started))

	switch {
	case contentType == "text/event-stream":
		// SSE stream response
		t.logger.Debug("Handling SSE stream response")

		// For single-event responses, the server might close the connection immediately
		// Read all the data first to avoid "read on closed response body" errors
		allData, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read SSE response: %w", err)
		}
		t.logger.Debug("Read SSE response data", zap.Int("bytes", len(allData)))

		// Parse the SSE data from the buffer
		return t.handleSSEStream(ctx, io.NopCloser(bytes.NewReader(allData)))

	case contentType == "application/json":
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

// handleSSEStream processes an SSE response stream.
func (t *StreamableHTTPTransport) handleSSEStream(ctx context.Context, body io.ReadCloser) error {
	t.logger.Debug("Starting SSE stream handler")
	t.activeStreams.Add(1)
	go func() {
		defer t.activeStreams.Done()
		defer body.Close()

		parser := NewSSEParser(body)

		for {
			t.logger.Debug("Parsing SSE event")
			event, err := parser.ParseEvent()
			if err != nil {
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
				select {
				case t.errors <- fmt.Errorf("SSE parse error: %w", err):
				default:
				}
				return
			}

			// Skip empty events (no data)
			if len(event.Data) == 0 {
				t.logger.Debug("Skipping empty SSE event")
				continue
			}

			// Store event for resumption
			if t.enableResumption && event.ID != "" {
				t.resumption.AddEvent(*event)
			}

			t.logger.Debug("SSE event parsed successfully",
				zap.String("event_id", event.ID),
				zap.ByteString("data", event.Data))

			// Send message to channel
			select {
			case t.messages <- event.Data:
				t.logger.Debug("Message sent to channel")
			case <-t.streamCtx.Done():
				t.logger.Debug("Stream context cancelled")
				return
			case <-ctx.Done():
				t.logger.Debug("Request context cancelled")
				return
			}
		}
	}()

	return nil
}

// handleHTTPStatus handles HTTP status codes per MCP spec.
func (t *StreamableHTTPTransport) handleHTTPStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return nil

	case http.StatusBadRequest:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bad request (400): %s", body)

	case http.StatusNotFound:
		// Session expired
		t.logger.Warn("Session expired (404), clearing session")
		t.sessionMgr.ClearSession()
		if t.enableResumption {
			t.resumption.Clear()
		}
		return ErrSessionExpired

	case http.StatusMethodNotAllowed:
		return fmt.Errorf("method not allowed (405): server doesn't support this operation")

	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP error %d: %s", resp.StatusCode, body)
	}
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

	req.Header.Set("Mcp-Session-Id", t.sessionMgr.GetSessionID())

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
