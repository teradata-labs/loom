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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// DefaultRequestTimeout is the per-RPC timeout applied to every gRPC call
// made through the bridge. Callers can override this with BridgeOption.
const DefaultRequestTimeout = 30 * time.Second

// LoomBridge maps Loom's gRPC API to MCP tool and resource providers.
// It connects to a running looms server and exposes its capabilities
// as MCP tools for clients like Claude Desktop.
type LoomBridge struct {
	conn           *grpc.ClientConn
	client         loomv1.LoomServiceClient
	uiRegistry     *apps.UIResourceRegistry
	logger         *zap.Logger
	requestTimeout time.Duration          // per-RPC timeout for gRPC calls
	tlsCertFile    string                 // optional path to CA certificate for TLS
	tlsSkipVerify  bool                   // skip server certificate verification (insecure)
	tlsEnabled     bool                   // whether TLS is explicitly enabled
	tools          []protocol.Tool        // cached tool definitions
	handlers       map[string]toolHandler // cached tool handlers (built once)
}

// BridgeOption configures a LoomBridge.
type BridgeOption func(*LoomBridge)

// WithRequestTimeout sets the per-RPC timeout for gRPC calls.
func WithRequestTimeout(d time.Duration) BridgeOption {
	return func(b *LoomBridge) {
		b.requestTimeout = d
	}
}

// WithTLS configures TLS for the gRPC connection to the looms server.
// certFile is the path to a PEM-encoded CA certificate. If empty, the system
// certificate pool is used. Set skipVerify to true to skip server certificate
// verification -- this is NOT recommended for production deployments.
func WithTLS(certFile string, skipVerify bool) BridgeOption {
	return func(b *LoomBridge) {
		b.tlsEnabled = true
		b.tlsCertFile = certFile
		b.tlsSkipVerify = skipVerify
	}
}

// NewLoomBridge creates a bridge to a running looms server.
func NewLoomBridge(grpcAddr string, uiRegistry *apps.UIResourceRegistry, logger *zap.Logger, opts ...BridgeOption) (*LoomBridge, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Apply options first so TLS config is available before dialing.
	bridge := &LoomBridge{
		uiRegistry:     uiRegistry,
		logger:         logger,
		requestTimeout: DefaultRequestTimeout,
	}
	for _, opt := range opts {
		opt(bridge)
	}

	// Build transport credentials based on TLS configuration.
	creds, err := bridge.buildTransportCredentials()
	if err != nil {
		return nil, fmt.Errorf("configure transport credentials: %w", err)
	}

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("connect to looms at %s: %w", grpcAddr, err)
	}

	bridge.conn = conn
	bridge.client = loomv1.NewLoomServiceClient(conn)
	bridge.tools = bridge.buildToolDefinitions()
	bridge.handlers = bridge.buildToolHandlers()
	return bridge, nil
}

// buildTransportCredentials returns the appropriate gRPC transport credentials.
// When TLS is enabled it loads the CA cert (or uses the system pool) and
// optionally skips server verification. Otherwise it returns insecure credentials
// for backwards-compatible localhost development.
func (b *LoomBridge) buildTransportCredentials() (credentials.TransportCredentials, error) {
	if !b.tlsEnabled {
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: b.tlsSkipVerify, // #nosec G402 -- opt-in via WithTLS option
	}

	if b.tlsCertFile != "" {
		pemBytes, err := os.ReadFile(b.tlsCertFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS CA cert %s: %w", b.tlsCertFile, err)
		}
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", b.tlsCertFile)
		}
		tlsCfg.RootCAs = certPool
	}

	return credentials.NewTLS(tlsCfg), nil
}

// NewLoomBridgeFromClient creates a bridge from an existing gRPC client.
// Useful for testing with mock clients.
func NewLoomBridgeFromClient(client loomv1.LoomServiceClient, uiRegistry *apps.UIResourceRegistry, logger *zap.Logger, opts ...BridgeOption) *LoomBridge {
	if logger == nil {
		logger = zap.NewNop()
	}

	bridge := &LoomBridge{
		client:         client,
		uiRegistry:     uiRegistry,
		logger:         logger,
		requestTimeout: DefaultRequestTimeout,
	}
	for _, opt := range opts {
		opt(bridge)
	}

	bridge.tools = bridge.buildToolDefinitions()
	bridge.handlers = bridge.buildToolHandlers()
	return bridge
}

// Close closes the gRPC connection.
func (b *LoomBridge) Close() error {
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}

// ListTools implements ToolProvider.
func (b *LoomBridge) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return b.tools, nil
}

// CallTool implements ToolProvider.
func (b *LoomBridge) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	handler, ok := b.handlers[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}

	b.logger.Debug("calling tool", zap.String("tool", name))
	return handler(ctx, args)
}

// ListResources implements ResourceProvider.
func (b *LoomBridge) ListResources(_ context.Context) ([]protocol.Resource, error) {
	if b.uiRegistry == nil {
		return []protocol.Resource{}, nil
	}
	return b.uiRegistry.List(), nil
}

// ReadResource implements ResourceProvider.
func (b *LoomBridge) ReadResource(_ context.Context, uri string) (*protocol.ReadResourceResult, error) {
	if b.uiRegistry == nil {
		return nil, fmt.Errorf("no UI registry configured")
	}
	return b.uiRegistry.Read(uri)
}

// toolHandler is a function that handles a specific tool call.
type toolHandler func(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error)

// callGRPC is a generic helper that marshals args to a proto request, calls the RPC,
// and returns the response as an MCP tool result. A per-request deadline is applied
// via timeout; if the parent context already has a shorter deadline it takes precedence.
func callGRPC[Req proto.Message, Resp proto.Message](
	ctx context.Context,
	timeout time.Duration,
	args map[string]interface{},
	newReq func() Req,
	rpc func(context.Context, Req, ...grpc.CallOption) (Resp, error),
) (*protocol.CallToolResult, error) {
	req := newReq()

	// Marshal args to JSON, then unmarshal into proto message
	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
		if err := protojson.Unmarshal(argsJSON, req); err != nil {
			return nil, fmt.Errorf("unmarshal args to proto: %w", err)
		}
	}

	// Apply per-request timeout so a hung server cannot block forever.
	rpcCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := rpc(rpcCtx, req)
	if err != nil {
		return nil, err
	}

	// Marshal proto response to JSON for the MCP result
	respJSON, err := protojson.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	return &protocol.CallToolResult{
		Content: []protocol.Content{
			{Type: "text", Text: string(respJSON)},
		},
	}, nil
}
