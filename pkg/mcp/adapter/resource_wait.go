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
			zap.String("uri", uri), zap.Error(serr))
		return nil, callErr
	}

	a.logger.Info("resource-wait: parked failed tool call on linked resource",
		zap.String("tool", a.tool.Name),
		zap.String("uri", uri),
		zap.Duration("budget", budget))

	lastErr := callErr
	timer := time.NewTimer(time.Until(waitDeadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-timer.C:
			return nil, lastErr
		case <-sub.Done():
			// Stream ended (server closed it or no subscription support at
			// runtime): nothing will wake us — surface the error.
			return nil, lastErr
		case notif, ok := <-sub.Notifications:
			if !ok {
				return nil, lastErr
			}
			if notif.Method != protocol.NotificationResourceUpdated {
				continue // the acknowledgment, or an unrelated method
			}
			result, err := a.client.CallTool(ctx, a.tool.Name, params)
			if err == nil {
				a.logger.Info("resource-wait: retry succeeded after resource update",
					zap.String("tool", a.tool.Name), zap.String("uri", uri))
				return result, nil
			}
			lastErr = err
			// Still failing with the same linked condition: keep waiting out
			// the budget. A different failure surfaces immediately.
			var again *client.ToolResultError
			if !errors.As(err, &again) || again.RetryResourceURI() != uri {
				return nil, err
			}
		}
	}
}
