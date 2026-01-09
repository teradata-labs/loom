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

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Handler is a function that processes an interrupt signal.
// Handlers must be idempotent and fast (<100ms for non-CRITICAL, <10ms for CRITICAL).
// Long-running work should be dispatched asynchronously.
type Handler func(ctx context.Context, signal InterruptSignal, payload []byte) error

// Interrupt represents a single interrupt message.
type Interrupt struct {
	ID        string          // Unique interrupt ID for tracing
	TraceID   string          // Trace ID for correlation across system
	Signal    InterruptSignal // The interrupt signal
	TargetID  string          // Target agent ID (empty for broadcast)
	Payload   []byte          // Optional payload (JSON recommended)
	Timestamp time.Time       // When the interrupt was created
	SenderID  string          // ID of the sender (for tracing)
}

// HandlerRegistration tracks a registered interrupt handler.
type HandlerRegistration struct {
	AgentID      string          // Agent that registered this handler
	Signal       InterruptSignal // Signal to handle
	Handler      Handler         // Handler function
	WakeOnSignal bool            // If true, wake DORMANT agent when signal received
}

// InterruptChannel is the 4th communication channel in Loom's quad-modal system.
// It provides targeted, guaranteed interrupt delivery with type-safe enums.
//
// Architecture:
// - Fast path: Go channels with large buffers (<1ms delivery)
// - Slow path: Persistent SQLite queue for CRITICAL signals (guaranteed delivery)
// - Router: Multiplexes signals to registered handlers
// - Type-safe: Compile-time signal validation via enums
//
// Usage:
//
//	ic := interrupt.NewInterruptChannel(ctx, router, queue)
//	defer ic.Close()
//
//	// Register handler
//	ic.RegisterHandler("my-agent", SignalEmergencyStop, myHandler, true)
//
//	// Send to specific agent
//	ic.Send(ctx, SignalEmergencyStop, "my-agent", payload)
//
//	// Broadcast to all handlers for signal
//	ic.Broadcast(ctx, SignalSystemShutdown, payload)
type InterruptChannel struct {
	ctx    context.Context
	cancel context.CancelFunc

	// Router handles fast-path delivery via Go channels
	router *Router

	// PersistentQueue handles slow-path delivery for CRITICAL signals
	queue *PersistentQueue

	// Handler registry
	mu       sync.RWMutex
	handlers map[string]map[InterruptSignal]*HandlerRegistration // agentID -> signal -> registration

	// Metrics
	metricsMu    sync.Mutex
	totalSent    int64
	totalDropped int64
	totalRetried int64

	// Observability
	tracer observability.Tracer // Optional tracer for Hawk integration

	// Lifecycle hooks (for testing and observability)
	onSend      func(i *Interrupt)
	onDelivered func(i *Interrupt, agentID string)
	onDropped   func(i *Interrupt, reason string)
}

// NewInterruptChannel creates a new interrupt channel.
// The router handles fast-path delivery, and queue handles slow-path (CRITICAL only).
func NewInterruptChannel(ctx context.Context, router *Router, queue *PersistentQueue) *InterruptChannel {
	ctx, cancel := context.WithCancel(ctx)
	ic := &InterruptChannel{
		ctx:      ctx,
		cancel:   cancel,
		router:   router,
		queue:    queue,
		handlers: make(map[string]map[InterruptSignal]*HandlerRegistration),
	}
	return ic
}

// WithTracer sets an optional tracer for observability.
// This enables Hawk integration for spans, metrics, and tracing.
func (ic *InterruptChannel) WithTracer(tracer observability.Tracer) *InterruptChannel {
	ic.tracer = tracer
	// Also set tracer on router for handler instrumentation
	if ic.router != nil {
		ic.router.WithTracer(tracer)
	}
	return ic
}

// RegisterHandler registers a handler for a specific signal on an agent.
// If wakeOnSignal is true, the agent will be awakened from DORMANT state when this signal is received.
// Returns an error if the agent/signal combination is already registered.
func (ic *InterruptChannel) RegisterHandler(
	agentID string,
	signal InterruptSignal,
	handler Handler,
	wakeOnSignal bool,
) error {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	// Initialize agent's handler map if needed
	if ic.handlers[agentID] == nil {
		ic.handlers[agentID] = make(map[InterruptSignal]*HandlerRegistration)
	}

	// Check for duplicate registration
	if _, exists := ic.handlers[agentID][signal]; exists {
		return fmt.Errorf("handler already registered for agent %s, signal %s", agentID, signal)
	}

	// Register handler
	reg := &HandlerRegistration{
		AgentID:      agentID,
		Signal:       signal,
		Handler:      handler,
		WakeOnSignal: wakeOnSignal,
	}
	ic.handlers[agentID][signal] = reg

	// Register with router for fast-path delivery
	if err := ic.router.RegisterHandler(agentID, signal, handler); err != nil {
		// Rollback registration
		delete(ic.handlers[agentID], signal)
		return fmt.Errorf("failed to register with router: %w", err)
	}

	return nil
}

