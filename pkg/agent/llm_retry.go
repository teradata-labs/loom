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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// chatWithRetry wraps LLM Chat calls with exponential backoff retry logic.
// If the provider supports streaming and a progress callback is configured,
// it will use streaming with token buffering to emit real-time progress.
func (a *Agent) chatWithRetry(ctx Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	// Debug tap: one context dump per provider call, before any dispatch branch.
	// No-op unless the dump switch is on. Covers streaming, no-retry, and the
	// retry loop alike since every path fans out from here.
	a.dumpContext(ctx, messages, tools)

	// Check if provider supports streaming and we have a progress callback
	supportsStreaming := llmtypes.SupportsStreaming(a.llm)
	progressCallback := ctx.ProgressCallback()
	useStreaming := supportsStreaming && progressCallback != nil

	// If using streaming, bypass retry logic (streaming already handles errors)
	if useStreaming {
		return a.chatWithStreaming(ctx, messages, tools, progressCallback)
	}

	// Non-streaming path with retry logic
	// If retry is disabled, call LLM directly — but throttling still pauses
	// rather than failing: disabling retry opts out of retrying FAILURES,
	// not out of honoring provider flow control.
	if !a.config.Retry.Enabled || a.config.Retry.MaxRetries == 0 {
		var patience throttlePatience
		for {
			resp, err := a.llm.Chat(ctx, messages, tools)
			if err == nil || !llm.IsThrottlingError(err) {
				return resp, err
			}
			if waitErr := patience.wait(ctx, err); waitErr != nil {
				return nil, waitErr
			}
		}
	}

	var lastErr error
	var patience throttlePatience
	delay := a.config.Retry.InitialDelay

	for attempt := 0; attempt <= a.config.Retry.MaxRetries; attempt++ {
		// Attempt LLM call
		response, err := a.llm.Chat(ctx, messages, tools)
		if err == nil {
			// Success! Log if we had previous failures
			if attempt > 0 {
				zap.L().Info("llm retry succeeded",
					zap.Int("attempt", attempt+1),
					zap.Duration("total_retry_time", delay),
				)
			}
			return response, nil
		}

		// A positively-identified context-too-long refusal is returned
		// immediately, never retried (blueprint A5): relief, not retry, is the
		// only response to it.
		if errors.Is(err, llm.ErrContextTooLong) {
			return nil, err
		}

		lastErr = err

		// Don't retry on context cancellation or deadline exceeded
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm call failed (attempt %d/%d): %w (context cancelled)",
				attempt+1, a.config.Retry.MaxRetries+1, err)
		}

		// Throttling is flow control, not failure: it never consumes a retry
		// attempt. Wait out the provider-stated window (or a patient default)
		// and go again, bounded by the patience budget and the conversation's
		// own deadline.
		if llm.IsThrottlingError(err) {
			if waitErr := patience.wait(ctx, err); waitErr != nil {
				return nil, waitErr
			}
			attempt--
			continue
		}

		// If this is the last attempt, don't sleep
		if attempt >= a.config.Retry.MaxRetries {
			break
		}

		// Log retry attempt
		zap.L().Warn("llm call failed, retrying",
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", a.config.Retry.MaxRetries),
			zap.Duration("delay", delay),
			zap.Error(err),
		)

		// Sleep with exponential backoff
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("llm call failed (attempt %d/%d): %w (context cancelled during retry)",
				attempt+1, a.config.Retry.MaxRetries+1, ctx.Err())
		case <-time.After(delay):
			// Continue to next retry
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * a.config.Retry.Multiplier)
		if delay > a.config.Retry.MaxDelay {
			delay = a.config.Retry.MaxDelay
		}
	}

	// All retries exhausted
	zap.L().Error("llm retries exhausted",
		zap.Int("max_retries", a.config.Retry.MaxRetries),
		zap.Error(lastErr),
	)

	return nil, fmt.Errorf("llm call failed after %d attempts: %w",
		a.config.Retry.MaxRetries+1, lastErr)
}

// throttlePatience paces re-invocation against provider throttling (429 and
// kin). Waits honor the provider-stated retry-after when the error names one,
// escalate 2s→60s otherwise, and give up only past a total budget — a
// throttled conversation pauses, it does not die.
type throttlePatience struct {
	delay time.Duration
	total time.Duration
}

