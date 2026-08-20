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
// This file implements the client side of subscriptions/listen (2026-07-28):
// one long-lived request whose response stream carries opted-in change
// notifications, demultiplexed on io.modelcontextprotocol/subscriptionId.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// Subscription is one active subscriptions/listen stream.
type Subscription struct {
	// ID is the subscription identifier: the JSON-RPC id of the
	// subscriptions/listen request, as notifications carry it.
	ID string

	// Notifications delivers the acknowledgment
	// (notifications/subscriptions/acknowledged) followed by the subscribed
	// change notifications. It is closed when the subscription ends.
	Notifications <-chan Notification

	done chan struct{}

	errMu sync.Mutex
	err   error // nil on graceful closure
}

// Done is closed when the subscription ends, gracefully or not.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Err reports why the subscription ended: nil for graceful closure (the
// server answered the listen request), non-nil for an abnormal end such as a
// lost stream or a server that answers MethodNotFound (no subscriptions
// support). Meaningful once the subscription has ended.
func (s *Subscription) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// subscriptionEntry is the client-side registration a demuxed notification
// lands in.
type subscriptionEntry struct {
	notifCh chan Notification
	sub     *Subscription
}

// Subscribe opens a subscriptions/listen stream for the given filter. It is
// only available on stateless (2026-07-28+) connections; legacy connections
// keep the capability-gated resources/subscribe path.
//
// The returned Subscription's Notifications channel delivers the server's
// acknowledgment first, then the subscribed notifications. The subscription
// ends when the server closes it gracefully (Err() == nil), the stream is
// lost (Err() != nil — reconnect by calling Subscribe again and refetching
// affected lists), or ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context, filter protocol.NotificationFilter) (*Subscription, error) {
	if !c.IsStateless() {
		return nil, fmt.Errorf("subscriptions/listen requires a 2026-07-28 connection; legacy connections use resources/subscribe")
	}

	c.mu.RLock()
	version := c.protocolVersion
	info := c.clientInfo
	c.mu.RUnlock()

	paramsJSON, err := json.Marshal(protocol.ListenParams{Notifications: filter})
	if err != nil {
		return nil, err
	}
	stamped, err := protocol.StampMeta(paramsJSON, version, info, c.clientCapabilities())
	if err != nil {
		return nil, fmt.Errorf("failed to stamp _meta: %w", err)
	}

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  protocol.MethodSubscriptionsListen,
		Params:  stamped,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	subID := req.ID.String()

	sub := &Subscription{
		ID:   subID,
		done: make(chan struct{}),
	}
	notifCh := make(chan Notification, 64)
	sub.Notifications = notifCh

	// Register the demux entry and the pending slot for the final response
	// (graceful closure, or the transport's synthesized stream-lost error)
	// before sending, so nothing can race the acknowledgment.
	respChan := make(chan *protocol.Response, 1)
	c.subsMu.Lock()
	c.subs[subID] = &subscriptionEntry{notifCh: notifCh, sub: sub}
	c.subsMu.Unlock()
	c.pendingMu.Lock()
	c.pending[subID] = respChan
	c.pendingMu.Unlock()

	cleanup := func(endErr error) {
		// The terminal error is published before anything externally visible
		// closes: a consumer that observes the closed Notifications channel
		// (or Done) must never read Err() == nil for an abnormal end.
		sub.errMu.Lock()
		sub.err = endErr
		sub.errMu.Unlock()
		// notifCh is closed under the write lock: dispatchNotification sends
		// under the read lock (non-blocking), so close and send are mutually
		// exclusive and a send-on-closed-channel panic is impossible.
		c.subsMu.Lock()
		delete(c.subs, subID)
		close(notifCh)
		c.subsMu.Unlock()
		c.pendingMu.Lock()
		delete(c.pending, subID)
		c.pendingMu.Unlock()
		close(sub.done)
	}

	ctx = transport.WithExtraHeaders(ctx, map[string]string{"MCP-Protocol-Version": version})
	if err := c.transport.Send(ctx, reqJSON); err != nil {
		cleanup(err)
		return nil, fmt.Errorf("failed to open subscriptions/listen: %w", err)
	}

	c.logger.Debug("subscriptions/listen opened", zap.String("subscription_id", subID))

	// Watch for the subscription's end: the JSON-RPC response to the listen
	// request (graceful closure or synthesized stream-lost error), caller
	// cancellation, or client shutdown.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		select {
		case resp := <-respChan:
			if resp.Error != nil {
				c.logger.Warn("subscription ended abnormally",
					zap.String("subscription_id", subID),
					zap.String("error", resp.Error.Error()))
				cleanup(resp.Error)
				return
			}
			c.logger.Debug("subscription closed gracefully", zap.String("subscription_id", subID))
			cleanup(nil)
		case <-ctx.Done():
			// Caller cancellation must reach the server, not just local
			// state. On Streamable HTTP the request context's cancellation
			// already closes the listen SSE stream — the transport-level
			// cancellation signal; on other transports (stdio) the protocol
			// requires notifications/cancelled naming the listen request.
			c.cancelSubscriptionUpstream(req.ID)
			cleanup(ctx.Err())
		case <-c.ctx.Done():
			// Client shutdown tears the whole transport down; no
			// per-subscription cancellation is needed.
			cleanup(c.ctx.Err())
		}
	}()

	return sub, nil
}

