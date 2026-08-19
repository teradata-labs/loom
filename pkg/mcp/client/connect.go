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
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// errProbeTimeout marks a server/discover probe that produced no answer
// within the client's bounded probe window while the caller's context was
// still alive. On transports without per-request status signals (stdio) the
// specification classifies this silence as a legacy server; on Streamable
// HTTP it stays a hard error, since status codes — not timeouts — carry the
// era signal there.
var errProbeTimeout = errors.New("server/discover probe timed out without a response")

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
// mandatory on new servers and safe against old ones, which answer with an
// implementation-defined JSON-RPC error (commonly MethodNotFound or
// InvalidParams — the fallback is deliberately not keyed to one code, per
// the stdio backward-compatibility rule), stay silent past the bounded probe
// timeout (stdio only), or — for HTTP servers predating the modern endpoint —
// return a bare 404/405/501 or a 400 without a modern error body. On a
// successful discover, the client enters stateless mode for 2026-07-28+
// revisions, stamping protocol version, client capabilities, and client
// identity into params._meta on every request. When the server only offers
// pre-2026 revisions, or does not implement discover at all, Connect falls
// back to the initialize handshake.
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
// falling back to the initialize handshake for pre-discover servers. A probe
// timeout counts as a legacy signal only off Streamable HTTP: the stdio
// backward-compatibility rule says a server that does not respond within a
// reasonable timeout is legacy, while on HTTP the era signal is carried by
// status codes and a hung POST stays a hard error.
func (c *Client) connectAuto(ctx context.Context, clientInfo protocol.Implementation) error {
	disc, err := c.discover(ctx)
	if err != nil {
		timedOutLegacy := errors.Is(err, errProbeTimeout) && !c.transportCarriesHeaders()
		if isLegacyServerSignal(err) || timedOutLegacy {
			c.logger.Debug("server/discover answered as a pre-2026 server; falling back to initialize handshake",
				zap.Error(err))
			return c.Initialize(ctx, clientInfo)
		}
		return fmt.Errorf("server/discover failed: %w", err)
	}

	version, ok := protocol.NegotiateVersion(disc.SupportedVersions)
	if !ok {
		return protocol.NewError(protocol.UnsupportedProtocolVersion,
			fmt.Sprintf("no mutually supported protocol revision: server offers %v", disc.SupportedVersions),
			nil)
	}

	if !protocol.IsStatelessVersion(version) {
		// The server implements discover but the best common revision still
		// uses the handshake. Run it requesting exactly the revision discover
		// selected so both sides agree on lifecycle.
		c.logger.Debug("negotiated legacy revision via discover; running initialize handshake",
			zap.String("version", version))
		return c.initializeWithVersion(ctx, clientInfo, version)
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
		// The handshake must request the pinned revision itself: requesting
		// any other version invites a dual-revision server to negotiate it,
		// which would then fail the pin check below for no real reason.
		if err := c.initializeWithVersion(ctx, clientInfo, pin); err != nil {
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
	for _, v := range disc.SupportedVersions {
		if v == pin {
			c.enterStatelessMode(pin, disc)
			return nil
		}
	}
	return protocol.NewError(protocol.UnsupportedProtocolVersion,
		fmt.Sprintf("protocol version pinned to %s but server offers %v", pin, disc.SupportedVersions),
		nil)
}

// isLegacyServerSignal reports whether a failed server/discover probe
// indicates a pre-2026 server rather than a genuine failure.
//
// At the JSON-RPC layer, any error other than the recognized modern codes is
// a legacy signal. The stdio backward-compatibility rule is explicit that
// the fallback MUST NOT be keyed to one specific error code: legacy servers
// answer unknown pre-initialize requests with implementation-defined errors
// (commonly -32601 MethodNotFound or -32602 InvalidParams, e.g. "initialize
// first"). Conversely a recognized modern error — UnsupportedProtocolVersion,
// HeaderMismatch, MissingRequiredClientCapability — identifies a modern
// server, and the client must not fall back over it.
//
// At the HTTP layer, servers predating the modern endpoint (or strict
// gateways in front of them) answer with a bare 404, 405, or 501. A 400
// falls back only when its body is not a recognized modern JSON-RPC error —
// strict legacy session servers reject any pre-initialize request with a
// bare 400 ("session required"), while modern servers put
// UnsupportedProtocolVersionError or HeaderMismatch in the body (routable
// bodies never even reach here: the transport delivers them as typed
// protocol errors). Auth failures and server errors are never treated as a
// legacy signal.
func isLegacyServerSignal(err error) bool {
	var rpcErr *protocol.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case protocol.HeaderMismatch, protocol.MissingRequiredClientCapability, protocol.UnsupportedProtocolVersion:
			return false // recognized modern errors: a modern server answered
		default:
			return true
		}
	}
	var httpErr *transport.HTTPStatusError
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
			return true
		case http.StatusBadRequest:
			return !hasModernErrorBody(httpErr.Body)
		}
	}
	return false
}

