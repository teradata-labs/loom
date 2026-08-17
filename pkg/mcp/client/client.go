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
// Package client implements the MCP client for connecting to MCP servers.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// Client represents an MCP client connection to a server
type Client struct {
	transport transport.Transport
	logger    *zap.Logger

	// State
	initialized        bool
	initializing       bool
	protocolVersion    string
	serverInfo         protocol.Implementation
	serverCapabilities protocol.ServerCapabilities

	// Negotiated revision state (see connect.go). statelessMode is true when
	// the server speaks a 2026-07-28+ revision; requests then carry protocol
	// version, client capabilities, and client identity in params._meta
	// instead of relying on the initialize handshake.
	statelessMode bool
	clientInfo    protocol.Implementation
	versionPin    string

	// Request tracking
	nextID    int64
	pending   map[string]chan *protocol.Response
	pendingMu sync.RWMutex

	// Tool cache. toolHeaderParams holds the validated x-mcp-header
	// annotations per tool, refreshed together with tools by ListTools.
	tools            map[string]protocol.Tool
	toolHeaderParams map[string][]protocol.HeaderParam
	toolsMu          sync.RWMutex

	// Handlers
	progressHandlers map[string]ProgressHandler

	// Notifications
	notifications chan Notification

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool
}

// Config configures the MCP client
type Config struct {
	Transport transport.Transport
	Logger    *zap.Logger

	// Client info
	Name    string
	Version string

	// Capabilities.
	//
	// Deprecated: SupportsSampling and SupportsRoots are frozen legacy MCP
	// surface (docs/architecture/mcp-2026-07-28-migration.md §9.2); removal
	// no earlier than 2027-07-28. Sampling and Roots are Deprecated by
	// SEP-2577 and were never wired to behavior in this client.
	SupportsSampling bool
	SupportsRoots    bool

	// ProtocolVersion pins the revision Connect negotiates. Empty or "auto"
	// negotiates normally; "legacy" forces the initialize handshake without
	// probing server/discover; an explicit revision (e.g. "2026-07-28")
	// requires the server to speak exactly that revision.
	ProtocolVersion string

	// Timeouts
	RequestTimeout time.Duration // Default: 30s
}

// ProgressHandler is called for progress updates
type ProgressHandler func(progress, total float64)

// Notification represents a notification from the server
type Notification struct {
	Method string
	Params json.RawMessage
}

// NewClient creates a new MCP client
func NewClient(config Config) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}

	c := &Client{
		transport:        config.Transport,
		logger:           config.Logger,
		versionPin:       config.ProtocolVersion,
		ctx:              ctx,
		cancel:           cancel,
		pending:          make(map[string]chan *protocol.Response),
		tools:            make(map[string]protocol.Tool),
		toolHeaderParams: make(map[string][]protocol.HeaderParam),
		progressHandlers: make(map[string]ProgressHandler),
		notifications:    make(chan Notification, 100),
	}

	// Start message receiver
	c.wg.Add(1)
	go c.receiveLoop()

	return c
}

// Initialize performs the MCP handshake
func (c *Client) Initialize(ctx context.Context, clientInfo protocol.Implementation) error {
	c.mu.Lock()
	if c.initialized {
		c.mu.Unlock()
		return fmt.Errorf("already initialized")
	}
	if c.initializing {
		c.mu.Unlock()
		return fmt.Errorf("initialization already in progress")
	}
	c.initializing = true
	c.mu.Unlock()

	// If initialization fails, clear the initializing flag so it can be retried
	defer func() {
		c.mu.Lock()
		if !c.initialized {
			c.initializing = false
		}
		c.mu.Unlock()
	}()

	// Build capabilities
	caps := protocol.ClientCapabilities{}
	// Note: We'll add capabilities based on config in a future implementation

	// Create initialize request
	params := protocol.InitializeParams{
		ProtocolVersion: protocol.ProtocolVersion,
		Capabilities:    caps,
		ClientInfo:      clientInfo,
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "initialize",
		Params:  paramsJSON,
	}

	c.logger.Debug("Sending initialize request")

	// Send request
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		c.logger.Error("Initialize request failed", zap.Error(err))
		return fmt.Errorf("initialize failed: %w", err)
	}

	c.logger.Debug("Received initialize response")

	// Parse result
	var result protocol.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("failed to parse initialize result: %w", err)
	}

	// Verify the server answered with a revision this client can speak. The
	// previous strict equality check broke against any server negotiating a
	// different (including newer, backwards-compatible) handshake revision.
	if !protocol.IsSupportedVersion(result.ProtocolVersion) {
		return fmt.Errorf("unsupported protocol version from server: %s (client supports up to %s)",
			result.ProtocolVersion, protocol.PreferredVersion)
	}

	// Store server info
	c.mu.Lock()
	c.initialized = true
	c.protocolVersion = result.ProtocolVersion
	c.serverInfo = result.ServerInfo
	c.serverCapabilities = result.Capabilities
	c.mu.Unlock()

	c.logger.Info("MCP client initialized",
		zap.String("server", result.ServerInfo.Name),
		zap.String("version", result.ServerInfo.Version),
		zap.Bool("tools", result.Capabilities.Tools != nil),
		zap.Bool("resources", result.Capabilities.Resources != nil),
		zap.Bool("prompts", result.Capabilities.Prompts != nil),
	)

	// Send initialized notification per MCP spec
	// This completes the handshake and tells the server the client is ready
	// Notifications are JSON-RPC requests without an ID
	notification := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  "notifications/initialized",
		// ID is omitted for notifications
	}

	notificationJSON, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal initialized notification: %w", err)
	}

	c.logger.Debug("Sending initialized notification")

	if err := c.transport.Send(ctx, notificationJSON); err != nil {
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	c.logger.Debug("Initialized notification sent")

	return nil
}

