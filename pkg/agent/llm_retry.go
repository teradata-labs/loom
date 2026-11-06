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
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// Persisted roles for agent-authored content that reaches the LLM as a user
// turn. Values mirror internal/message (a TUI-domain package pkg/agent must
// not import); producers and IsSyntheticWireUserRole share these so a rename
// cannot silently stop a role folding to "user" on the wire.
const (
	roleSkillBody          = "skill_body"
	roleHygieneInjection   = "hygiene_injection"
	roleEmptyResponseRetry = "empty_response_retry"
	roleSynthesisPrompt    = "synthesis_prompt"
)

// IsSyntheticWireUserRole reports whether role is a persisted Loom-internal
// role that must be delivered to the LLM as user-turn content — the
// skill-body sidecar, a hygiene retry injection, an empty-response retry
// nudge, or a synthesis prompt — as opposed to the literal end user.
// Exported so pkg/server's live-streaming gate (isNewMessageUpdateRole)
// shares this definition instead of maintaining the same string set
// independently.
func IsSyntheticWireUserRole(role string) bool {
	switch role {
	case roleSkillBody, roleHygieneInjection, roleEmptyResponseRetry, roleSynthesisPrompt:
		return true
	default:
		return false
	}
}

// normalizeWireRoles returns a copy of messages where synthetic
// Loom-internal roles read "user". The persisted and in-memory session
// messages are never mutated; only this local copy, built immediately
// before the provider call, changes. Returns the input slice unchanged
// (no allocation) when nothing needs folding — the common case, since most
// turns carry no skill-body or hygiene-injection rows at all.
func normalizeWireRoles(messages []Message) []Message {
	needsFold := false
	for _, m := range messages {
		if IsSyntheticWireUserRole(m.Role) {
			needsFold = true
			break
		}
	}
	if !needsFold {
		return messages
	}

	out := make([]Message, len(messages))
	for i, m := range messages {
		if IsSyntheticWireUserRole(m.Role) {
			m.Role = "user"
		}
		out[i] = m
	}
	return out
}

// chatWithRetry wraps LLM Chat calls with slot scheduling, capacity
// observation, and exponential backoff retry logic. If the provider supports
// streaming and a progress callback is configured, it will use streaming
// with token buffering to emit real-time progress.
func (a *Agent) chatWithRetry(ctx Context, messages []Message, tools []shuttle.Tool) (resp *LLMResponse, err error) {
	// Fold synthetic Loom-internal roles to the wire "user" role before
	// anything else sees this slice — including the debug dump below, which
	// must capture the true wire-bound view, not the persisted role.
	messages = normalizeWireRoles(messages)

	// Debug tap: one context dump per provider call, before any dispatch branch.
	// No-op unless the dump switch is on. Covers streaming, no-retry, and the
	// retry loop alike since every path fans out from here.
	a.dumpContext(ctx, messages, tools)

	// Slot scheduling: every LLM call — streaming, retry, and direct alike —
	// acquires a capacity slot here. Waiting for a slot can only end with a
	// grant or the caller's own context expiring; it is never a scheduler
	// timeout (docs/architecture/llm-slot-scheduler.md). A nil grant means
	// scheduling is disabled or the turn carries no SlotInfo.
	grant, aerr := scheduler.AcquireForCall(ctx, a.schedulerScope(), a.estimateReservation(messages))
	if aerr != nil {
		return nil, aerr
	}
	if grant != nil {
		// Registered before the observation defer so it runs AFTER it:
		// the outcome must be observed (throttle → ceiling halved, wake
		// armed) before the released reservation re-dispatches waiters.
		defer func() {
			var actual int64
			if resp != nil {
				// Provider-metered usage, which for OpenAI-compatible and
				// Gemini scopes includes cached prompt tokens (see
				// types.Usage.RateLimitTokens) — not the cache-exclusive
				// TotalTokens.
				actual = int64(resp.Usage.ThroughputTokens())
			}
			grant.Release(actual)
		}()
	}
	// Provider-agnostic capacity observation: every provider's calls pass
	// through this funnel, so this one seam calibrates all scopes — with or
	// without a grant, because an unscheduled call's 429 depletes the same
	// shared quota (see observeSchedulerOutcome).
	defer func() { a.observeSchedulerOutcome(err) }()

	return a.dispatchChat(ctx, messages, tools)
}

