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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// DefaultSessionTTL is the recommended session TTL for production use (30 minutes).
// Pass this to StreamableHTTPServerConfig.SessionTTL to enable session cleanup.
const DefaultSessionTTL = 30 * time.Minute

// MCPHandler is a function that processes MCP JSON-RPC messages and returns a response.
// For notifications (no id), it returns nil.
type MCPHandler func(msg []byte) ([]byte, error)

// SSEWriter writes individual Server-Sent Events on a streaming POST response.
// Each call emits one framed event (id/event/data) and flushes immediately.
// A single response uses one SSEWriter; it is not safe for concurrent use.
type SSEWriter interface {
	// WriteEvent frames data as one SSE "message" event and flushes it.
	// data must be a single JSON-RPC message with no literal newlines
	// (standard JSON marshaling satisfies this).
	WriteEvent(data []byte) error
}

// StreamingMCPHandler is an optional capability a Handler may also implement to
// support POST-response Server-Sent Events. When a StreamHandler is configured
// and a POST carries "Accept: text/event-stream", the transport routes the
// request here instead of the synchronous Handler.
//
// The handler emits zero or more intermediate events (e.g. progress
// notifications) via w, then returns the final JSON-RPC response bytes for the
// transport to emit as the last event. Tool-level failures should be encoded
// into the returned bytes (e.g. an isError result); a non-nil error is reserved
// for unexpected transport-level failures.
type StreamingMCPHandler interface {
	HandleMessageStream(ctx context.Context, msg []byte, w SSEWriter) ([]byte, error)
}

// StreamableHTTPServer implements the MCP streamable-http server transport.
// It provides a single POST endpoint that handles JSON-RPC messages
// per the MCP 2025-03-26 spec.
//
// Security: This transport has NO authentication or authorization. It MUST
// only be bound to localhost (127.0.0.1 / ::1). Exposing it on a network
// interface grants unauthenticated access to all registered MCP tools.
// Use WarnIfNotLocalhost to check the listen address before starting.
//
// Features:
//   - Single POST endpoint for all MCP communication
//   - Session management via Mcp-Session-Id header
//   - DELETE for session termination
//   - JSON responses for single messages
//   - Automatic session cleanup with configurable TTL
type StreamableHTTPServer struct {
	handler       MCPHandler
	streamHandler StreamingMCPHandler
	sessions      map[string]*httpSession
	mu            sync.RWMutex
	logger        *zap.Logger
	sessionTTL    time.Duration
	stopCleanup   chan struct{}
	cleanupOnce   sync.Once
}

type httpSession struct {
	id           string
	lastActivity time.Time
}

// StreamableHTTPServerConfig configures the HTTP server transport.
type StreamableHTTPServerConfig struct {
	Handler MCPHandler // Required: processes MCP messages
	// StreamHandler is optional. When set, POST requests that carry
	// "Accept: text/event-stream" are answered as Server-Sent Events, allowing
	// long-running tool calls to stream progress before the final result.
	StreamHandler StreamingMCPHandler
	Logger        *zap.Logger
	SessionTTL    time.Duration // TTL for idle sessions; 0 disables cleanup, default 30 minutes
}