// Ping sends a ping to check connection health.
//
// Deprecated: frozen legacy MCP surface (docs/architecture/mcp-2026-07-28-migration.md §9.2);
// removal no earlier than 2027-07-28. The method does not exist under the
// 2026-07-28 revision; health on stateless HTTP connections is a transport
// property.
func (c *Client) Ping(ctx context.Context) error {
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "ping",
		Params:  json.RawMessage(`{}`),
	}

	_, err := c.sendRequest(ctx, req)
	return err
}

// ServerInfo returns the server implementation info
func (c *Client) ServerInfo() protocol.Implementation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// ServerCapabilities returns the server capabilities
func (c *Client) ServerCapabilities() protocol.ServerCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverCapabilities
}

// IsInitialized returns whether the client is initialized
func (c *Client) IsInitialized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

// NegotiatedVersion returns the protocol revision agreed with the server, or
// the empty string before Connect/Initialize completes.
func (c *Client) NegotiatedVersion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.protocolVersion
}

// Close closes the client connection
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// Cancel context to stop receiver
	c.cancel()

	// Close transport
	if err := c.transport.Close(); err != nil {
		c.logger.Error("failed to close transport", zap.Error(err))
	}

	// Wait for receiver goroutine
	c.wg.Wait()

	// Close notification channel
	close(c.notifications)

	c.logger.Info("MCP client closed")
	return nil
}

// sendRequest sends a request and waits for its final response, driving the
// revision-level behaviors that operate on whole logical calls: _meta
// stamping, idempotency keys, and one re-issue after a lost response stream
// (the 2026-07-28 recovery for broken streams, safe because the re-issue
// carries the same idempotency key a dedupe-aware server can join on).
func (c *Client) sendRequest(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	// Validate request
	if err := protocol.ValidateRequest(req); err != nil {
		return nil, err
	}

	// Generate request ID if not set
	if req.ID == nil {
		req.ID = c.nextRequestID()
	}

	c.mu.RLock()
	stateless := c.statelessMode
	version := c.protocolVersion
	info := c.clientInfo
	c.mu.RUnlock()

	// The MCP-Protocol-Version header is required on every POST by the
	// 2026-07-28 transport and defined since 2025-06-18; it must match the
	// version stamped in _meta. Older servers ignore unknown headers, and
	// non-HTTP transports ignore the context entry.
	if version != "" {
		ctx = transport.WithExtraHeaders(ctx, map[string]string{"MCP-Protocol-Version": version})
	}

	// The idempotency key names this logical call across re-issues; it is
	// minted once here and stamped identically on every attempt.
	idemKey := ""
	if stateless && req.Method == "tools/call" {
		idemKey = uuid.NewString()
	}

	baseParams := req.Params
	reissued := false

	for {
		params := baseParams
		if stateless {
			// Every request carries protocol version, client capabilities,
			// and client identity in params._meta under the stateless core.
			stamped, stampErr := protocol.StampMeta(params, version, info, c.clientCapabilities())
			if stampErr != nil {
				return nil, fmt.Errorf("failed to stamp _meta: %w", stampErr)
			}
			if idemKey != "" {
				stamped, stampErr = protocol.StampMetaKey(stamped, protocol.MetaIdempotencyKey, idemKey)
				if stampErr != nil {
					return nil, fmt.Errorf("failed to stamp idempotency key: %w", stampErr)
				}
			}
			params = stamped
		}

		attemptReq := &protocol.Request{
			JSONRPC: req.JSONRPC,
			ID:      req.ID,
			Method:  req.Method,
			Params:  params,
		}

		resp, err := c.dispatchAndWait(ctx, attemptReq)
		if err != nil {
			var rpcErr *protocol.Error
			if stateless && !reissued && errors.As(err, &rpcErr) && rpcErr.Code == transport.CodeStreamLost {
				// Spec-mandated recovery: re-issue as a new request with a
				// new ID. The unchanged idempotency key lets the server join
				// the retry to the original run instead of executing twice.
				reissued = true
				req.ID = c.nextRequestID()
				c.logger.Warn("response stream lost; re-issuing request",
					zap.String("method", req.Method),
					zap.String("idempotency_key", idemKey))
				continue
			}
			return nil, err
		}

		// Under the stateless revision every result carries a resultType
		// envelope. Surfacing interim MRTR results here — the one choke point
		// all request paths share — prevents any caller from decoding an
		// input_required interim result as if it were the final one.
		if stateless {
			if err := checkResultEnvelope(req.Method, resp.Result); err != nil {
				return nil, err
			}
		}
		return resp, nil
	}
}

