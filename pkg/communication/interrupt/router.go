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
package interrupt

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
)

// routerEntry represents a single registered handler with its delivery channel.
type routerEntry struct {
	agentID  string
	signal   InterruptSignal
	handler  Handler
	channel  chan *routerMessage
	cancelFn context.CancelFunc
}

// routerMessage is the internal message format for the fast path.
type routerMessage struct {
	ctx        context.Context
	signal     InterruptSignal
	payload    []byte
	timestamp  time.Time
	responseCh chan error // For synchronous delivery confirmation
}

// Router handles fast-path interrupt delivery via Go channels.
// Each registered handler gets a dedicated channel with priority-appropriate buffer size.
//
// Design:
// - Dedicated channel per handler (no multiplexing contention)
// - Non-blocking sends (returns false if buffer full)
// - Background goroutine processes each handler's queue
// - Graceful shutdown waits for in-flight handlers
type Router struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	entries map[string]map[InterruptSignal]*routerEntry // agentID -> signal -> entry

	wg sync.WaitGroup // Tracks background handler goroutines

	tracer observability.Tracer // Optional tracer for observability
}

// NewRouter creates a new fast-path router.
func NewRouter(ctx context.Context) *Router {
	ctx, cancel := context.WithCancel(ctx)
	return &Router{
		ctx:     ctx,
		cancel:  cancel,
		entries: make(map[string]map[InterruptSignal]*routerEntry),
	}
}

// WithTracer sets an optional tracer for observability.
func (r *Router) WithTracer(tracer observability.Tracer) *Router {
	r.tracer = tracer
	return r
}

// RegisterHandler registers a handler for fast-path delivery.
// Creates a dedicated channel and background goroutine for this handler.
func (r *Router) RegisterHandler(agentID string, signal InterruptSignal, handler Handler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Initialize agent's entry map if needed
	if r.entries[agentID] == nil {
		r.entries[agentID] = make(map[InterruptSignal]*routerEntry)
	}

	// Check for duplicate registration
	if _, exists := r.entries[agentID][signal]; exists {
		return fmt.Errorf("handler already registered for agent %s, signal %s", agentID, signal)
	}

	// Create dedicated channel with priority-appropriate buffer size
	bufferSize := signal.Priority().BufferSize()
	ch := make(chan *routerMessage, bufferSize)

	// Create handler goroutine context
	handlerCtx, handlerCancel := context.WithCancel(r.ctx)

	entry := &routerEntry{
		agentID:  agentID,
		signal:   signal,
		handler:  handler,
		channel:  ch,
		cancelFn: handlerCancel,
	}

	// Start background handler goroutine
	r.wg.Add(1)
	go r.runHandler(handlerCtx, entry)

	// Register entry
	r.entries[agentID][signal] = entry

	return nil
}

// UnregisterHandler removes a handler and stops its background goroutine.
func (r *Router) UnregisterHandler(agentID string, signal InterruptSignal) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	agentEntries := r.entries[agentID]
	if agentEntries == nil {
		return fmt.Errorf("no handlers registered for agent %s", agentID)
	}

	entry, exists := agentEntries[signal]
	if !exists {
		return fmt.Errorf("no handler registered for agent %s, signal %s", agentID, signal)
	}

	// Cancel handler goroutine
	entry.cancelFn()
	close(entry.channel)

	// Remove entry
	delete(r.entries[agentID], signal)
	if len(r.entries[agentID]) == 0 {
		delete(r.entries, agentID)
	}

	return nil
}

// Send attempts non-blocking delivery to a specific agent's handler.
// Returns (true, nil) if delivered successfully.
// Returns (false, nil) if buffer is full (caller should fall back to persistent queue).
// Returns (false, err) if handler not found.
func (r *Router) Send(ctx context.Context, signal InterruptSignal, targetAgentID string, payload []byte) (bool, error) {
	r.mu.RLock()
	agentEntries := r.entries[targetAgentID]
	entry := agentEntries[signal]
	r.mu.RUnlock()

	if entry == nil {
		return false, fmt.Errorf("no handler registered for agent %s, signal %s", targetAgentID, signal)
	}

	msg := &routerMessage{
		ctx:        ctx,
		signal:     signal,
		payload:    payload,
		timestamp:  time.Now(),
		responseCh: make(chan error, 1), // Buffered for non-blocking send
	}

	// Non-blocking send
	select {
	case entry.channel <- msg:
		// Successfully queued for delivery
		return true, nil
	default:
		// Buffer full - caller should fall back to persistent queue
		return false, nil
	}
}

// runHandler is the background goroutine that processes messages for a single handler.
// Runs until the handler is unregistered or router is closed.
func (r *Router) runHandler(ctx context.Context, entry *routerEntry) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			// Handler unregistered or router closed
			return

		case msg, ok := <-entry.channel:
			if !ok {
				// Channel closed (handler unregistered)
				return
			}

			// Start observability span if tracer available
			var span *observability.Span
			handlerCtx := msg.ctx
			if r.tracer != nil {
				handlerCtx, span = r.tracer.StartSpan(msg.ctx, observability.SpanInterruptHandle,
					observability.WithAttribute(observability.AttrInterruptSignal, msg.signal.String()),
					observability.WithAttribute(observability.AttrInterruptPriority, msg.signal.Priority().String()),
					observability.WithAttribute(observability.AttrInterruptTarget, entry.agentID),
					observability.WithAttribute(observability.AttrInterruptPath, "fast"),
				)
			}

			// Execute handler
			start := time.Now()
			err := entry.handler(handlerCtx, msg.signal, msg.payload)
			latency := time.Since(start)

			// Record metrics to Hawk
			if r.tracer != nil {
				r.tracer.RecordMetric(observability.MetricInterruptLatency, float64(latency.Milliseconds()), map[string]string{
					observability.AttrInterruptSignal:   msg.signal.String(),
					observability.AttrInterruptPriority: msg.signal.Priority().String(),
					observability.AttrInterruptPath:     "fast",
				})

				if err != nil {
					r.tracer.RecordMetric(observability.MetricInterruptDropped, 1.0, map[string]string{
						observability.AttrInterruptSignal: msg.signal.String(),
						observability.AttrErrorMessage:    err.Error(),
					})
					span.SetAttribute(observability.AttrErrorMessage, err.Error())
					span.SetAttribute(observability.AttrInterruptDelivered, "false")
				} else {
					r.tracer.RecordMetric(observability.MetricInterruptDelivered, 1.0, map[string]string{
						observability.AttrInterruptSignal:   msg.signal.String(),
						observability.AttrInterruptPriority: msg.signal.Priority().String(),
					})
					span.SetAttribute(observability.AttrInterruptDelivered, "true")
				}

				r.tracer.EndSpan(span)
			}

			// Send response (non-blocking)
			select {
			case msg.responseCh <- err:
			default:
				// Response channel full or abandoned - ignore
			}
		}
	}
}

// Close shuts down the router gracefully.
// Waits for all in-flight handlers to complete (with timeout).
func (r *Router) Close() error {
	r.cancel()

	// Wait for all handler goroutines to finish (with timeout)
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("router close timeout: some handlers did not finish within 30s")
	}
}

// GetStats returns router statistics.
func (r *Router) GetStats() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := make(map[string]int)
	totalHandlers := 0
	for agentID, entries := range r.entries {
		stats[fmt.Sprintf("agent_%s_handlers", agentID)] = len(entries)
		totalHandlers += len(entries)
	}
	stats["total_handlers"] = totalHandlers
	stats["total_agents"] = len(r.entries)

	return stats
}