// cancelSubscriptionUpstream sends notifications/cancelled for a cancelled
// subscription on transports where closing the response stream is not the
// cancellation signal. Without it, a stdio server keeps the subscription
// alive and keeps emitting notifications nobody consumes.
func (c *Client) cancelSubscriptionUpstream(listenID *protocol.RequestID) {
	if c.transportCarriesHeaders() {
		return // Streamable HTTP: closing the stream is the cancellation
	}

	params, err := json.Marshal(protocol.CancelledParams{
		RequestID: listenID,
		Reason:    "subscription cancelled by client",
	})
	if err != nil {
		c.logger.Warn("failed to marshal cancellation params", zap.Error(err))
		return
	}
	notif := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  protocol.NotificationCancelled,
		Params:  params,
	}
	data, err := json.Marshal(notif)
	if err != nil {
		c.logger.Warn("failed to marshal cancellation notification", zap.Error(err))
		return
	}

	// The subscription's own context is already cancelled; the notification
	// gets a short lease tied to the client lifecycle instead.
	sendCtx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.transport.Send(sendCtx, data); err != nil {
		c.logger.Debug("failed to send subscription cancellation", zap.Error(err))
		return
	}
	c.logger.Debug("subscription cancellation sent", zap.String("subscription_id", listenID.String()))
}

// transportCarriesHeaders reports whether the transport mirrors requests
// into per-request HTTP headers (Streamable HTTP). Header-conditional
// behaviors — x-mcp-header handling, stream-close-as-cancellation — key off
// this.
func (c *Client) transportCarriesHeaders() bool {
	if hc, ok := c.transport.(transport.RequestHeaderCarrier); ok {
		return hc.CarriesRequestHeaders()
	}
	return false
}

// dispatchNotification routes a server notification: subscription-tagged
// notifications go to their subscription's channel, everything else to the
// client's generic notifications channel. Notifications are never answered.
func (c *Client) dispatchNotification(method string, params json.RawMessage) {
	notif := Notification{Method: method, Params: params}

	if subID, ok := subscriptionIDFromParams(params); ok {
		// The non-blocking send happens under the read lock so it cannot
		// race the close in Subscribe's cleanup (which holds the write lock).
		c.subsMu.RLock()
		entry, exists := c.subs[subID]
		if exists {
			select {
			case entry.notifCh <- notif:
			default:
				c.logger.Warn("subscription notification dropped: consumer too slow",
					zap.String("subscription_id", subID),
					zap.String("method", method))
			}
		}
		c.subsMu.RUnlock()
		if !exists {
			c.logger.Debug("notification for unknown subscription",
				zap.String("subscription_id", subID), zap.String("method", method))
		}
		return
	}

	select {
	case c.notifications <- notif:
	default:
		c.logger.Debug("generic notification dropped: channel full", zap.String("method", method))
	}
}

// subscriptionIDFromParams extracts io.modelcontextprotocol/subscriptionId
// from a notification's params._meta, normalized to the same string form
// RequestID.String() produces so demux keys match.
func subscriptionIDFromParams(params json.RawMessage) (string, bool) {
	var probe struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &probe); err != nil {
		return "", false
	}
	raw, ok := probe.Meta[protocol.MetaSubscriptionID]
	if !ok {
		return "", false
	}
	var asNum int64
	if err := json.Unmarshal(raw, &asNum); err == nil {
		return strconv.FormatInt(asNum, 10), true
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return asStr, true
	}
	return "", false
}
