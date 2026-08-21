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
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"

	"net/url"
	"sort"
)

// DefaultSessionTTL is the recommended session TTL for production use (30 minutes).
// Pass this to StreamableHTTPServerConfig.SessionTTL to enable session cleanup.
const DefaultSessionTTL = 30 * time.Minute

// MCPHandler is a function that processes MCP JSON-RPC messages and returns a response.
// For notifications (no id), it returns nil.
type MCPHandler func(ctx context.Context, msg []byte) ([]byte, error)

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
	handler        MCPHandler
	streamHandler  StreamingMCPHandler
	sessions       map[string]*httpSession
	mu             sync.RWMutex
	logger         *zap.Logger
	sessionTTL     time.Duration
	stopCleanup    chan struct{}
	cleanupOnce    sync.Once
	allowedOrigins []string
	statelessVers  map[string]bool // exact stateless revisions this server admits
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

	// AllowedOrigins lists Origin header values permitted on incoming
	// connections, compared case-insensitively as exact values; the entry
	// "*" permits any origin. Requests without an Origin header (non-browser
	// clients) are always admitted. When empty, only loopback origins
	// (http/https on localhost, 127.0.0.1, or [::1], any port) are permitted
	// — the Streamable HTTP specification requires Origin validation on all
	// incoming connections to prevent DNS-rebinding attacks, with HTTP 403
	// for a present-but-invalid Origin.
	AllowedOrigins []string

	// SupportedStatelessVersions lists the exact stateless (2026-07-28+)
	// protocol revisions this server admits; a request declaring any other
	// modern revision is rejected with 400 and UnsupportedProtocolVersion
	// (-32022) listing this set, as the specification requires. Defaults to
	// {2026-07-28}. Admission is exact-match — an open-ended comparison
	// would execute revisions this server has never implemented.
	SupportedStatelessVersions []string
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

	statelessVers := make(map[string]bool)
	if len(config.SupportedStatelessVersions) == 0 {
		statelessVers[protocol.Version20260728] = true
	}
	for _, v := range config.SupportedStatelessVersions {
		statelessVers[v] = true
	}

	s := &StreamableHTTPServer{
		handler:        config.Handler,
		streamHandler:  config.StreamHandler,
		sessions:       make(map[string]*httpSession),
		logger:         config.Logger,
		sessionTTL:     ttl,
		stopCleanup:    make(chan struct{}),
		allowedOrigins: config.AllowedOrigins,
		statelessVers:  statelessVers,
	}

	if ttl > 0 {
		s.startCleanup()
	}

	return s, nil
}

