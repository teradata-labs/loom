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
// This file implements subscriptions/listen on the stdio transport
// (2026-07-28): all messages share one channel, so each subscription is a
// registration whose notifications are interleaved onto the connection,
// tagged with io.modelcontextprotocol/subscriptionId, acknowledged before
// anything else, and cancelled via notifications/cancelled referencing the
// listen request's id — the stdio binding's cancellation signal.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// stdioConn tracks one stdio connection's subscription state: JSON-RPC ids
// are connection-scoped, so duplicate detection and cancellation routing
// live here rather than in the server-global registry.
type stdioConn struct {
	mu      sync.Mutex
	listens map[string]context.CancelFunc // listen request id → pump cancel
}

func newStdioConn() *stdioConn {
	return &stdioConn{listens: make(map[string]context.CancelFunc)}
}

func (c *stdioConn) add(id string, cancel context.CancelFunc) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.listens[id]; exists {
		return false
	}
	c.listens[id] = cancel
	return true
}

func (c *stdioConn) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.listens, id)
}

// cancel ends the subscription opened by the given listen request id,
// reporting whether one existed.
func (c *stdioConn) cancel(id string) bool {
	c.mu.Lock()
	cancel, ok := c.listens[id]
	c.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (c *stdioConn) cancelAll() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.listens))
	for _, cancel := range c.listens {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// serveMessagePeek carries the fields the Serve loop routes on.
type serveMessagePeek struct {
	method    string
	hasID     bool
	idString  string
	cancelsID string // notifications/cancelled params.requestId, normalized
	modern    bool   // params._meta declares a stateless revision
}

func peekServeMessage(msg []byte) serveMessagePeek {
	var p struct {
		ID     *protocol.RequestID `json:"id"`
		Method string              `json:"method"`
		Params struct {
			Meta      map[string]json.RawMessage `json:"_meta"`
			RequestID *protocol.RequestID        `json:"requestId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg, &p); err != nil {
		return serveMessagePeek{}
	}
	peek := serveMessagePeek{method: p.Method, hasID: p.ID != nil}
	if p.ID != nil {
		peek.idString = p.ID.String()
	}
	if p.Params.RequestID != nil {
		peek.cancelsID = p.Params.RequestID.String()
	}
	if raw, ok := p.Params.Meta[protocol.MetaProtocolVersion]; ok {
		var v string
		_ = json.Unmarshal(raw, &v)
		peek.modern = protocol.IsStatelessVersion(v)
	}
	return peek
}

// startStdioSubscription registers a subscriptions/listen stream on a stdio
// connection and starts its pump: the acknowledgment is written before any
// notification for this subscription (the pump is that subscription's only
// writer, so per-subscription ordering holds even though other traffic
// interleaves on the shared channel). It returns response bytes only on
// immediate failure; nil means the subscription is live.
func (s *MCPServer) startStdioSubscription(ctx context.Context, t transport.Transport, conn *stdioConn, msg []byte) []byte {
	var req protocol.Request
	if err := json.Unmarshal(msg, &req); err != nil || req.ID == nil {
		resp, _ := marshalResponse(nil, nil, protocol.NewError(protocol.InvalidRequest, "invalid subscriptions/listen request", nil))
		return resp
	}

	var params protocol.ListenParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp, _ := marshalResponse(req.ID, nil, protocol.NewError(protocol.InvalidParams, "invalid subscriptions/listen params", nil))
			return resp
		}
	}

	idJSON, err := json.Marshal(req.ID)
	if err != nil {
		resp, _ := marshalResponse(req.ID, nil, protocol.NewError(protocol.InternalError, "unmarshalable request id", nil))
		return resp
	}

	sub := &serverSubscription{
		id:       idJSON,
		filter:   params.Notifications,
		ch:       make(chan []byte, subscriptionBuffer),
		overflow: make(chan struct{}),
	}

	subCtx, cancel := context.WithCancel(ctx)
	if !conn.add(req.ID.String(), cancel) {
		cancel()
		resp, _ := marshalResponse(req.ID, nil, protocol.NewError(protocol.InvalidRequest, "duplicate subscription id on this connection", nil))
		return resp
	}

	subKey := fmt.Sprintf("sub-%d", s.subSeq.Add(1))
	s.subsMu.Lock()
	s.subscriptions[subKey] = sub
	s.subsMu.Unlock()

	listenID := req.ID.String()
	go func() {
		// cancel is routed through conn.listens for client-initiated ends;
		// self-termination paths (overflow, send failure) must release the
		// child context too or it accumulates on the Serve context across
		// overflow/re-subscribe cycles (round-3 finding 3).
		defer cancel()
		// Single-flight: the overflow path runs cleanup inline BEFORE the
		// cancelled notification, so the listen id is already reusable when
		// the client learns of the gap. Once run, the deferred call is a
		// no-op — the old pump can never remove a successor's registration
		// under the same id.
		var cleanupOnce sync.Once
		cleanup := func() {
			cleanupOnce.Do(func() {
				s.subsMu.Lock()
				delete(s.subscriptions, subKey)
				s.subsMu.Unlock()
				conn.remove(listenID)
			})
		}
		defer cleanup()

		ack, err := marshalNotification(protocol.NotificationSubscriptionAcknowledged, map[string]interface{}{
			"_meta":         map[string]json.RawMessage{protocol.MetaSubscriptionID: idJSON},
			"notifications": params.Notifications,
		})
		if err != nil {
			return
		}
		if err := t.Send(subCtx, ack); err != nil {
			s.logger.Debug("stdio subscription ack send failed", zap.Error(err))
			return
		}
		s.logger.Info("stdio subscription opened", zap.String("subscription_id", listenID))

		for {
			select {
			case <-subCtx.Done():
				// notifications/cancelled, connection teardown, or server
				// shutdown; the stdio binding sends no response on
				// client-initiated cancellation.
				s.logger.Debug("stdio subscription closed", zap.String("subscription_id", listenID))
				return
			case <-sub.overflow:
				// The connection stays up on stdio, so the gap signal is a
				// server-initiated notifications/cancelled for this listen
				// id: the client re-subscribes and refetches. Free the id
				// first — cancelled is the client's cue to re-subscribe, so
				// the id must be reusable before the cue can arrive.
				cleanup()
				cancelled, err := marshalNotification(protocol.NotificationCancelled, map[string]interface{}{
					"_meta":     map[string]json.RawMessage{protocol.MetaSubscriptionID: idJSON},
					"requestId": json.RawMessage(idJSON),
					"reason":    "notification buffer overflowed; re-subscribe and refetch",
				})
				if err == nil {
					_ = t.Send(subCtx, cancelled)
				}
				s.logger.Warn("stdio subscription overflowed; cancelled for client refetch",
					zap.String("subscription_id", listenID))
				return
			case notif := <-sub.ch:
				if err := t.Send(subCtx, notif); err != nil {
					s.logger.Debug("stdio subscription send failed; closing",
						zap.String("subscription_id", listenID), zap.Error(err))
					return
				}
			}
		}
	}()

	return nil
}