// observeSchedulerOutcome feeds the slot scheduler's provider-agnostic AIMD
// seam from one call's outcome: a clean completion grows the scope's ceiling
// (until header calibration outranks it), a surfaced throttle — a typed
// llm.ThrottleError from any HTTP client, or an SDK throttling message
// (Bedrock) — halves it, at most once per congestion event. Anthropic,
// Bedrock, OpenAI, Gemini, and Ollama scopes all calibrate through this with
// zero per-provider wiring; Azure's response-header calibration stays in its
// client and outranks these observations.
func (a *Agent) observeSchedulerOutcome(err error) {
	if !scheduler.Enabled() {
		return
	}
	if err == nil {
		scheduler.ObserveSuccessForScope(a.schedulerScope())
		return
	}
	if llm.IsThrottle(err) {
		scheduler.ObserveThrottleForScope(a.schedulerScope(), llm.RetryAfter(err))
	}
}

// providerCtx returns the context for one provider call, suppressing extended
// thinking when this history cannot be sent with it on.
//
// Thinking blocks are turn-scoped replay material and are never persisted, so
// a turn rebuilt from the session store — a HITL park resumed mid tool loop —
// replays its assistant tool_use rows without the blocks that accompanied
// them. Anthropic rejects that pairing, which would kill the turn on resume.
//
// Both conditions are required. A resumed turn whose rows still carry their
// blocks (the park never left the process) needs no suppression, and a missing
// block outside a resume is not this problem — compile strips blocks from
// SETTLED turns by design, and a model may simply have returned none.
func (a *Agent) providerCtx(ctx Context, messages []Message) context.Context {
	if isResumedTurn(ctx) && rebuiltToolTurn(messages) {
		return llm.ContextSuppressThinking(ctx)
	}
	return ctx
}

// rebuiltToolTurn reports whether the newest turn present holds an assistant
// message with tool calls but no thinking blocks — the shape a store rebuild
// produces. Only the newest turn is examined: settled turns render without
// their blocks on purpose.
func rebuiltToolTurn(messages []Message) bool {
	var newest int64
	for i := range messages {
		if messages[i].Turn > newest {
			newest = messages[i].Turn
		}
	}
	for i := range messages {
		m := &messages[i]
		if m.Turn != newest || m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		if len(m.ThinkingBlocks) == 0 {
			return true
		}
	}
	return false
}