const (
	throttleInitialDelay = 2 * time.Second
	throttleMaxDelay     = 60 * time.Second
	// throttleBudget bounds total throttle waiting per LLM call so a caller
	// without a deadline cannot park forever on a hard-down provider.
	throttleBudget = 10 * time.Minute
)

func (p *throttlePatience) wait(ctx Context, err error) error {
	if p.delay == 0 {
		p.delay = throttleInitialDelay
	}
	wait := p.delay
	if hint := llm.RetryAfterHint(err); hint > wait {
		wait = hint
	}
	if p.total+wait > throttleBudget {
		return fmt.Errorf("llm throttled beyond %s patience budget: %w", throttleBudget, err)
	}
	zap.L().Warn("llm throttled; conversation paused, not failed",
		zap.Duration("wait", wait),
		zap.Duration("throttled_so_far", p.total),
		zap.Error(err),
	)
	select {
	case <-ctx.Done():
		return fmt.Errorf("llm throttled and context ended during wait: %w", ctx.Err())
	case <-time.After(wait):
	}
	p.total += wait
	p.delay *= 2
	if p.delay > throttleMaxDelay {
		p.delay = throttleMaxDelay
	}
	return nil
}

// chatWithStreaming uses streaming API with token buffering and progress emission.
func (a *Agent) chatWithStreaming(ctx Context, messages []Message, tools []shuttle.Tool, progressCallback ProgressCallback) (*LLMResponse, error) {
	streamingProvider, ok := a.llm.(llmtypes.StreamingLLMProvider)
	if !ok {
		// Fallback to non-streaming (should never happen due to check in chatWithRetry)
		return a.llm.Chat(ctx, messages, tools)
	}

	// Token buffering state
	var buffer strings.Builder
	var tokenCount int32
	var ttft int64
	ttftRecorded := false
	startTime := time.Now()
	lastFlushTime := startTime

	const (
		flushInterval   = 50 * time.Millisecond // Flush every 50ms
		flushTokenCount = 20                    // OR flush every 20 tokens
	)

	// Create token callback with buffering logic
	tokenCallback := func(token string) {
		// Record TTFT on first token
		if !ttftRecorded {
			ttft = time.Since(startTime).Milliseconds()
			ttftRecorded = true
		}

		// Add token to buffer
		buffer.WriteString(token)
		tokenCount++

		now := time.Now()
		timeSinceFlush := now.Sub(lastFlushTime)

		// Flush if we've accumulated enough tokens OR enough time has passed
		shouldFlush := tokenCount >= flushTokenCount || timeSinceFlush >= flushInterval

		if shouldFlush {
			// Check context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Emit progress event with partial content
			progressCallback(ProgressEvent{
				Stage:          StageLLMGeneration,
				Progress:       50, // Mid-generation
				Message:        "Streaming LLM response...",
				Timestamp:      now,
				PartialContent: buffer.String(),
				IsTokenStream:  true,
				TokenCount:     tokenCount,
				TTFT:           ttft,
			})

			lastFlushTime = now
		}
	}

	// Call streaming provider. Throttling pauses and re-invokes — the
	// previous behavior ("streaming already handles errors") returned the
	// first 429 straight up the stack and killed the conversation. Buffered
	// state resets per attempt so a retried stream never duplicates tokens.
	var patience throttlePatience
	var resp *LLMResponse
	for {
		buffer.Reset()
		tokenCount = 0
		ttft = 0
		ttftRecorded = false

		var err error
		resp, err = streamingProvider.ChatStream(ctx, messages, tools, tokenCallback)
		if err == nil {
			break
		}
		if llm.IsThrottlingError(err) {
			if waitErr := patience.wait(ctx, err); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		return nil, err
	}

	// Emit final progress event with complete content
	if progressCallback != nil {
		progressCallback(ProgressEvent{
			Stage:          StageLLMGeneration,
			Progress:       100,
			Message:        "LLM response complete",
			Timestamp:      time.Now(),
			PartialContent: resp.Content,
			IsTokenStream:  false, // Final event
			TokenCount:     tokenCount,
			TTFT:           ttft,
		})
	}

	return resp, nil
}
