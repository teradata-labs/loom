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

	// Request tracking
	nextID    int64
	pending   map[string]chan *protocol.Response
	pendingMu sync.RWMutex

	// Tool cache
	tools   map[string]protocol.Tool
	toolsMu sync.RWMutex

	// Handlers
	samplingHandler  SamplingHandler
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

	// Capabilities
	SupportsSampling bool
	SupportsRoots    bool

	// Timeouts
	RequestTimeout time.Duration // Default: 30s
}

// SamplingHandler is called when server requests LLM completion
type SamplingHandler func(ctx context.Context, params protocol.SamplingParams) (*protocol.SamplingResult, error)

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
		ctx:              ctx,
		cancel:           cancel,
		pending:          make(map[string]chan *protocol.Response),
		tools:            make(map[string]protocol.Tool),
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

	// Verify protocol version
	if result.ProtocolVersion != protocol.ProtocolVersion {
		return fmt.Errorf("protocol version mismatch: client=%s server=%s",
			protocol.ProtocolVersion, result.ProtocolVersion)
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

// Ping sends a ping to check connection health
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

// sendRequest sends a request and waits for response
func (c *Client) sendRequest(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	// Validate request
	if err := protocol.ValidateRequest(req); err != nil {
		return nil, err
	}

	// Generate request ID if not set
	if req.ID == nil {
		req.ID = c.nextRequestID()
	}

	// Create response channel
	respChan := make(chan *protocol.Response, 1)
	reqIDStr := req.ID.String()

	c.pendingMu.Lock()
	c.pending[reqIDStr] = respChan
	c.pendingMu.Unlock()

	// Cleanup
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, reqIDStr)
		c.pendingMu.Unlock()
		close(respChan)
	}()

	// Serialize request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.logger.Debug("Sending request via transport",
		zap.String("method", req.Method),
		zap.String("id", reqIDStr))

	// Send request
	if err := c.transport.Send(ctx, reqJSON); err != nil {
		c.logger.Error("Failed to send request via transport",
			zap.String("method", req.Method),
			zap.Error(err))
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	c.logger.Debug("Request sent, waiting for response",
		zap.String("method", req.Method),
		zap.String("id", reqIDStr))

	// Wait for response with timeout
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
			if c.ctx.Err() != nil || errors.Is(err, io.EOF) {
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

// handleRequest handles incoming requests from server (sampling, etc.)
func (c *Client) handleRequest(req *protocol.Request) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Minute)
	defer cancel()

	var resp *protocol.Response
	var err error

	switch req.Method {
	case "sampling/createMessage":
		resp, err = c.handleSamplingRequest(ctx, req)
	default:
		resp = c.createErrorResponse(req.ID, protocol.MethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method), nil)
	}

	if err != nil {
		c.logger.Error("failed to handle request", zap.String("method", req.Method), zap.Error(err))
		return
	}

	// Send response
	respJSON, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		c.logger.Error("failed to marshal response", zap.String("method", req.Method), zap.Error(marshalErr))
		return
	}
	if err := c.transport.Send(ctx, respJSON); err != nil {
		c.logger.Error("failed to send response", zap.Error(err))
	}
}

// handleSamplingRequest processes incoming sampling request from server
func (c *Client) handleSamplingRequest(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	c.mu.RLock()
	handler := c.samplingHandler
	c.mu.RUnlock()

	if handler == nil {
		return c.createErrorResponse(req.ID, protocol.MethodNotFound, "sampling not supported", nil), nil
	}

	// Parse params
	var params protocol.SamplingParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return c.createErrorResponse(req.ID, protocol.InvalidParams, "invalid sampling params", err), nil
	}

	// Call handler
	result, err := handler(ctx, params)
	if err != nil {
		return c.createErrorResponse(req.ID, protocol.InternalError, "sampling failed", err), nil
	}

	// Create response
	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		c.logger.Error("failed to marshal sampling result", zap.Error(marshalErr))
		return c.createErrorResponse(req.ID, protocol.InternalError, "failed to marshal sampling result", marshalErr), nil
	}
	return &protocol.Response{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      req.ID,
		Result:  resultJSON,
	}, nil
}

// SetSamplingHandler registers a handler for sampling requests
func (c *Client) SetSamplingHandler(handler SamplingHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samplingHandler = handler
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