// NewStreamableHTTPServer creates a new MCP streamable HTTP server handler.
// Set SessionTTL > 0 to enable automatic session cleanup (recommended: DefaultSessionTTL).
// SessionTTL of 0 (the zero value) disables automatic cleanup.
func NewStreamableHTTPServer(config StreamableHTTPServerConfig) (*StreamableHTTPServer, error) {
	if config.Handler == nil {
		return nil, fmt.Errorf("handler is required")
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	ttl := config.SessionTTL
	if ttl < 0 {
		ttl = 0
	}

	s := &StreamableHTTPServer{
		handler:       config.Handler,
		streamHandler: config.StreamHandler,
		sessions:      make(map[string]*httpSession),
		logger:        config.Logger,
		sessionTTL:    ttl,
		stopCleanup:   make(chan struct{}),
	}

	if ttl > 0 {
		s.startCleanup()
	}

	return s, nil
}

// ServeHTTP implements http.Handler for the MCP endpoint.
func (s *StreamableHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePost(w, r)
	case http.MethodDelete:
		s.handleDelete(w, r)
	case http.MethodGet:
		// The standalone server-initiated GET stream is not offered. Per the MCP
		// spec, respond 405 so a client probing for it degrades gracefully.
		// (POST-response SSE is supported via handlePost when a StreamHandler is
		// configured.)
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *StreamableHTTPServer) handlePost(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()

	// Validate content type (accept "application/json" with optional params like charset)
	ct := r.Header.Get("Content-Type")
	if ct != "" {
		mediaType, _, _ := mime.ParseMediaType(ct)
		if mediaType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
	}

	// Read request body
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		s.logger.Error("failed to read request body", zap.Error(err))
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if len(body) == 0 {
		http.Error(w, "Empty request body", http.StatusBadRequest)
		return
	}

	// Check if this is an initialize request (needs session creation)
	isInit := s.isInitializeRequest(body)

	// Session handling
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID != "" {
		s.mu.Lock()
		sess, exists := s.sessions[sessionID]
		if exists {
			sess.lastActivity = time.Now()
		}
		s.mu.Unlock()
		if !exists {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}
	}

	// POST-response SSE: when the client accepts an event stream, a StreamHandler
	// is configured, the response writer can flush, and this is not the initialize
	// handshake (which must create the session via the JSON path), answer as
	// Server-Sent Events so a long-running tool call can stream progress.
	if !isInit && s.streamHandler != nil && acceptsEventStream(r.Header.Get("Accept")) {
		if flusher, ok := w.(http.Flusher); ok {
			s.handleStreamingPost(w, r, flusher, body)
			return
		}
	}

	// Process message
	resp, err := s.handler(body)
	if err != nil {
		s.logger.Error("handler error", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Create session on initialize response
	if isInit && sessionID == "" {
		newSessionID := uuid.New().String()
		s.mu.Lock()
		s.sessions[newSessionID] = &httpSession{
			id:           newSessionID,
			lastActivity: time.Now(),
		}
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", newSessionID)
		s.logger.Info("created new session", zap.String("session_id", newSessionID))
	}

	// Send response
	if resp == nil {
		// Notification - accepted but no content
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

// acceptsEventStream reports whether the request's Accept header opts into SSE.
func acceptsEventStream(accept string) bool {
	return strings.Contains(accept, "text/event-stream")
}

// handleStreamingPost answers a POST as Server-Sent Events. It writes the SSE
// headers, then delegates to the StreamHandler, which emits intermediate events
// via the SSEWriter and returns the final JSON-RPC response (written last).
func (s *StreamableHTTPServer) handleStreamingPost(w http.ResponseWriter, r *http.Request, flusher http.Flusher, body []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable response buffering in intermediary proxies (nginx, tunnels) so
	// events reach the client as they are flushed.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := &httpSSEWriter{w: w, flusher: flusher}
	final, err := s.streamHandler.HandleMessageStream(r.Context(), body, sw)
	if err != nil {
		s.logger.Error("streaming handler error", zap.Error(err))
		// The response is already 200/SSE; surface a generic JSON-RPC error event.
		_ = sw.WriteEvent([]byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"}}`))
		return
	}
	if final != nil {
		if err := sw.WriteEvent(final); err != nil {
			s.logger.Warn("failed to write final SSE event", zap.Error(err))
		}
	}
}

// httpSSEWriter frames JSON-RPC messages as SSE "message" events with a
// monotonic id, matching the grammar in sse_parser.go and the MCP spec.
type httpSSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	eventID int
}

func (s *httpSSEWriter) WriteEvent(data []byte) error {
	s.eventID++
	if _, err := fmt.Fprintf(s.w, "id: %d\nevent: message\ndata: %s\n\n", s.eventID, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *StreamableHTTPServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	_, exists := s.sessions[sessionID]
	if exists {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	if !exists {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	s.logger.Info("session terminated", zap.String("session_id", sessionID))
	w.WriteHeader(http.StatusOK)
}

// isInitializeRequest checks if the body contains an initialize method call.
func (s *StreamableHTTPServer) isInitializeRequest(body []byte) bool {
	var req struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	return req.Method == "initialize"
}

// SessionCount returns the number of active sessions.
func (s *StreamableHTTPServer) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// Close stops the background session cleanup goroutine and releases resources.
// It is safe to call Close multiple times.
func (s *StreamableHTTPServer) Close() {
	s.cleanupOnce.Do(func() {
		close(s.stopCleanup)
	})
}

// startCleanup starts a background goroutine that periodically removes expired sessions.
// The cleanup interval is half the session TTL.
func (s *StreamableHTTPServer) startCleanup() {
	interval := s.sessionTTL / 2
	if interval < time.Second {
		interval = time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-s.stopCleanup:
				return
			case now := <-ticker.C:
				s.expireSessions(now)
			}
		}
	}()
}

// expireSessions removes all sessions whose lastActivity is older than the TTL.
func (s *StreamableHTTPServer) expireSessions(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sess := range s.sessions {
		if now.Sub(sess.lastActivity) > s.sessionTTL {
			delete(s.sessions, id)
			s.logger.Info("session expired", zap.String("session_id", id))
		}
	}
}

// WarnIfNotLocalhost logs a warning if the given listen address appears to bind
// to a non-localhost interface. This transport has no authentication, so binding
// to 0.0.0.0 or a public IP exposes all MCP tools without access control.
//
// Call this before starting the HTTP server:
//
//	transport.WarnIfNotLocalhost(logger, listenAddr)
//	http.ListenAndServe(listenAddr, handler)
func WarnIfNotLocalhost(logger *zap.Logger, addr string) {
	if logger == nil {
		return
	}
	host := addr
	// Strip port if present.
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		host = addr[:idx]
	}
	// Strip brackets for IPv6.
	host = strings.Trim(host, "[]")

	switch host {
	case "", "0.0.0.0", "::":
		logger.Warn("MCP HTTP transport binding to all interfaces - this is INSECURE",
			zap.String("addr", addr),
			zap.String("recommendation", "bind to 127.0.0.1 or ::1 for localhost-only access"),
		)
	case "127.0.0.1", "::1", "localhost":
		// Safe - localhost only.
	default:
		logger.Warn("MCP HTTP transport binding to non-localhost address - this is INSECURE",
			zap.String("addr", addr),
			zap.String("recommendation", "bind to 127.0.0.1 or ::1 for localhost-only access"),
		)
	}
}
