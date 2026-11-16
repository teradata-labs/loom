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
package agent

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// streamFailureRetry retries a streaming LLM call that failed with a
// non-throttle error, under the agent's Retry config (the same budget and
// backoff as the direct path). It retries only while the failed attempt sent
// nothing to the progress callback: after a partial response reached the
// caller, a retry would stream that text or tool call a second time, so the
// error is returned as before.
//
// Throttles never reach it: chatWithStreaming waits them out first, under
// throttlePatience, without using a retry attempt.
type streamFailureRetry struct {
	cfg      RetryConfig
	failures int
	delay    time.Duration
	after    func(time.Duration) <-chan time.Time
}

// newStreamFailureRetry reads the agent's Retry config. An agent with no
// config (a bare test agent) gets the zero RetryConfig: retry off.
func (a *Agent) newStreamFailureRetry() *streamFailureRetry {
	var cfg RetryConfig
	if a.config != nil {
		cfg = a.config.Retry
	}
	return &streamFailureRetry{cfg: cfg, delay: cfg.InitialDelay, after: time.After}
}

// wait decides about one failed streaming attempt. It returns nil after the
// backoff when the call should be made again, or the error that ends the call:
// err itself when the error is final (isFinalLLMError), retry is off, or the
// attempt already emitted progress; a wrapped err once the budget is spent or
// the context ends.
func (r *streamFailureRetry) wait(ctx Context, err error, emitted bool) error {
	if emitted || isFinalLLMError(err) || !r.cfg.Enabled || r.cfg.MaxRetries <= 0 {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("llm stream failed (attempt %d/%d): %w (context cancelled)",
			r.failures+1, r.cfg.MaxRetries+1, err)
	}
	if r.failures >= r.cfg.MaxRetries {
		zap.L().Error("llm stream retries exhausted",
			zap.Int("max_retries", r.cfg.MaxRetries),
			zap.Error(err),
		)
		return fmt.Errorf("llm call failed after %d attempts: %w", r.failures+1, err)
	}
	r.failures++
	zap.L().Warn("llm stream failed before any output, retrying",
		zap.Int("attempt", r.failures),
		zap.Int("max_retries", r.cfg.MaxRetries),
		zap.Duration("delay", r.delay),
		zap.Error(err),
	)
	select {
	case <-ctx.Done():
		return fmt.Errorf("llm stream failed (attempt %d/%d): %w (context cancelled during retry)",
			r.failures, r.cfg.MaxRetries+1, ctx.Err())
	case <-r.after(r.delay):
	}
	r.delay = time.Duration(float64(r.delay) * r.cfg.Multiplier)
	if r.delay > r.cfg.MaxDelay {
		r.delay = r.cfg.MaxDelay
	}
	return nil
}