// ServeHTTP implements http.Handler for the MCP endpoint.
func (s *StreamableHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Origin validation runs before any body or session processing: the
	// specification requires validating Origin on all incoming connections
	// and answering a present-but-invalid one with HTTP 403 (DNS-rebinding
	// defense). The response body MAY be an id-less JSON-RPC error.
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
		s.logger.Warn("rejected request from disallowed origin", zap.String("origin", origin))
		s.writeJSONRPCError(w, http.StatusForbidden, nil, protocol.InvalidRequest,
			"origin not allowed")
		return
	}

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

	// Dual-mode admission (MCP 2026-07-28): a request declaring a stateless
	// revision — in params._meta or in the MCP-Protocol-Version header —
	// takes the stateless path, which validates the declaration strictly
	// and bypasses the session layer entirely (no lookup, no minting, no
	// session response header; any stale Mcp-Session-Id is ignored).
	// Routing on either signal closes the bypass where a legacy-shaped body
	// under modern headers would slip past validation into legacy dispatch.
	peek := peekStatelessRequest(body)
	if protocol.IsStatelessVersion(peek.version) || protocol.IsStatelessVersion(r.Header.Get("MCP-Protocol-Version")) {
		s.handleStatelessPost(w, r, body, peek)
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

	// Process message (pass the request context so auth/identity middleware
	// data reaches the handler on the synchronous path too).
	resp, err := s.handler(r.Context(), body)
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

// statelessPeek carries the body fields dual-mode admission and header
// validation need, extracted in a single parse.
type statelessPeek struct {
	id      json.RawMessage
	method  string
	version string
	name    string // params.name (tools/call, prompts/get)
	uri     string // params.uri (resources/read)
}

func peekStatelessRequest(body []byte) statelessPeek {
	var p struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
			Name string                     `json:"name"`
			URI  string                     `json:"uri"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return statelessPeek{}
	}
	peek := statelessPeek{id: p.ID, method: p.Method, name: p.Params.Name, uri: p.Params.URI}
	if raw, ok := p.Params.Meta[protocol.MetaProtocolVersion]; ok {
		_ = json.Unmarshal(raw, &peek.version)
	}
	return peek
}

// mcpNameSource returns the body value the Mcp-Name header mirrors for the
// given method, and whether the method requires the header at all.
func (p statelessPeek) mcpNameSource() (value string, required bool) {
	switch p.method {
	case "tools/call", "prompts/get":
		return p.name, true
	case "resources/read":
		return p.uri, true
	}
	return "", false
}

// originAllowed implements the trusted-Origin policy: exact case-insensitive
// match against the configured allowlist ("*" admits any), defaulting to
// loopback origins only. Absent Origin headers are handled by the caller.
func (s *StreamableHTTPServer) originAllowed(origin string) bool {
	if len(s.allowedOrigins) > 0 {
		for _, allowed := range s.allowedOrigins {
			if allowed == "*" || strings.EqualFold(allowed, origin) {
				return true
			}
		}
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// handleStatelessPost admits a modern (2026-07-28+) request. Admission is
// strict, per the transport's Server Validation rules: the declared revision
// must be one this server implements exactly (open-ended acceptance would
// execute revisions never implemented here), and on requests the required
// standard headers must be present and agree with the body — a missing or
// mismatched MCP-Protocol-Version, Mcp-Method, or (where applicable)
// Mcp-Name is a 400 with HeaderMismatch (-32020). Notification POSTs carry
// no header requirements in this revision.
func (s *StreamableHTTPServer) handleStatelessPost(w http.ResponseWriter, r *http.Request, body []byte, peek statelessPeek) {
	headerVersion := r.Header.Get("MCP-Protocol-Version")
	isRequest := len(peek.id) > 0 && string(peek.id) != "null"

	// A legacy-shaped body (no stateless _meta) routed here by a modern
	// header is a header/body disagreement, not an unknown version.
	if peek.version == "" {
		s.writeJSONRPCError(w, http.StatusBadRequest, peek.id, protocol.HeaderMismatch,
			fmt.Sprintf("MCP-Protocol-Version header %q does not match _meta protocol version %q", headerVersion, peek.version))
		return
	}

	// Exact version support: unknown or unimplemented revisions — including
	// futures this build has never seen — are rejected with the supported
	// set, so the client can retry with a mutual revision.
	if !s.statelessVers[peek.version] {
		supported := make([]string, 0, len(s.statelessVers))
		for v := range s.statelessVers {
			supported = append(supported, v)
		}
		sort.Strings(supported)
		s.writeJSONRPCErrorData(w, http.StatusBadRequest, peek.id, protocol.UnsupportedProtocolVersion,
			fmt.Sprintf("unsupported protocol version %q", peek.version),
			map[string]interface{}{"supported": supported, "requested": peek.version})
		return
	}

	// Header requirements apply to requests; the revision defines no header
	// requirements for notification POSTs.
	if isRequest {
		if headerVersion != peek.version {
			s.writeJSONRPCError(w, http.StatusBadRequest, peek.id, protocol.HeaderMismatch,
				fmt.Sprintf("MCP-Protocol-Version header %q does not match _meta protocol version %q", headerVersion, peek.version))
			return
		}
		if hdr := r.Header.Get("Mcp-Method"); hdr == "" || hdr != peek.method {
			s.writeJSONRPCError(w, http.StatusBadRequest, peek.id, protocol.HeaderMismatch,
				fmt.Sprintf("Mcp-Method header %q does not match body method %q", hdr, peek.method))
			return
		}
		if source, required := peek.mcpNameSource(); required {
			decoded, err := protocol.DecodeHeaderValue(r.Header.Get("Mcp-Name"))
			if err != nil || r.Header.Get("Mcp-Name") == "" || decoded != source {
				s.writeJSONRPCError(w, http.StatusBadRequest, peek.id, protocol.HeaderMismatch,
					fmt.Sprintf("Mcp-Name header does not match the body value for %s", peek.method))
				return
			}
		}
		// Mcp-Param-* headers mirror x-mcp-header-annotated tool arguments.
		// None of this server's tools declare such annotations, so no
		// Mcp-Param header is recognized here and they are ignored per the
		// forwarding rule — but a malformed Base64 sentinel is still a
		// header that "contains invalid characters" and is rejected.
		for name, vals := range r.Header {
			if !strings.HasPrefix(http.CanonicalHeaderKey(name), "Mcp-Param-") {
				continue
			}
			for _, v := range vals {
				if _, err := protocol.DecodeHeaderValue(v); err != nil {
					s.writeJSONRPCError(w, http.StatusBadRequest, peek.id, protocol.HeaderMismatch,
						fmt.Sprintf("header %s carries an invalid encoded value", name))
					return
				}
			}
		}
	}

	// POST-response SSE only for methods that actually stream: the SSE path
	// commits HTTP 200 before the handler runs, which would defeat the
	// status mapping below for everything else (unknown methods must be 404).
	streamable := peek.method == "tools/call" || peek.method == protocol.MethodSubscriptionsListen
	if isRequest && streamable && s.streamHandler != nil && acceptsEventStream(r.Header.Get("Accept")) {
		if flusher, ok := w.(http.Flusher); ok {
			s.handleStreamingPost(w, r, flusher, body)
			return
		}
	}

	resp, err := s.handler(r.Context(), body)
	if err != nil {
		s.logger.Error("handler error", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if resp == nil {
		// Notification - accepted but no content
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statelessResponseStatus(resp))
	_, _ = w.Write(resp)
}

// statelessResponseStatus maps handler-produced JSON-RPC errors to the HTTP
// status the transport requires on the modern path: unknown method is
// 404 + MethodNotFound; version and header failures are 400. Everything
// else — results and ordinary errors — is 200.
func statelessResponseStatus(resp []byte) int {
	var probe struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &probe); err != nil || probe.Error == nil {
		return http.StatusOK
	}
	switch probe.Error.Code {
	case protocol.MethodNotFound:
		return http.StatusNotFound
	case protocol.HeaderMismatch, protocol.MissingRequiredClientCapability, protocol.UnsupportedProtocolVersion:
		return http.StatusBadRequest
	}
	return http.StatusOK
}

// writeJSONRPCErrorData is writeJSONRPCError with a data member — the
// UnsupportedProtocolVersion error carries its supported/requested lists.
func (s *StreamableHTTPServer) writeJSONRPCErrorData(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data interface{}) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]interface{}{"code": code, "message": message, "data": data},
	})
	if err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeJSONRPCError answers with an HTTP error status carrying a JSON-RPC
// error body, as the 2026-07-28 transport requires for header-validation
// failures.
func (s *StreamableHTTPServer) writeJSONRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]interface{}{"code": code, "message": message},
	})
	if err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
	// Strip the port if present. net.SplitHostPort handles bracketed IPv6
	// ("[::1]:8080" -> "::1") correctly, unlike a naive LastIndex(":") which
	// would truncate a bare IPv6 literal mid-address.
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	// Strip brackets for a bracketed IPv6 host given without a port.
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
