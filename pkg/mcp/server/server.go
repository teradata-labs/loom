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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// MethodHandler processes a JSON-RPC method call.
// id is the request ID (nil for notifications).
// params is the raw JSON params from the request.
type MethodHandler func(ctx context.Context, id json.RawMessage, params json.RawMessage) (interface{}, error)

// MCPServer is a JSON-RPC based MCP server that dispatches method calls
// to registered handlers.
type MCPServer struct {
	info               protocol.Implementation
	capabilities       protocol.ServerCapabilities
	extensions         map[string]interface{}
	handlers           map[string]MethodHandler
	logger             *zap.Logger
	mu                 sync.RWMutex
	clientInfo         *protocol.Implementation     // Stored after initialize
	clientCapabilities *protocol.ClientCapabilities // Stored after initialize
	notifyCh           chan []byte                  // Buffered channel for outgoing notifications
}

// Option configures an MCPServer.
type Option func(*MCPServer)

// WithToolProvider registers a ToolProvider and enables the tools capability.
func WithToolProvider(p ToolProvider) Option {
	return func(s *MCPServer) {
		s.capabilities.Tools = &protocol.ToolsCapability{}
		s.RegisterHandler("tools/list", newToolsListHandler(p))
		s.RegisterHandler("tools/call", newToolsCallHandler(p))
	}
}

// WithResourceProvider registers a ResourceProvider and enables the resources capability.
// Sets ListChanged: true to indicate the server may send resource list change notifications.
func WithResourceProvider(p ResourceProvider) Option {
	return func(s *MCPServer) {
		s.capabilities.Resources = &protocol.ResourcesCapability{
			ListChanged: true,
		}
		s.RegisterHandler("resources/list", newResourcesListHandler(p))
		s.RegisterHandler("resources/read", newResourcesReadHandler(p))
	}
}

// WithExtensions sets the server's extensions (e.g., MCP Apps).
func WithExtensions(ext map[string]interface{}) Option {
	return func(s *MCPServer) {
		s.extensions = ext
	}
}