// hasModernErrorBody reports whether an HTTP error body carries a JSON-RPC
// error with one of the codes only modern (2026-07-28+) servers emit. Such a
// body identifies a modern server even when it cannot be routed to the
// pending request (e.g. the specification permits id-less error responses),
// so the client must not fall back to the legacy handshake over it.
func hasModernErrorBody(body []byte) bool {
	var probe struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Error == nil {
		return false
	}
	switch probe.Error.Code {
	case protocol.HeaderMismatch, protocol.MissingRequiredClientCapability, protocol.UnsupportedProtocolVersion:
		return true
	}
	return false
}

// enterStatelessMode records the negotiated stateless revision and the
// server identity discover reported in the result's _meta block (a SHOULD in
// the specification, so a missing identity is tolerated).
func (c *Client) enterStatelessMode(version string, disc *protocol.DiscoverResult) {
	serverInfo, hasInfo := disc.ServerInfo()

	c.mu.Lock()
	c.initialized = true
	c.statelessMode = true
	c.protocolVersion = version
	c.serverInfo = serverInfo
	c.serverCapabilities = disc.Capabilities
	c.mu.Unlock()

	c.logger.Info("MCP client connected (stateless revision)",
		zap.String("version", version),
		zap.String("server", serverInfo.Name),
		zap.String("serverVersion", serverInfo.Version),
		zap.Bool("serverIdentified", hasInfo),
		zap.Bool("tools", disc.Capabilities.Tools != nil),
		zap.Bool("resources", disc.Capabilities.Resources != nil),
		zap.Bool("prompts", disc.Capabilities.Prompts != nil),
	)
}

// discover calls server/discover. The probe is a first-class 2026-07-28
// request: its params carry the standard _meta identity keys stamped at the
// client's preferred revision, and the MCP-Protocol-Version header must match
// the version in _meta — conforming servers reject a mismatch (or a missing
// body field) with HeaderMismatch.
//
// The wait is bounded by the client's probe timeout, independent of the
// caller's context: a legacy server may never answer the probe at all, and
// the fallback handshake needs the caller's context still alive when the
// probe gives up. A probe that produces nothing within the window while the
// caller's context is still live returns errProbeTimeout.
func (c *Client) discover(ctx context.Context) (*protocol.DiscoverResult, error) {
	c.mu.RLock()
	info := c.clientInfo
	c.mu.RUnlock()

	params, err := protocol.StampMeta(nil, protocol.PreferredVersion, info, c.clientCapabilities())
	if err != nil {
		return nil, fmt.Errorf("failed to stamp server/discover params: %w", err)
	}

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "server/discover",
		Params:  params,
	}

	probeCtx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	probeCtx = transport.WithExtraHeaders(probeCtx, map[string]string{
		"MCP-Protocol-Version": protocol.PreferredVersion,
	})

	resp, err := c.sendRequest(probeCtx, req)
	if err != nil {
		if probeCtx.Err() != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("%w after %s", errProbeTimeout, c.probeTimeout())
		}
		return nil, err
	}

	var result protocol.DiscoverResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse server/discover result: %w", err)
	}
	if len(result.SupportedVersions) == 0 {
		return nil, fmt.Errorf("server/discover returned no supported versions")
	}
	return &result, nil
}

// probeTimeout is the bounded wait for the server/discover probe,
// configured via Config.RequestTimeout.
func (c *Client) probeTimeout() time.Duration {
	if c.requestTimeout > 0 {
		return c.requestTimeout
	}
	return 30 * time.Second
}

// IsStateless reports whether the client negotiated a 2026-07-28+ revision.
func (c *Client) IsStateless() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statelessMode
}
