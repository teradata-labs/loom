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
// This file implements revision-aware connection setup: it probes
// server/discover (2026-07-28) and falls back to the initialize handshake for
// servers on earlier revisions. Config.ProtocolVersion pins the outcome.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// InputRequiredNotSupportedError is returned when a server answers with an
// MRTR interim result (resultType "input_required") and this client has no
// MRTR driver configured. The MRTR retry loop (migration Phase 4) replaces
// this fail-fast with a driver that answers inputRequests.
type InputRequiredNotSupportedError struct {
	Method string
}

func (e *InputRequiredNotSupportedError) Error() string {
	return fmt.Sprintf("%s returned input_required (MRTR), which this client does not drive yet", e.Method)
}

// Connect negotiates the protocol revision with the server and prepares the
// client for requests. It should be used in place of Initialize by callers
// that want automatic revision selection; Initialize remains available for
// callers that must force the legacy handshake.
//
// The probe order follows the 2026-07-28 specification: server/discover is
// mandatory on new servers and safe against old ones, which answer
// MethodNotFound (or, for HTTP servers predating the modern endpoint, a bare
// 404/405/501). On a successful discover, the client enters stateless mode
// for 2026-07-28+ revisions, stamping protocol version, client capabilities,
// and client identity into params._meta on every request. When the server
// only offers pre-2026 revisions, or does not implement discover at all,
// Connect falls back to the initialize handshake.
//
// Config.ProtocolVersion overrides negotiation: "legacy" skips the probe and
// runs the handshake directly; an explicit revision requires the server to
// speak exactly that revision and fails without fallback.
func (c *Client) Connect(ctx context.Context, clientInfo protocol.Implementation) error {
	c.mu.Lock()
	if c.initialized {
		c.mu.Unlock()
		return fmt.Errorf("already connected")
	}
	c.clientInfo = clientInfo
	pin := c.versionPin
	c.mu.Unlock()

	switch pin {
	case "", "auto":
		return c.connectAuto(ctx, clientInfo)
	case "legacy":
		c.logger.Info("protocol version pinned to legacy handshake; skipping server/discover probe")
		return c.Initialize(ctx, clientInfo)
	default:
		return c.connectPinned(ctx, clientInfo, pin)
	}
}

// connectAuto probes server/discover and negotiates the best mutual revision,
// falling back to the initialize handshake for pre-discover servers.
func (c *Client) connectAuto(ctx context.Context, clientInfo protocol.Implementation) error {
	disc, err := c.discover(ctx)
	if err != nil {
		if isLegacyServerSignal(err) {
			c.logger.Debug("server/discover not implemented; falling back to initialize handshake",
				zap.Error(err))
			return c.Initialize(ctx, clientInfo)
		}
		return fmt.Errorf("server/discover failed: %w", err)
	}

	version, ok := protocol.NegotiateVersion(disc.ProtocolVersions)
	if !ok {
		return protocol.NewError(protocol.UnsupportedProtocolVersion,
			fmt.Sprintf("no mutually supported protocol revision: server offers %v", disc.ProtocolVersions),
			nil)
	}

	if !protocol.IsStatelessVersion(version) {
		// The server implements discover but the best common revision still
		// uses the handshake. Run it so both sides agree on lifecycle.
		c.logger.Debug("negotiated legacy revision via discover; running initialize handshake",
			zap.String("version", version))
		return c.Initialize(ctx, clientInfo)
	}

	c.enterStatelessMode(version, disc)
	return nil
}

// connectPinned enforces an explicitly configured revision. There is no
// fallback: a pin exists so an operator can rely on the outcome.
func (c *Client) connectPinned(ctx context.Context, clientInfo protocol.Implementation, pin string) error {
	if !protocol.IsSupportedVersion(pin) {
		return fmt.Errorf("pinned protocol version %q is not supported by this client", pin)
	}

	if !protocol.IsStatelessVersion(pin) {
		if err := c.Initialize(ctx, clientInfo); err != nil {
			return err
		}
		if got := c.NegotiatedVersion(); got != pin {
			return fmt.Errorf("protocol version pinned to %s but server negotiated %s", pin, got)
		}
		return nil
	}

	disc, err := c.discover(ctx)
	if err != nil {
		return fmt.Errorf("protocol version pinned to %s but server/discover failed: %w", pin, err)
	}
	for _, v := range disc.ProtocolVersions {
		if v == pin {
			c.enterStatelessMode(pin, disc)
			return nil
		}
	}
	return protocol.NewError(protocol.UnsupportedProtocolVersion,
		fmt.Sprintf("protocol version pinned to %s but server offers %v", pin, disc.ProtocolVersions),
		nil)
}

// isLegacyServerSignal reports whether a failed server/discover probe
// indicates a pre-2026 server rather than a genuine failure. Conformant
// legacy servers answer MethodNotFound at the JSON-RPC layer; HTTP servers
// predating the modern endpoint (or strict gateways in front of them) may
// instead answer with a bare 404, 405, or 501. Auth failures and server
// errors are never treated as a legacy signal.
func isLegacyServerSignal(err error) bool {
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) && rpcErr.Code == protocol.MethodNotFound {
		return true
	}
	var httpErr *transport.HTTPStatusError
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			return true
		}
	}
	return false
}

// enterStatelessMode records the negotiated stateless revision and the
// server identity discover reported.
func (c *Client) enterStatelessMode(version string, disc *protocol.DiscoverResult) {
	c.mu.Lock()
	c.initialized = true
	c.statelessMode = true
	c.protocolVersion = version
	c.serverInfo = disc.ServerInfo
	c.serverCapabilities = disc.Capabilities
	c.mu.Unlock()

	c.logger.Info("MCP client connected (stateless revision)",
		zap.String("version", version),
		zap.String("server", disc.ServerInfo.Name),
		zap.String("serverVersion", disc.ServerInfo.Version),
		zap.Bool("tools", disc.Capabilities.Tools != nil),
		zap.Bool("resources", disc.Capabilities.Resources != nil),
		zap.Bool("prompts", disc.Capabilities.Prompts != nil),
	)
}

// discover calls server/discover. It is issued before any mode is selected,
// so the request is intentionally not stamped with _meta; the required
// MCP-Protocol-Version header carries the client's preferred revision.
func (c *Client) discover(ctx context.Context) (*protocol.DiscoverResult, error) {
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "server/discover",
		Params:  json.RawMessage(`{}`),
	}

	ctx = transport.WithExtraHeaders(ctx, map[string]string{
		"MCP-Protocol-Version": protocol.PreferredVersion,
	})

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var result protocol.DiscoverResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse server/discover result: %w", err)
	}
	if len(result.ProtocolVersions) == 0 {
		return nil, fmt.Errorf("server/discover returned no protocol versions")
	}
	return &result, nil
}

// IsStateless reports whether the client negotiated a 2026-07-28+ revision.
func (c *Client) IsStateless() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statelessMode
}

// checkResultEnvelope inspects a result's revision-level envelope and rejects
// interim MRTR results. It runs centrally in sendRequest for every stateless
// request, so no method-specific decoder can mistake an input_required
// interim result for the final one.
func checkResultEnvelope(method string, result json.RawMessage) error {
	env := protocol.ParseResultEnvelope(result)
	if env.ResultType == protocol.ResultTypeInputRequired {
		return &InputRequiredNotSupportedError{Method: method}
	}
	return nil
}
