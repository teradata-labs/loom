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
// This file keeps managed stateless clients' tool caches fresh via
// subscriptions/listen (2026-07-28): subscribe to toolsListChanged, refetch
// on every (re)open — the reconnect-gap reconciliation the revision
// prescribes in place of resumption — and refetch on each change event.
package manager

import (
	"context"
	"errors"
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
)

const (
	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

// watchToolLists maintains one subscriptions/listen loop for a stateless
// client. It exits when stopCh closes (manager stop), the client closes, or
// the server answers MethodNotFound (subscriptions unsupported). stopCh is
// passed explicitly so a manager restart cannot race the field.
func (m *Manager) watchToolLists(name string, cl *client.Client, stopCh <-chan struct{}) {
	defer m.watchWG.Done()

	logger := m.logger.With(zap.String("server", name))
	backoff := watchBackoffMin

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcherDone := make(chan struct{})
	defer close(watcherDone)
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-watcherDone: // watcher exited on its own; don't leak
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Refetch first: notifications missed while disconnected are
		// reconciled by refreshing the list, not replayed.
		if _, err := cl.ListTools(ctx); err != nil {
			logger.Debug("tool refetch failed; retrying after backoff", zap.Error(err))
			if !sleepOrDone(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		sub, err := cl.Subscribe(ctx, protocol.NotificationFilter{ToolsListChanged: true})
		if err != nil {
			var rpcErr *protocol.Error
			if errors.As(err, &rpcErr) && rpcErr.Code == protocol.MethodNotFound {
				logger.Debug("server does not implement subscriptions/listen; tool-list watching disabled")
				return
			}
			logger.Warn("subscriptions/listen failed; retrying after backoff", zap.Error(err))
			if !sleepOrDone(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = watchBackoffMin
		logger.Info("watching tool-list changes", zap.String("subscription_id", sub.ID))

		m.consumeSubscription(ctx, logger, cl, sub)

		if err := sub.Err(); err != nil {
			// MethodNotFound arrives asynchronously (the listen request's
			// JSON-RPC error response ends the subscription), so a server
			// without subscriptions support is detected here, not at
			// Subscribe time.
			var rpcErr *protocol.Error
			if errors.As(err, &rpcErr) && rpcErr.Code == protocol.MethodNotFound {
				logger.Debug("server does not implement subscriptions/listen; tool-list watching disabled")
				return
			}
			logger.Warn("tool-list subscription ended; reconnecting", zap.Error(err))
		} else {
			logger.Info("tool-list subscription closed by server; reconnecting")
		}
		if !sleepOrDone(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// consumeSubscription refreshes the client tool cache on every change event
// until the subscription ends or ctx is cancelled.
func (m *Manager) consumeSubscription(ctx context.Context, logger *zap.Logger, cl *client.Client, sub *client.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case notif, ok := <-sub.Notifications:
			if !ok {
				return
			}
			switch notif.Method {
			case protocol.NotificationSubscriptionAcknowledged:
				logger.Debug("subscription acknowledged")
			case protocol.NotificationToolsListChanged:
				if _, err := cl.ListTools(ctx); err != nil {
					logger.Warn("tool refetch after list-change failed", zap.Error(err))
				} else {
					logger.Info("tool list refreshed after change notification")
				}
			default:
				logger.Debug("ignoring unsubscribed notification", zap.String("method", notif.Method))
			}
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > watchBackoffMax {
		return watchBackoffMax
	}
	return next
}