// UnregisterHandler removes a handler for a specific signal on an agent.
func (ic *InterruptChannel) UnregisterHandler(agentID string, signal InterruptSignal) error {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	if ic.handlers[agentID] == nil {
		return fmt.Errorf("no handlers registered for agent %s", agentID)
	}

	if _, exists := ic.handlers[agentID][signal]; !exists {
		return fmt.Errorf("no handler registered for agent %s, signal %s", agentID, signal)
	}

	// Unregister from router
	if err := ic.router.UnregisterHandler(agentID, signal); err != nil {
		return fmt.Errorf("failed to unregister from router: %w", err)
	}

	// Remove from handlers map
	delete(ic.handlers[agentID], signal)
	if len(ic.handlers[agentID]) == 0 {
		delete(ic.handlers, agentID)
	}

	return nil
}

// Send sends an interrupt to a specific agent.
// For CRITICAL signals, falls back to persistent queue if fast path fails.
func (ic *InterruptChannel) Send(
	ctx context.Context,
	signal InterruptSignal,
	targetAgentID string,
	payload []byte,
) error {
	return ic.SendFrom(ctx, signal, targetAgentID, payload, "")
}

// SendFrom sends an interrupt with explicit sender ID (for tracing).
func (ic *InterruptChannel) SendFrom(
	ctx context.Context,
	signal InterruptSignal,
	targetAgentID string,
	payload []byte,
	senderID string,
) error {
	// Start observability span
	var span *observability.Span
	if ic.tracer != nil {
		ctx, span = ic.tracer.StartSpan(ctx, observability.SpanInterruptSend,
			observability.WithAttribute(observability.AttrInterruptSignal, signal.String()),
			observability.WithAttribute(observability.AttrInterruptPriority, signal.Priority().String()),
			observability.WithAttribute(observability.AttrInterruptTarget, targetAgentID),
			observability.WithAttribute(observability.AttrInterruptSender, senderID),
		)
		defer ic.tracer.EndSpan(span)
	}

	// Create interrupt with tracing IDs
	interrupt := &Interrupt{
		ID:        uuid.New().String(),
		TraceID:   "", // Will be set from span if available
		Signal:    signal,
		TargetID:  targetAgentID,
		Payload:   payload,
		Timestamp: time.Now(),
		SenderID:  senderID,
	}

	// Add interrupt ID and trace ID to span
	if span != nil {
		interrupt.TraceID = span.TraceID
		span.SetAttribute("interrupt.id", interrupt.ID)
		span.SetAttribute(observability.AttrTraceID, interrupt.TraceID)
	}

	// Fire onSend hook
	if ic.onSend != nil {
		ic.onSend(interrupt)
	}

	ic.metricsMu.Lock()
	ic.totalSent++
	ic.metricsMu.Unlock()

	// Record metric
	if ic.tracer != nil {
		ic.tracer.RecordMetric(observability.MetricInterruptSent, 1.0, map[string]string{
			observability.AttrInterruptSignal:   signal.String(),
			observability.AttrInterruptPriority: signal.Priority().String(),
		})
	}

	// Check if handler exists
	ic.mu.RLock()
	agentHandlers := ic.handlers[targetAgentID]
	registration, exists := agentHandlers[signal]
	ic.mu.RUnlock()

	if !exists {
		ic.recordDropped(interrupt, "no handler registered")
		return fmt.Errorf("no handler registered for agent %s, signal %s", targetAgentID, signal)
	}

	// Attempt fast path delivery
	delivered, err := ic.router.Send(ctx, signal, targetAgentID, payload)
	if err != nil {
		// Fast path failed
		if signal.IsCritical() {
			// Fall back to persistent queue for CRITICAL signals
			if queueErr := ic.queue.Enqueue(ctx, interrupt); queueErr != nil {
				ic.recordDropped(interrupt, fmt.Sprintf("fast path failed, queue failed: %v", queueErr))
				return fmt.Errorf("fast path failed: %w, queue failed: %v", err, queueErr)
			}
			ic.metricsMu.Lock()
			ic.totalRetried++
			ic.metricsMu.Unlock()
			return nil // Queued successfully
		}
		// Non-critical signals: drop
		ic.recordDropped(interrupt, err.Error())
		return err
	}

	if delivered {
		ic.recordDelivered(interrupt, targetAgentID)
		// Note: WakeOnSignal functionality requires LifecycleManager integration
		// This will be implemented when LifecycleManager is added in Phase 3-4
		// For now, handler execution implies agent is ACTIVE
		_ = registration.WakeOnSignal
	} else {
		// Fast path buffer full
		if signal.IsCritical() {
			// Fall back to persistent queue
			if queueErr := ic.queue.Enqueue(ctx, interrupt); queueErr != nil {
				ic.recordDropped(interrupt, fmt.Sprintf("fast path full, queue failed: %v", queueErr))
				return fmt.Errorf("fast path full, queue failed: %v", queueErr)
			}
			ic.metricsMu.Lock()
			ic.totalRetried++
			ic.metricsMu.Unlock()
			return nil // Queued successfully
		}
		// Non-critical signals: drop
		ic.recordDropped(interrupt, "buffer full")
		return fmt.Errorf("buffer full for signal %s", signal)
	}

	return nil
}

