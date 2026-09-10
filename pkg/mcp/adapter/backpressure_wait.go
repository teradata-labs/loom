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
)

// Backpressure freeze (issue #354). A 512-agent run measured what happens
// when capacity flow control reaches a model: 27% of the fleet met a full
// session-handle pool, retried connect politely twice, and gave up — locked
// out for the whole run. Flow control is the runtime's job: a tool call that
// fails with the machine-readable backpressure contract
// (client.BackpressureHint) freezes HERE, below the LLM, and re-invokes
// until capacity frees or the conversation's own deadline expires. The model
// just sees a tool call that took longer.
const (
	// defaultBackpressureBudget bounds the total freeze ONLY when the
	// caller's context carries no deadline. When a deadline exists the
	// freeze rides it entirely: a parked call is making progress — it holds
	// a queue position — so nothing shorter than the conversation's own
	// deadline may cut it loose. (az512f measured the alternative: capping
	// the freeze at 15 min under a 40-min deadline surfaced 44 budget-full
	// errors whose slots would have freed minutes later.)
	defaultBackpressureBudget = 15 * time.Minute
	// backpressureDeadlineMargin is reserved from the ctx deadline so the
	// final outcome can still travel back to the loop before ctx death.
	backpressureDeadlineMargin = 2 * time.Second
	// backpressurePollFloor/Ceil bound the sleep between re-invokes when the
	// server names no wait_param. retry_after_s is a worst-case hint —
	// capacity may free sooner — so polls never sleep past the ceiling.
	backpressurePollFloor = time.Second
	backpressurePollCeil  = 30 * time.Second
)

// backpressureBudget is how long a frozen call may wait in total: the
// caller's own deadline (minus a margin for the outcome to travel back)
// when one exists, else the conservative default.
func backpressureBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline) - backpressureDeadlineMargin
	}
	return defaultBackpressureBudget
}

// isBackpressure reports whether a tool-call failure carries the
// machine-readable backpressure contract.
func isBackpressure(err error) bool {
	var terr *client.ToolResultError
	return errors.As(err, &terr) && terr.Backpressure() != nil
}

// awaitBackpressure freezes a tool call that failed with capacity
// backpressure and re-invokes it until it succeeds, fails differently, or
// the budget/context expires — then surfaces the newest error unchanged.
//
// When the server names a wait_param, each re-invoke passes it the remaining
// budget (clamped to the server's max_wait_s), so the retry parks
// SERVER-side in the server's FIFO wait queue and wakes on a freed slot:
// one parked call holds one queue position for the whole wait. Without a
// wait_param the retry polls, sleeping retry_after_s between attempts
// (clamped to the poll bounds).
func (a *MCPToolAdapter) awaitBackpressure(ctx context.Context, params map[string]interface{}, callErr error) (interface{}, error) {
	var terr *client.ToolResultError
	if !errors.As(callErr, &terr) {
		return nil, callErr
	}
	hint := terr.Backpressure()
	if hint == nil {
		return nil, callErr
	}

	budget := backpressureBudget(ctx)
	if budget <= 0 {
		return nil, callErr
	}
	waitDeadline := time.Now().Add(budget)
	start := time.Now()

	a.logger.Info("backpressure: froze tool call until capacity frees",
		zap.String("tool", a.tool.Name),
		zap.String("server", a.serverName),
		zap.String("code", hint.Code),
		zap.String("wait_param", hint.WaitParam),
		zap.Duration("budget", budget))

	// The re-invoke params are a private copy: params aliases the model's
	// toolCall.Input whenever no schema case-restore happened, and injecting
	// wait_s into that map would leak flow control into the model's replayed
	// tool history and the persisted tool-execution record — the exact leak
	// this freeze exists to prevent.
	reinvoke := make(map[string]interface{}, len(params)+1)
	for k, v := range params {
		reinvoke[k] = v
	}

	attempts := 0
	lastErr := callErr
	for {
		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			a.logger.Warn("backpressure: budget exhausted; surfacing the error",
				zap.String("tool", a.tool.Name),
				zap.String("code", hint.Code),
				zap.Int("attempts", attempts),
				zap.Duration("waited", time.Since(start)))
			return nil, lastErr
		}

		if hint.WaitParam != "" {
			// Server-side park: ask the server to hold this call until a
			// slot frees, for as much of the remaining budget as it allows.
			waitS := int64(remaining / time.Second)
			if hint.MaxWaitS > 0 && waitS > hint.MaxWaitS {
				waitS = hint.MaxWaitS
			}
			if waitS < 1 {
				waitS = 1
			}
			reinvoke[hint.WaitParam] = waitS
		} else {
			sleep := time.Duration(hint.RetryAfterS) * time.Second
			if sleep < backpressurePollFloor {
				sleep = backpressurePollFloor
			}
			if sleep > backpressurePollCeil {
				sleep = backpressurePollCeil
			}
			if sleep > remaining {
				sleep = remaining
			}
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, lastErr
			case <-timer.C:
			}
		}

		attempts++
		attemptStart := time.Now()
		result, err := a.client.CallTool(ctx, a.tool.Name, reinvoke)
		if err == nil {
			a.logger.Info("backpressure: call succeeded after freeze",
				zap.String("tool", a.tool.Name),
				zap.String("code", hint.Code),
				zap.Int("attempts", attempts),
				zap.Duration("waited", time.Since(start)))
			return result, nil
		}
		lastErr = err

		var again *client.ToolResultError
		if !errors.As(err, &again) {
			// Transport/context failure: nothing to wait out.
			return nil, err
		}
		next := again.Backpressure()
		if next == nil {
			// The condition changed to a task-level failure: the model must
			// see it.
			return nil, err
		}
		if hint.WaitParam != "" && time.Since(attemptStart) < backpressurePollFloor {
			// The server advertised parking but refused instantly instead of
			// holding the call: degrade to polling pace — honoring the
			// server's own retry_after estimate when it exceeds the floor —
			// so the loop can't spin hot against a server that doesn't
			// honor its own contract.
			sleep := backpressurePollFloor
			if ra := time.Duration(next.RetryAfterS) * time.Second; ra > sleep {
				sleep = ra
			}
			if remaining := time.Until(waitDeadline); sleep > remaining {
				sleep = remaining
			}
			if sleep > 0 {
				timer := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, err
				case <-timer.C:
				}
			}
		}
		if hint.WaitParam != "" && hint.WaitParam != next.WaitParam {
			// The contract changed: drop the previously injected wait key so
			// later polling re-invokes don't carry a stale server-side park
			// request on top of the client-side pacing.
			delete(reinvoke, hint.WaitParam)
		}
		hint = next // refresh retry_after / wait caps from the newest error
	}
}