// dispatchChat routes one LLM call to streaming, direct, or the retry loop.
func (a *Agent) dispatchChat(ctx Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	// Check if provider supports streaming and we have a progress callback
	supportsStreaming := llmtypes.SupportsStreaming(a.llm)
	progressCallback := ctx.ProgressCallback()
	useStreaming := supportsStreaming && progressCallback != nil

	// Streaming skips the failure-retry loop below: chatWithStreaming returns
	// any non-throttle error at once, and waits out throttles itself.
	if useStreaming {
		return a.chatWithStreaming(ctx, messages, tools, progressCallback)
	}

	// Non-streaming path with retry logic
	// If retry is disabled, call LLM directly — but throttling still pauses
	// rather than failing: disabling retry opts out of retrying FAILURES,
	// not out of honoring provider flow control.
	if !a.config.Retry.Enabled || a.config.Retry.MaxRetries == 0 {
		patience := a.newThrottlePatience()
		for {
			patience.beginAttempt()
			resp, err := a.llm.Chat(a.providerCtx(ctx, messages), messages, tools)
			if err == nil || !isRetryableThrottle(err) {
				return resp, err
			}
			if waitErr := a.absorbThrottle(ctx, &patience, err); waitErr != nil {
				return nil, waitErr
			}
		}
	}

	var lastErr error
	patience := a.newThrottlePatience()
	delay := a.config.Retry.InitialDelay

	for attempt := 0; attempt <= a.config.Retry.MaxRetries; attempt++ {
		// Attempt LLM call
		patience.beginAttempt()
		response, err := a.llm.Chat(a.providerCtx(ctx, messages), messages, tools)
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

		// Throttling is flow control, not failure: it never consumes a retry
		// attempt. Wait out the provider-stated window (or a patient default)
		// and go again, bounded by the wall-clock patience budget and the
		// conversation's own deadline. If the context has already ended, the
		// wait returns at once with both errors.
		//
		// This check comes FIRST, before any check that makes an error final,
		// so the rule is the same as on the retry-disabled and streaming
		// paths: an error that is a throttle through any wrapping (a provider
		// RateLimiter that ran out of its own retries wraps the last 429) is
		// waited out under the budget; anything else is not. A
		// context-too-long refusal is never a throttle (isRetryableThrottle),
		// so it falls through to the ErrContextTooLong check below.
		if isRetryableThrottle(err) {
			if waitErr := a.absorbThrottle(ctx, &patience, err); waitErr != nil {
				return nil, waitErr
			}
			attempt--
			continue
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
// escalate 2s→60s otherwise, and give up only past a wall-clock budget — a
// throttled conversation pauses, it does not die.
//
// The budget is WALL-CLOCK time since the start of the first provider
// invocation of this LLM call that returned a throttle. It covers the
// patience waits AND the time spent inside each re-invoked call, which
// includes a provider RateLimiter's own retries and backoff. Before each wait
// the agent checks elapsed + next wait against the budget, so the last
// re-invoked call can still run after the budget is reached: the worst case
// is the budget plus one invocation.
//
// The same rule holds on all three paths (retry-enabled non-streaming,
// streaming, retry-disabled): an error that is a throttle through any
// wrapping is waited out under the budget; any other error is not.
type throttlePatience struct {
	clock throttleClock
	delay time.Duration
	// attemptStart is when the current provider invocation began.
	attemptStart time.Time
	// start is the attemptStart of the first invocation that returned a
	// throttle; zero until then. The budget is measured from it.
	start time.Time
}

const (
	throttleInitialDelay = 2 * time.Second
	throttleMaxDelay     = 60 * time.Second
	// throttleBudget bounds the wall-clock time one LLM call can spend
	// throttled — patience waits plus the re-invoked calls (and their
	// RateLimiter retries) — so a caller without a deadline cannot park
	// forever on a hard-down provider. It is measured from the start of the
	// first throttled invocation and checked before each wait.
	throttleBudget = 10 * time.Minute
)

// throttleClock is the time source of throttlePatience: Now for the budget,
// After for the waits. Production uses realThrottleClock.
type throttleClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realThrottleClock is the wall clock.
type realThrottleClock struct{}

func (realThrottleClock) Now() time.Time                         { return time.Now() }
func (realThrottleClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// virtualThrottleClock is a throttleClock that never sleeps: After moves the
// clock forward by the wait and fires at once. It does not follow the wall
// clock, so only the patience waits count toward the budget.
type virtualThrottleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *virtualThrottleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualThrottleClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	fired := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- fired
	return ch
}

// WithInstantThrottleWaitsForTesting is for tests outside this package only.
// It puts the agent's throttle patience on a virtual clock: each throttle
// wait returns at once and moves that clock forward by the wait. The budget,
// the backoff steps and the error that surfaces when the budget is used up
// do not change; only the real sleeping is removed. A test that drives an
// always-throttling provider through a server or harness thus sees the
// throttle error after the full budget of virtual time, in milliseconds.
// Do not use it in production: it removes the pause between calls to a
// throttled provider.
func WithInstantThrottleWaitsForTesting() Option {
	return func(a *Agent) {
		a.throttleClock = &virtualThrottleClock{now: time.Unix(0, 0)}
	}
}

// newThrottlePatience returns the patience state for one LLM call, on the
// agent's throttle clock (the real clock unless a test set one).
func (a *Agent) newThrottlePatience() throttlePatience {
	if a.throttleClock != nil {
		return throttlePatience{clock: a.throttleClock}
	}
	return throttlePatience{clock: realThrottleClock{}}
}

// beginAttempt records the start of a provider invocation. Call it right
// before each invocation, so the budget also covers the time the first
// throttled invocation spent inside the provider (its limiter retries).
func (p *throttlePatience) beginAttempt() {
	p.attemptStart = p.now()
}

func (p *throttlePatience) now() time.Time {
	if p.clock == nil {
		p.clock = realThrottleClock{}
	}
	return p.clock.Now()
}

// isRetryableThrottle reports whether err is a throttle that throttle patience
// should wait out. A positively-identified context-too-long refusal never is,
// even when llm.IsThrottle classifies it as one: IsThrottle falls back to a
// substring match on the error text, and a provider that puts the raw 400
// body into the error (Anthropic: "prompt is too long: 204296 tokens > ...")
// can contain "429". Waiting that out would re-send a prompt that can never
// fit for the whole budget, before relief can run. All three paths use this
// one check, so they cannot drift.
func isRetryableThrottle(err error) bool {
	return llm.IsThrottle(err) && !errors.Is(err, llm.ErrContextTooLong)
}

// absorbThrottle records the throttle with the slot scheduler, then waits it
// out. The observation has to happen HERE, at absorption: chatWithRetry's
// deferred observeSchedulerOutcome only ever sees the call's FINAL error, so
// a throttle that patience rides out and then succeeds would reach the AIMD
// seam as a SUCCESS and GROW the scope's ceiling — the exact opposite of what
// a congestion signal must do. The scheduler already collapses repeat
// observations within one congestion event, so absorbing a burst does not
// halve the ceiling repeatedly.
func (a *Agent) absorbThrottle(ctx Context, p *throttlePatience, err error) error {
	if scheduler.Enabled() {
		scheduler.ObserveThrottleForScope(a.schedulerScope(), llm.RetryAfter(err))
	}
	return p.wait(ctx, err)
}

// wait waits out one throttle, or returns the error that ends the call: the
// throttle error once elapsed + the next wait would pass throttleBudget, or
// the throttle error joined with the context error if ctx ends first.
func (p *throttlePatience) wait(ctx Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("llm throttled and context ended before wait: %w: %w", err, ctxErr)
	}
	now := p.now()
	if p.start.IsZero() {
		p.start = p.attemptStart
		if p.start.IsZero() {
			p.start = now
		}
	}
	elapsed := now.Sub(p.start)
	if p.delay == 0 {
		p.delay = throttleInitialDelay
	}
	wait := p.delay
	if hint := llm.RetryAfter(err); hint > wait {
		wait = hint
	}
	if elapsed+wait > throttleBudget {
		return fmt.Errorf("llm throttled for %s; the next %s wait would pass the %s patience budget: %w",
			elapsed, wait, throttleBudget, err)
	}
	zap.L().Warn("llm throttled; conversation paused, not failed",
		zap.Duration("wait", wait),
		zap.Duration("throttled_so_far", elapsed),
		zap.Error(err),
	)
	select {
	case <-ctx.Done():
		p.recordWait(ctx, wait, now, true)
		// Wrap both: callers match the context error (errors.Is), and the
		// throttle error keeps its classification and retry-after hint.
		return fmt.Errorf("llm throttled and context ended during wait: %w: %w", err, ctx.Err())
	case <-p.clock.After(wait):
	}
	p.recordWait(ctx, wait, now, false)
	p.delay *= 2
	if p.delay > throttleMaxDelay {
		p.delay = throttleMaxDelay
	}
	return nil
}

// recordWait adds one llm.throttle_wait event for a wait that has ended. The
// pause can last minutes, so it is visible on the trace too: the event lands
// on the span already in the call's context (the agent's top-level chat
// span), next to the turn's other span events. It is emitted after the wait,
// so it records the time actually waited, not the planned wait:
//   - wait_ms: the planned wait;
//   - waited_ms: the time actually waited (less than wait_ms when the
//     context ended during the wait);
//   - throttled_total_ms: the wall-clock time throttled when the wait ended;
//   - interrupted: true when the context ended during the wait.
func (p *throttlePatience) recordWait(ctx Context, wait time.Duration, waitStart time.Time, interrupted bool) {
	span := observability.SpanFromContext(ctx)
	if span == nil {
		return
	}
	end := p.now()
	span.AddEvent("llm.throttle_wait", map[string]interface{}{
		"wait_ms":            wait.Milliseconds(),
		"waited_ms":          end.Sub(waitStart).Milliseconds(),
		"throttled_total_ms": end.Sub(p.start).Milliseconds(),
		"budget_ms":          throttleBudget.Milliseconds(),
		"interrupted":        interrupted,
	})
}

// toolInputActivityInterval throttles the IsToolInputStream progress events
// emitted while a provider streams tool-input deltas: liveness needs a pulse,
// not one event per JSON fragment.
const toolInputActivityInterval = time.Second

// chatWithStreaming uses streaming API with token buffering and progress emission.
func (a *Agent) chatWithStreaming(ctx Context, messages []Message, tools []shuttle.Tool, progressCallback ProgressCallback) (*LLMResponse, error) {
	streamingProvider, ok := a.llm.(llmtypes.StreamingLLMProvider)
	if !ok {
		// Fallback to non-streaming (should never happen due to check in chatWithRetry)
		return a.llm.Chat(a.providerCtx(ctx, messages), messages, tools)
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

	// Tool-input (function-call argument) bytes never reach tokenCallback —
	// the callback is text only, because its tokens become the visible
	// partial response — so a tool argument the size of a document streams
	// for minutes with no progress event at all, and anything watching for
	// activity (an idle-based turn deadline, a proxy inactivity timer) sees a
	// stalled stream. Providers report those deltas through
	// NotifyStreamActivity; emit them as a throttled, content-free progress
	// event so they count as activity without leaking JSON into the text.
	var lastToolInputEmit time.Time
	// toolInputReported flips once a provider reports per-call progress; that
	// hook then owns the events, so the bare activity pulse stays silent.
	toolInputReported := false
	// Same suppression as the non-streaming path: a resumed turn's rebuilt
	// rows carry no thinking blocks, and the provider rejects that pairing.
	streamCtx := llmtypes.WithStreamActivity(a.providerCtx(ctx, messages), func() {
		if toolInputReported {
			return
		}
		now := time.Now()
		if now.Sub(lastToolInputEmit) < toolInputActivityInterval {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		lastToolInputEmit = now
		progressCallback(ProgressEvent{
			Stage:             StageLLMGeneration,
			Progress:          50,
			Message:           "Generating tool call...",
			Timestamp:         now,
			IsToolInputStream: true,
			Droppable:         true,
			TokenCount:        tokenCount,
			TTFT:              ttft,
		})
	})

	// Providers that know which call is streaming also report its id, name and
	// size so far. The first delta of each call (and the delta that first
	// supplies its name or id) is emitted immediately so a consumer can show
	// the tool before its arguments finish; later deltas are throttled.
	type toolInputCall struct {
		id, name string
		lastEmit time.Time
	}
	toolInputCalls := map[int]*toolInputCall{}
	streamCtx = llmtypes.WithToolInputProgress(streamCtx, func(p llmtypes.ToolInputProgress) {
		toolInputReported = true
		now := time.Now()
		call, seen := toolInputCalls[p.Index]
		if !seen {
			call = &toolInputCall{}
			toolInputCalls[p.Index] = call
		}
		identityChanged := !seen ||
			(p.ToolName != "" && p.ToolName != call.name) ||
			(p.ToolCallID != "" && p.ToolCallID != call.id)
		if p.ToolName != "" {
			call.name = p.ToolName
		}
		if p.ToolCallID != "" {
			call.id = p.ToolCallID
		}
		if !identityChanged && now.Sub(call.lastEmit) < toolInputActivityInterval {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		call.lastEmit = now
		progressCallback(ProgressEvent{
			Stage:             StageLLMGeneration,
			Progress:          50,
			Message:           "Generating tool call...",
			Timestamp:         now,
			IsToolInputStream: true,
			Droppable:         true,
			TokenCount:        tokenCount,
			TTFT:              ttft,
			ToolName:          call.name,
			ToolCallID:        call.id,
			ToolInputBytes:    int64(p.Bytes),
			ToolCallIndex:     int32(p.Index),
		})
	})

	// Call streaming provider. Throttling pauses and re-invokes — the
	// previous behavior ("streaming already handles errors") returned the
	// first 429 straight up the stack and killed the conversation. Buffered
	// state resets per attempt so a retried stream never duplicates tokens.
	patience := a.newThrottlePatience()
	var resp *LLMResponse
	for {
		patience.beginAttempt()
		buffer.Reset()
		tokenCount = 0
		ttft = 0
		ttftRecorded = false
		clear(toolInputCalls)

		var err error
		resp, err = streamingProvider.ChatStream(streamCtx, messages, tools, tokenCallback)
		if err == nil {
			break
		}
		if isRetryableThrottle(err) {
			if waitErr := a.absorbThrottle(ctx, &patience, err); waitErr != nil {
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

// schedulerScope derives the quota scope of this agent's LLM provider for
// the slot scheduler: the provider's own boundary when it exposes one,
// otherwise name|model.
func (a *Agent) schedulerScope() string {
	return scheduler.ScopeFor(a.llm.Name(), a.llm.Model(), a.llm)
}

// estimateReservation mirrors reservation-accounting providers, which debit
// prompt-estimate + max_tokens at admission: a cheap chars/4 prompt estimate
// plus the configured completion budget.
func (a *Agent) estimateReservation(messages []Message) int64 {
	var chars int
	for _, m := range messages {
		chars += len(m.Content)
	}
	out := int64(a.config.ReservedOutputTokens)
	if out <= 0 {
		out = 4096 // provider-default max_tokens when unconfigured
	}
	est := int64(chars/4) + out
	if est < 1 {
		est = 1
	}
	return est
}
