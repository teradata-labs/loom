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
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// defaultResourceWaitBudget bounds how long a failed tool call may stay
// parked waiting for its linked resource to update before the error is
// surfaced. Also capped by the caller's context deadline.
const defaultResourceWaitBudget = 60 * time.Second

// awaitLinkedResource implements park-and-wake (issue #343): when a tool-level
// failure links a resource in its error content — the spec-native way for a
// server to say "this resource's next update may clear the failure" (e.g. a
// session-handle budget that frees a slot) — the call parks on a
// subscriptions/listen stream for that URI and retries when
// notifications/resources/updated arrives. The agent sees one tool call that
// succeeds late instead of burning a turn per retry.
//
// Two acknowledgment-driven behaviors close the protocol's gaps:
//
//   - Missed-wake window: an update landing between the failed call and the
//     subscription's registration is never delivered. The server's
//     acknowledgment proves registration, so ONE optimistic retry fires as
//     soon as the ack arrives — an update that raced the subscribe is covered
//     by that attempt. Notification-driven retries continue afterwards.
//
//   - Honored subset: the acknowledgment echoes the filter subset the server
//     agreed to honor. If our URI's resource subscription was not honored,
//     no wake will ever come — fail fast to the original error instead of
//     stalling out the full budget.
//
// Returns the retried call's outcome. Anything that prevents waiting — no
// linked resource, a legacy (non-2026-07-28) connection, subscription
// failure — returns the original error unchanged, so behavior degrades to
// exactly what it was before this mechanism existed.
func (a *MCPToolAdapter) awaitLinkedResource(ctx context.Context, params map[string]interface{}, callErr error) (interface{}, error) {
	var terr *client.ToolResultError
	if !errors.As(callErr, &terr) {
		return nil, callErr
	}
	uri := terr.RetryResourceURI()
	if uri == "" || !a.client.IsStateless() {
		return nil, callErr
	}

	budget := defaultResourceWaitBudget
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) - time.Second; remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return nil, callErr
	}
	waitDeadline := time.Now().Add(budget)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, serr := a.client.Subscribe(subCtx, protocol.NotificationFilter{
		ResourceSubscriptions: []string{uri},
	})
	if serr != nil {
		a.logger.Debug("resource-wait: subscribe failed; surfacing original error",
			zap.String("server", a.serverName),
			zap.String("tool", a.tool.Name),
			zap.String("uri", uri), zap.Error(serr))
		return nil, callErr
	}

	a.logger.Info("resource-wait: parked failed tool call on linked resource",
		zap.String("server", a.serverName),
		zap.String("tool", a.tool.Name),
		zap.String("uri", uri),
		zap.Duration("budget", budget))

	parkExit := func(reason string) {
		a.logger.Info("resource-wait: park ended without success; surfacing error",
			zap.String("server", a.serverName),
			zap.String("tool", a.tool.Name),
			zap.String("uri", uri),
			zap.String("reason", reason))
	}

	// retryOnce re-issues the parked call. final=true means the outcome is
	// terminal: success, or a failure that is not the same linked condition.
	// A repeat of the same linked failure keeps the park alive within budget.
	retryOnce := func(trigger string) (interface{}, error, bool) {
		result, err := a.client.CallTool(ctx, a.tool.Name, params)
		if err == nil {
			a.logger.Info("resource-wait: retry succeeded",
				zap.String("server", a.serverName),
				zap.String("tool", a.tool.Name),
				zap.String("uri", uri),
				zap.String("trigger", trigger))
			return result, nil, true
		}
		var again *client.ToolResultError
		if !errors.As(err, &again) || again.RetryResourceURI() != uri {
			parkExit("retry failed with a different error")
			return nil, err, true
		}
		return nil, err, false
	}

	lastErr := callErr
	timer := time.NewTimer(time.Until(waitDeadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			parkExit("context cancelled")
			return nil, lastErr
		case <-timer.C:
			parkExit("budget exhausted")
			return nil, lastErr
		case <-sub.Done():
			// Stream ended (server closed it or no subscription support at
			// runtime): nothing will wake us — surface the error.
			parkExit("subscription stream ended")
			return nil, lastErr
		case notif, ok := <-sub.Notifications:
			if !ok {
				parkExit("subscription channel closed")
				return nil, lastErr
			}
			switch notif.Method {
			case protocol.NotificationSubscriptionAcknowledged:
				if !ackHonorsResource(notif.Params, uri) {
					// The server declined our resource subscription: no
					// update will ever arrive, so waiting is a guaranteed
					// full-budget stall. Fail fast to the original error.
					parkExit("server declined the resource subscription")
					return nil, lastErr
				}
				// The ack proves the subscription is registered; an update
				// that landed before registration was lost. One optimistic
				// retry covers that window.
				result, err, final := retryOnce("post-ack")
				if final {
					return result, err
				}
				lastErr = err
			case protocol.NotificationResourceUpdated:
				result, err, final := retryOnce("resource-updated")
				if final {
					return result, err
				}
				lastErr = err
			default:
				continue // an unrelated method on this stream
			}
		}
	}
}

// ackHonorsResource parses a subscriptions/listen acknowledgment's echoed
// honored subset and reports whether the resource subscription for uri was
// honored. A missing or unparseable subset counts as not honored: the
// 2026-07-28 revision requires the acknowledgment to echo what the server
// agreed to, so anything else gives no basis to expect a wake.
func ackHonorsResource(params json.RawMessage, uri string) bool {
	var ack struct {
		Notifications protocol.NotificationFilter `json:"notifications"`
	}
	if err := json.Unmarshal(params, &ack); err != nil {
		return false
	}
	for _, honored := range ack.Notifications.ResourceSubscriptions {
		if honored == uri {
			return true
		}
	}
	return false
}