// dispatchAndWait performs one wire attempt: register the pending request,
// send, and wait for its response or context cancellation.
func (c *Client) dispatchAndWait(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	respChan := make(chan *protocol.Response, 1)
	reqIDStr := req.ID.String()

	c.pendingMu.Lock()
	c.pending[reqIDStr] = respChan
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, reqIDStr)
		c.pendingMu.Unlock()
		close(respChan)
	}()

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.logger.Debug("Sending request via transport",
		zap.String("method", req.Method),
		zap.String("id", reqIDStr))

	if err := c.transport.Send(ctx, reqJSON); err != nil {
		c.logger.Error("Failed to send request via transport",
			zap.String("method", req.Method),
			zap.Error(err))
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	c.logger.Debug("Request sent, waiting for response",
		zap.String("method", req.Method),
		zap.String("id", reqIDStr))

	select {
	case <-ctx.Done():
		c.logger.Debug("Context cancelled while waiting for response",
			zap.String("method", req.Method))
		return nil, ctx.Err()
	case resp := <-respChan:
		c.logger.Debug("Received response",
			zap.String("method", req.Method),
			zap.String("id", reqIDStr))
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	}
}

// clientCapabilities builds the capabilities stamped into _meta on every
// stateless request.
func (c *Client) clientCapabilities() protocol.ClientCapabilities {
	return protocol.ClientCapabilities{}
}

// receiveLoop receives messages from transport
func (c *Client) receiveLoop() {
	defer c.wg.Done()
	c.logger.Debug("receiveLoop started")

	for {
		// Check if context is cancelled
		select {
		case <-c.ctx.Done():
			c.logger.Debug("receiveLoop: context cancelled")
			return
		default:
		}

		// Receive message
		data, err := c.transport.Receive(c.ctx)
		if err != nil {
			// Check for normal shutdown conditions
			// "transport closed" is returned by StdioTransport when Close() is called
			isTransportClosed := err.Error() == "transport closed"
			if c.ctx.Err() != nil || errors.Is(err, io.EOF) || isTransportClosed {
				// Context cancelled or connection closed - normal shutdown
				c.logger.Debug("receiveLoop: normal shutdown", zap.Error(err))
				return
			}
			c.logger.Error("failed to receive message", zap.Error(err))
			continue
		}

		// Skip empty messages
		if len(data) == 0 {
			c.logger.Debug("receiveLoop: skipping empty message")
			continue
		}

		// Try to parse as response first
		var resp protocol.Response
		if err := json.Unmarshal(data, &resp); err == nil && resp.ID != nil {
			c.handleResponse(&resp)
			continue
		}

		// Try to parse as request (for sampling, etc.)
		var req protocol.Request
		if err := json.Unmarshal(data, &req); err == nil && req.Method != "" {
			c.handleRequest(&req)
			continue
		}

		c.logger.Warn("received unrecognized message", zap.ByteString("data", data))
	}
}

// handleResponse routes response to pending request
func (c *Client) handleResponse(resp *protocol.Response) {
	reqIDStr := resp.ID.String()

	c.pendingMu.RLock()
	respChan, exists := c.pending[reqIDStr]
	c.pendingMu.RUnlock()

	if !exists {
		c.logger.Warn("received response for unknown request", zap.String("id", reqIDStr))
		return
	}

	select {
	case respChan <- resp:
	default:
		c.logger.Warn("response channel full", zap.String("id", reqIDStr))
	}
}

// handleRequest answers incoming server-initiated requests. Under the
// 2026-07-28 revision servers must not send them (MRTR embeds their content
// in input_required results instead), and the legacy sampling support was
// dead code (no handler was ever registered), so every server-initiated
// request is answered MethodNotFound.
func (c *Client) handleRequest(req *protocol.Request) {
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	resp := c.createErrorResponse(req.ID, protocol.MethodNotFound,
		fmt.Sprintf("method not found: %s", req.Method), nil)

	respJSON, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		c.logger.Error("failed to marshal response", zap.String("method", req.Method), zap.Error(marshalErr))
		return
	}
	if err := c.transport.Send(ctx, respJSON); err != nil {
		c.logger.Error("failed to send response", zap.Error(err))
	}
}

// nextRequestID generates next request ID
func (c *Client) nextRequestID() *protocol.RequestID {
	id := atomic.AddInt64(&c.nextID, 1)
	return protocol.NewNumericRequestID(id)
}

// createErrorResponse creates an error response
func (c *Client) createErrorResponse(id *protocol.RequestID, code int, message string, data interface{}) *protocol.Response {
	err := protocol.NewError(code, message, data)
	return &protocol.Response{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      id,
		Error:   err,
	}
}