// Broadcast sends an interrupt to all registered handlers for the signal.
// For CRITICAL signals, falls back to persistent queue for any failed deliveries.
func (ic *InterruptChannel) Broadcast(
	ctx context.Context,
	signal InterruptSignal,
	payload []byte,
) error {
	return ic.BroadcastFrom(ctx, signal, payload, "")
}

// BroadcastFrom broadcasts an interrupt with explicit sender ID (for tracing).
func (ic *InterruptChannel) BroadcastFrom(
	ctx context.Context,
	signal InterruptSignal,
	payload []byte,
	senderID string,
) error {
	// Find all handlers for this signal
	ic.mu.RLock()
	var targets []string
	for agentID, handlers := range ic.handlers {
		if _, exists := handlers[signal]; exists {
			targets = append(targets, agentID)
		}
	}
	ic.mu.RUnlock()

	if len(targets) == 0 {
		return fmt.Errorf("no handlers registered for signal %s", signal)
	}

	// Send to each target
	var firstErr error
	for _, targetAgentID := range targets {
		if err := ic.SendFrom(ctx, signal, targetAgentID, payload, senderID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr // Return first error encountered (if any)
}

// GetStats returns current interrupt channel statistics.
func (ic *InterruptChannel) GetStats() (sent, dropped, retried int64) {
	ic.metricsMu.Lock()
	defer ic.metricsMu.Unlock()
	return ic.totalSent, ic.totalDropped, ic.totalRetried
}

// ListHandlers returns all registered handlers for an agent.
func (ic *InterruptChannel) ListHandlers(agentID string) []InterruptSignal {
	ic.mu.RLock()
	defer ic.mu.RUnlock()

	agentHandlers := ic.handlers[agentID]
	if agentHandlers == nil {
		return nil
	}

	signals := make([]InterruptSignal, 0, len(agentHandlers))
	for signal := range agentHandlers {
		signals = append(signals, signal)
	}
	return signals
}

// ListAgents returns all agent IDs with registered handlers.
func (ic *InterruptChannel) ListAgents() []string {
	ic.mu.RLock()
	defer ic.mu.RUnlock()

	agents := make([]string, 0, len(ic.handlers))
	for agentID := range ic.handlers {
		agents = append(agents, agentID)
	}
	return agents
}

// Close shuts down the interrupt channel gracefully.
func (ic *InterruptChannel) Close() error {
	ic.cancel()
	// Router and Queue close will be handled by their owners
	return nil
}

// SetHooks sets lifecycle hooks for testing and observability.
func (ic *InterruptChannel) SetHooks(
	onSend func(i *Interrupt),
	onDelivered func(i *Interrupt, agentID string),
	onDropped func(i *Interrupt, reason string),
) {
	ic.onSend = onSend
	ic.onDelivered = onDelivered
	ic.onDropped = onDropped
}

// recordDelivered fires the onDelivered hook and updates metrics.
func (ic *InterruptChannel) recordDelivered(i *Interrupt, agentID string) {
	if ic.onDelivered != nil {
		ic.onDelivered(i, agentID)
	}
}

// recordDropped fires the onDropped hook and updates metrics.
func (ic *InterruptChannel) recordDropped(i *Interrupt, reason string) {
	ic.metricsMu.Lock()
	ic.totalDropped++
	ic.metricsMu.Unlock()

	if ic.onDropped != nil {
		ic.onDropped(i, reason)
	}
}
