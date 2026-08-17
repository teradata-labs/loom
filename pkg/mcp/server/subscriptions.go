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
// This file implements the server side of subscriptions/listen (2026-07-28):
// a long-lived POST-response stream carrying opted-in change notifications,
// each tagged with io.modelcontextprotocol/subscriptionId. It is the first
// HTTP delivery path server-initiated notifications have ever had — the old
// notifyCh only drained on the stdio Serve loop.
//
// Transport scope: full support rides the streaming path (Streamable HTTP).
// On the synchronous path (stdio HandleMessage) the method is not registered
// and answers MethodNotFound, which clients treat as "subscriptions
// unsupported" — the manager's watch loop exits cleanly on it.
package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// subscriptionBuffer bounds per-subscriber pending notifications. Overflow
// drops the notification (with a warning): recovery is refetch-on-reconnect,
// and ttlMs freshness hints make refetching cheap — redelivery machinery
// would be rebuilding the resumption the revision deleted.
const subscriptionBuffer = 64

// serverSubscription is one active listen stream's registration.
type serverSubscription struct {
	id     json.RawMessage // the listen request's JSON-RPC id, echoed in _meta
	filter protocol.NotificationFilter
	ch     chan []byte
}

// wantsNotification reports whether the subscription's filter opted into the
// given notification method (and URI, for resource updates).
func (sub *serverSubscription) wantsNotification(method, uri string) bool {
	switch method {
	case protocol.NotificationToolsListChanged:
		return sub.filter.ToolsListChanged
	case protocol.NotificationPromptsListChanged:
		return sub.filter.PromptsListChanged
	case protocol.NotificationResourcesListChanged:
		return sub.filter.ResourcesListChanged
	case protocol.NotificationResourceUpdated:
		for _, u := range sub.filter.ResourceSubscriptions {
			if u == uri {
				return true
			}
		}
	}
	return false
}

// handleSubscriptionsListenStream serves one subscriptions/listen request on
// the streaming path: acknowledge first (the spec forbids any notification
// before the ack), then forward matching notifications until the client
// closes the stream (transport-level cancellation under 2026-07-28).
func (s *MCPServer) handleSubscriptionsListenStream(ctx context.Context, req *protocol.Request, w transport.SSEWriter) ([]byte, error) {
	var params protocol.ListenParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return marshalResponse(req.ID, nil, protocol.NewError(protocol.InvalidParams, "invalid subscriptions/listen params", nil))
		}
	}

	idJSON, err := json.Marshal(req.ID)
	if err != nil {
		return marshalResponse(req.ID, nil, protocol.NewError(protocol.InternalError, "unmarshalable request id", nil))
	}

	sub := &serverSubscription{
		id:     idJSON,
		filter: params.Notifications, // every requested type is honored
		ch:     make(chan []byte, subscriptionBuffer),
	}

	subKey := string(idJSON)
	s.subsMu.Lock()
	if _, exists := s.subscriptions[subKey]; exists {
		s.subsMu.Unlock()
		return marshalResponse(req.ID, nil, protocol.NewError(protocol.InvalidRequest, "duplicate subscription id", nil))
	}
	s.subscriptions[subKey] = sub
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subscriptions, subKey)
		s.subsMu.Unlock()
	}()

	ack, err := marshalNotification(protocol.NotificationSubscriptionAcknowledged, map[string]interface{}{
		"_meta":         map[string]json.RawMessage{protocol.MetaSubscriptionID: idJSON},
		"notifications": params.Notifications,
	})
	if err != nil {
		return nil, err
	}
	if err := w.WriteEvent(ack); err != nil {
		return nil, fmt.Errorf("failed to send subscription acknowledgment: %w", err)
	}

	s.logger.Info("subscription opened", zap.String("subscription_id", subKey))

	for {
		select {
		case <-ctx.Done():
			// Client closed the stream — the 2026-07-28 cancellation signal.
			// No final response: a graceful server-side closure would carry
			// one, but the peer is already gone.
			s.logger.Debug("subscription closed by client", zap.String("subscription_id", subKey))
			return nil, nil
		case notif := <-sub.ch:
			if err := w.WriteEvent(notif); err != nil {
				s.logger.Debug("subscription stream write failed; closing",
					zap.String("subscription_id", subKey), zap.Error(err))
				return nil, nil
			}
		}
	}
}

// publishNotification fans one server-initiated notification out to every
// subscription that opted into it, tagging each copy with the subscription's
// id, and best-effort enqueues an untagged copy for the legacy stdio Serve
// loop. A publish with no interested subscribers is a silent no-op — the old
// notifyCh-only path warned on every publish once its 16-slot buffer filled,
// because HTTP deployments had nothing draining it.
func (s *MCPServer) publishNotification(method, uri string, extraParams map[string]interface{}) {
	s.subsMu.RLock()
	for _, sub := range s.subscriptions {
		if !sub.wantsNotification(method, uri) {
			continue
		}
		params := map[string]interface{}{
			"_meta": map[string]json.RawMessage{protocol.MetaSubscriptionID: sub.id},
		}
		for k, v := range extraParams {
			params[k] = v
		}
		notif, err := marshalNotification(method, params)
		if err != nil {
			continue
		}
		select {
		case sub.ch <- notif:
		default:
			s.logger.Warn("subscription notification dropped: consumer too slow",
				zap.String("subscription_id", string(sub.id)), zap.String("method", method))
		}
	}
	s.subsMu.RUnlock()

	// Legacy stdio delivery: Serve() drains notifyCh. On HTTP nothing does,
	// so overflow is expected and logged at Debug, not Warn.
	var legacyParams interface{}
	if len(extraParams) > 0 {
		legacyParams = extraParams
	}
	if notif, err := marshalNotification(method, legacyParams); err == nil {
		select {
		case s.notifyCh <- notif:
		default:
			s.logger.Debug("legacy notification channel full (no stdio drain); dropping",
				zap.String("method", method))
		}
	}
}

// NotifyToolsListChanged announces that tools/list output changed (e.g. a
// TER-263 lazy skill load landed).
func (s *MCPServer) NotifyToolsListChanged() {
	s.publishNotification(protocol.NotificationToolsListChanged, "", nil)
}

// NotifyPromptsListChanged announces that prompts/list output changed.
func (s *MCPServer) NotifyPromptsListChanged() {
	s.publishNotification(protocol.NotificationPromptsListChanged, "", nil)
}

// NotifyResourceUpdated announces a change to one resource for subscribers
// watching its URI.
func (s *MCPServer) NotifyResourceUpdated(uri string) {
	s.publishNotification(protocol.NotificationResourceUpdated, uri, map[string]interface{}{"uri": uri})
}