// NewMCPServer creates a new MCP server with the given identity and options.
func NewMCPServer(name, version string, logger *zap.Logger, opts ...Option) *MCPServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	s := &MCPServer{
		info: protocol.Implementation{
			Name:    name,
			Version: version,
		},
		handlers: make(map[string]MethodHandler),
		logger:   logger,
		notifyCh: make(chan []byte, 16),
	}

	// Register built-in handlers
	s.RegisterHandler("initialize", s.handleInitialize)
	s.RegisterHandler("notifications/initialized", s.handleNotificationsInitialized)
	s.RegisterHandler("ping", s.handlePing)

	// Apply options
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// RegisterHandler registers a handler for a JSON-RPC method.
func (s *MCPServer) RegisterHandler(method string, handler MethodHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

// HandleMessage processes a single JSON-RPC message and returns the response bytes.
// For notifications (no id), returns nil.
func (s *MCPServer) HandleMessage(ctx context.Context, msg []byte) ([]byte, error) {
	var req protocol.Request
	if err := json.Unmarshal(msg, &req); err != nil {
		return marshalResponse(nil, nil, protocol.NewError(protocol.ParseError, "invalid JSON", nil))
	}

	if err := protocol.ValidateRequest(&req); err != nil {
		return marshalResponse(nil, nil, protocol.NewError(protocol.InvalidRequest, err.Error(), nil))
	}

	s.logger.Debug("handling request", zap.String("method", req.Method), zap.Any("id", req.ID))
	start := time.Now()

	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		// Unknown method
		if req.ID == nil {
			// Notification for unknown method - ignore silently
			return nil, nil
		}
		return marshalResponse(req.ID, nil, protocol.NewError(protocol.MethodNotFound, fmt.Sprintf("method not found: %s", req.Method), nil))
	}

	// Extract raw ID for the handler
	var rawID json.RawMessage
	if req.ID != nil {
		idBytes, err := json.Marshal(req.ID)
		if err != nil {
			return marshalResponse(nil, nil, protocol.NewError(protocol.InternalError, "failed to marshal request ID", nil))
		}
		rawID = idBytes
	}

	result, err := handler(ctx, rawID, req.Params)
	duration := time.Since(start)

	if err != nil {
		// Handler returned an error
		s.logger.Warn("handler error",
			zap.String("method", req.Method),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		if req.ID == nil {
			// Notification - don't send error response
			return nil, nil
		}
		// Preserve original JSON-RPC error code if the handler returned a *protocol.Error
		var rpcErr *protocol.Error
		if errors.As(err, &rpcErr) {
			return marshalResponse(req.ID, nil, rpcErr)
		}
		return marshalResponse(req.ID, nil, protocol.NewError(protocol.InternalError, err.Error(), nil))
	}

	s.logger.Debug("request handled",
		zap.String("method", req.Method),
		zap.Duration("duration", duration),
	)

	if req.ID == nil {
		// Notification - no response
		return nil, nil
	}

	return marshalResponse(req.ID, result, nil)
}

// Serve runs the server's read loop on the given transport until the context
// is cancelled or the transport is closed. It concurrently handles incoming
// messages and dispatches outgoing notifications via the notification channel.
func (s *MCPServer) Serve(ctx context.Context, t transport.Transport) error {
	s.logger.Info("MCP server starting", zap.String("name", s.info.Name), zap.String("version", s.info.Version))

	// Use a goroutine for receiving to enable select on both receive and notify channels.
	msgCh := make(chan []byte)
	errCh := make(chan error, 1)
	go func() {
		for {
			msg, err := t.Receive(ctx)
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("MCP server stopping (context cancelled)")
			return ctx.Err()

		case err := <-errCh:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Error("receive error", zap.Error(err))
			return fmt.Errorf("receive error: %w", err)

		case msg := <-msgCh:
			resp, err := s.HandleMessage(ctx, msg)
			if err != nil {
				s.logger.Error("handle error", zap.Error(err))
				continue
			}
			if resp == nil {
				continue
			}
			if err := t.Send(ctx, resp); err != nil {
				s.logger.Error("send error", zap.Error(err))
				return fmt.Errorf("send error: %w", err)
			}

		case notif := <-s.notifyCh:
			if err := t.Send(ctx, notif); err != nil {
				s.logger.Error("notification send error", zap.Error(err))
				return fmt.Errorf("notification send error: %w", err)
			}
		}
	}
}

// handleInitialize processes the initialize request.
func (s *MCPServer) handleInitialize(_ context.Context, _ json.RawMessage, params json.RawMessage) (interface{}, error) {
	var initParams protocol.InitializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &initParams); err != nil {
			return nil, protocol.NewError(protocol.InvalidParams, fmt.Sprintf("invalid initialize params: %v", err), nil)
		}
	}

	// Validate protocol version compatibility
	if initParams.ProtocolVersion != "" && initParams.ProtocolVersion != protocol.ProtocolVersion {
		s.logger.Warn("client protocol version mismatch",
			zap.String("client_version", initParams.ProtocolVersion),
			zap.String("server_version", protocol.ProtocolVersion),
		)
	}

	// Store client info and capabilities for observability
	s.mu.Lock()
	caps := initParams.Capabilities
	s.clientCapabilities = &caps
	if initParams.ClientInfo.Name != "" {
		s.clientInfo = &initParams.ClientInfo
	}
	s.mu.Unlock()

	if initParams.ClientInfo.Name != "" {
		s.logger.Info("client connected",
			zap.String("client_name", initParams.ClientInfo.Name),
			zap.String("client_version", initParams.ClientInfo.Version),
			zap.Bool("supports_sampling", initParams.Capabilities.Sampling != nil),
			zap.Bool("supports_roots", initParams.Capabilities.Roots != nil),
		)
	}

	result := protocol.InitializeResult{
		ProtocolVersion: protocol.ProtocolVersion,
		Capabilities:    s.capabilities,
		ServerInfo:      s.info,
		Extensions:      s.extensions,
	}
	return result, nil
}

// handleNotificationsInitialized handles the initialized notification (no-op).
func (s *MCPServer) handleNotificationsInitialized(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
	s.logger.Debug("client initialized")
	return nil, nil
}

// handlePing handles the ping request.
func (s *MCPServer) handlePing(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
	return struct{}{}, nil
}

// ClientInfo returns the connected client's information, or nil if not yet initialized.
func (s *MCPServer) ClientInfo() *protocol.Implementation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientInfo
}

// ClientCapabilities returns the connected client's capabilities, or nil if not yet initialized.
func (s *MCPServer) ClientCapabilities() *protocol.ClientCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientCapabilities
}

// NotifyResourceListChanged enqueues a resources/list_changed notification.
// The notification is sent asynchronously via the Serve() select loop.
// If the channel is full the notification is dropped with a warning log.
func (s *MCPServer) NotifyResourceListChanged() {
	notif, err := marshalNotification("notifications/resources/list_changed", nil)
	if err != nil {
		s.logger.Error("failed to marshal resource list changed notification", zap.Error(err))
		return
	}
	select {
	case s.notifyCh <- notif:
		s.logger.Debug("enqueued resources/list_changed notification")
	default:
		s.logger.Warn("notification channel full, dropping resources/list_changed")
	}
}

// marshalNotification creates a JSON-RPC notification (no id field).
func marshalNotification(method string, params interface{}) ([]byte, error) {
	msg := struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  method,
		Params:  params,
	}
	return json.Marshal(msg)
}

// marshalResponse creates a JSON-RPC response.
func marshalResponse(id *protocol.RequestID, result interface{}, rpcErr *protocol.Error) ([]byte, error) {
	resp := protocol.Response{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      id,
		Error:   rpcErr,
	}

	if result != nil {
		resultBytes, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}
		resp.Result = resultBytes
	}

	return json.Marshal(resp)
}
