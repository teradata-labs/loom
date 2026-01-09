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
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4" // SQLite driver
	"github.com/teradata-labs/loom/pkg/observability"
)

// PersistentQueue provides guaranteed delivery for CRITICAL interrupts.
// Uses SQLite for persistence and implements retry logic with exponential backoff.
//
// Design:
// - SQLite database for persistence (survives process restarts)
// - Retry loop with exponential backoff (100ms, 200ms, 400ms, ..., up to 30s)
// - Maximum 50 retry attempts per interrupt
// - ACK protocol for delivery confirmation (delivered -> acknowledged)
// - Background goroutine processes queue every 100ms
type PersistentQueue struct {
	ctx    context.Context
	cancel context.CancelFunc

	db *sql.DB
	mu sync.Mutex

	// Router reference for delivery attempts
	router *Router

	// Retry configuration
	maxRetries    int
	retryInterval time.Duration

	// Observability
	tracer observability.Tracer // Optional tracer for metrics/tracing

	wg sync.WaitGroup // Tracks background retry goroutine
}

// QueueEntry represents a persisted interrupt in the queue.
type QueueEntry struct {
	ID           int64      `json:"id"`
	Signal       int        `json:"signal"`        // InterruptSignal as int
	TargetID     string     `json:"target_id"`     // Target agent ID
	Payload      string     `json:"payload"`       // JSON payload
	SenderID     string     `json:"sender_id"`     // Sender ID for tracing
	CreatedAt    time.Time  `json:"created_at"`    // When interrupt was created
	EnqueuedAt   time.Time  `json:"enqueued_at"`   // When interrupt was queued
	DeliveredAt  *time.Time `json:"delivered_at"`  // When delivered (null if pending)
	AckAt        *time.Time `json:"ack_at"`        // When acknowledged (null if pending)
	RetryCount   int        `json:"retry_count"`   // Number of retry attempts
	State        string     `json:"state"`         // pending, delivered, acknowledged, failed
	ErrorMessage string     `json:"error_message"` // Last error message (if any)
}

const schema = `
CREATE TABLE IF NOT EXISTS interrupt_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    signal INTEGER NOT NULL,
    target_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    enqueued_at INTEGER NOT NULL,
    delivered_at INTEGER,
    ack_at INTEGER,
    retry_count INTEGER DEFAULT 0,
    max_retries INTEGER DEFAULT 50,
    state TEXT DEFAULT 'pending',
    error_message TEXT DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_state ON interrupt_queue(state);
CREATE INDEX IF NOT EXISTS idx_target_id ON interrupt_queue(target_id);
CREATE INDEX IF NOT EXISTS idx_created_at ON interrupt_queue(created_at);
`

// NewPersistentQueue creates a new persistent interrupt queue.
// dbPath is the path to the SQLite database file.
func NewPersistentQueue(ctx context.Context, dbPath string, router *Router) (*PersistentQueue, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create schema
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	pq := &PersistentQueue{
		ctx:           ctx,
		cancel:        cancel,
		db:            db,
		router:        router,
		maxRetries:    50,
		retryInterval: 100 * time.Millisecond,
	}

	// Start background retry loop
	pq.wg.Add(1)
	go pq.retryLoop()

	return pq, nil
}

// WithTracer sets an optional tracer for observability.
func (pq *PersistentQueue) WithTracer(tracer observability.Tracer) *PersistentQueue {
	pq.tracer = tracer
	return pq
}

// Enqueue adds a CRITICAL interrupt to the persistent queue.
func (pq *PersistentQueue) Enqueue(ctx context.Context, interrupt *Interrupt) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Serialize payload
	payloadJSON, err := json.Marshal(interrupt.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	now := time.Now().Unix()
	createdAtUnix := interrupt.Timestamp.Unix()

	query := `
		INSERT INTO interrupt_queue (
			signal, target_id, payload, sender_id, created_at, enqueued_at, state
		) VALUES (?, ?, ?, ?, ?, ?, 'pending')
	`

	result, err := pq.db.ExecContext(ctx, query,
		int(interrupt.Signal),
		interrupt.TargetID,
		string(payloadJSON),
		interrupt.SenderID,
		createdAtUnix,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert into queue: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get insert id: %w", err)
	}

	// Store queue ID in interrupt for tracing (if tracer is configured in the future)
	// This allows correlation between the interrupt and its persistent queue entry
	_ = id

	return nil
}

// retryLoop is the background goroutine that processes pending interrupts.
// Implements exponential backoff retry logic with a maximum of 50 attempts.
func (pq *PersistentQueue) retryLoop() {
	defer pq.wg.Done()

	ticker := time.NewTicker(pq.retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pq.ctx.Done():
			return
		case <-ticker.C:
			// Process pending interrupts
			if err := pq.processPendingInterrupts(); err != nil {
				// Log error but continue processing
				// In production, this would be logged via tracer
				_ = err
			}
		}
	}
}

// processPendingInterrupts attempts to deliver all pending interrupts.
func (pq *PersistentQueue) processPendingInterrupts() error {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Start observability span if tracer available
	var span *observability.Span
	if pq.tracer != nil {
		_, span = pq.tracer.StartSpan(pq.ctx, observability.SpanInterruptRetry)
		defer pq.tracer.EndSpan(span)
	}

	// Select pending interrupts that are ready for retry (using exponential backoff)
	query := `
		SELECT id, signal, target_id, payload, sender_id, created_at, retry_count, max_retries
		FROM interrupt_queue
		WHERE state = 'pending'
		  AND retry_count < max_retries
		ORDER BY created_at ASC
		LIMIT 100
	`

	rows, err := pq.db.QueryContext(pq.ctx, query)
	if err != nil {
		if span != nil {
			span.RecordError(err)
		}
		return fmt.Errorf("failed to query pending interrupts: %w", err)
	}
	defer rows.Close()

	// First, collect all pending interrupts into a slice
	// (can't modify table while iterating over rows in SQLite)
	type pendingInterrupt struct {
		id, signal                        int64
		targetID, payloadJSON, senderID   string
		createdAt, retryCount, maxRetries int64
	}
	var pending []pendingInterrupt

	for rows.Next() {
		var p pendingInterrupt
		if err := rows.Scan(&p.id, &p.signal, &p.targetID, &p.payloadJSON, &p.senderID, &p.createdAt, &p.retryCount, &p.maxRetries); err != nil {
			continue // Skip this row, continue with others
		}
		pending = append(pending, p)
	}
	rows.Close() // Close rows before modifying table

	// Now process each interrupt
	processed := 0
	failed := 0
	retried := 0
	for _, p := range pending {
		id := p.id
		signal := p.signal
		targetID := p.targetID
		payloadJSON := p.payloadJSON
		senderID := p.senderID
		createdAt := p.createdAt
		retryCount := p.retryCount
		maxRetries := p.maxRetries
		_ = senderID // unused for now

		// Calculate exponential backoff delay
		backoffDelay := pq.calculateBackoff(int(retryCount))
		timeSinceCreated := time.Since(time.Unix(createdAt, 0))
		if timeSinceCreated < backoffDelay {
			// Not ready for retry yet (exponential backoff)
			continue
		}

		// Parse payload
		var payload []byte
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			// Mark as failed if payload is invalid
			if markErr := pq.markFailed(id, fmt.Sprintf("invalid payload: %v", err)); markErr != nil {
				if span != nil {
					span.RecordError(markErr)
				}
			}
			continue
		}

		// Attempt delivery via router
		interruptSignal := InterruptSignal(signal)
		delivered, err := pq.router.Send(pq.ctx, interruptSignal, targetID, payload)

		if err != nil || !delivered {
			// Delivery failed - increment retry count
			errMsg := "buffer full"
			if err != nil {
				errMsg = err.Error()
			}

			if retryCount+1 >= maxRetries {
				// Max retries reached - mark as failed
				if markErr := pq.markFailed(id, fmt.Sprintf("max retries reached: %s", errMsg)); markErr != nil {
					if span != nil {
						span.RecordError(markErr)
					}
				}
				failed++
			} else {
				// Increment retry count
				if markErr := pq.incrementRetry(id, errMsg); markErr != nil {
					if span != nil {
						span.RecordError(markErr)
					}
				}
				retried++
			}
			continue
		}

		// Delivery successful - mark as delivered
		if markErr := pq.markDelivered(id); markErr != nil {
			if span != nil {
				span.RecordError(markErr)
			}
		}
		processed++
	}

	// Record metrics
	if pq.tracer != nil {
		if processed > 0 {
			pq.tracer.RecordMetric(observability.MetricInterruptDelivered, float64(processed), map[string]string{
				observability.AttrInterruptPath: "slow",
			})
		}
		if retried > 0 {
			pq.tracer.RecordMetric(observability.MetricInterruptRetried, float64(retried), map[string]string{
				observability.AttrInterruptPath: "slow",
			})
		}
		if failed > 0 {
			pq.tracer.RecordMetric(observability.MetricInterruptDropped, float64(failed), map[string]string{
				observability.AttrInterruptPath: "slow",
			})
		}
		span.SetAttribute("processed", fmt.Sprintf("%d", processed))
		span.SetAttribute("retried", fmt.Sprintf("%d", retried))
		span.SetAttribute("failed", fmt.Sprintf("%d", failed))
	}

	return nil
}

// calculateBackoff calculates exponential backoff delay based on retry count.
// Returns: 100ms, 200ms, 400ms, 800ms, 1.6s, 3.2s, 6.4s, 12.8s, 25.6s, ...
func (pq *PersistentQueue) calculateBackoff(retryCount int) time.Duration {
	if retryCount == 0 {
		return 0 // First attempt, no backoff
	}

	// Exponential backoff: baseInterval * 2^(retryCount-1)
	multiplier := int64(1 << uint(retryCount-1)) // 2^(retryCount-1)
	backoff := time.Duration(multiplier) * pq.retryInterval

	// Cap at 30 seconds
	maxBackoff := 30 * time.Second
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	return backoff
}

// markDelivered marks an interrupt as successfully delivered.
func (pq *PersistentQueue) markDelivered(id int64) error {
	now := time.Now().Unix()
	query := `UPDATE interrupt_queue SET state = 'delivered', delivered_at = ? WHERE id = ?`
	_, err := pq.db.ExecContext(pq.ctx, query, now, id)
	return err
}

// incrementRetry increments the retry count and updates the error message.
func (pq *PersistentQueue) incrementRetry(id int64, errMsg string) error {
	query := `UPDATE interrupt_queue SET retry_count = retry_count + 1, error_message = ? WHERE id = ?`
	_, err := pq.db.ExecContext(pq.ctx, query, errMsg, id)
	return err
}

// markFailed marks an interrupt as failed after exhausting all retries.
func (pq *PersistentQueue) markFailed(id int64, errMsg string) error {
	query := `UPDATE interrupt_queue SET state = 'failed', error_message = ? WHERE id = ?`
	_, err := pq.db.ExecContext(pq.ctx, query, errMsg, id)
	return err
}

// GetPendingCount returns the number of pending interrupts in the queue.
func (pq *PersistentQueue) GetPendingCount(ctx context.Context) (int, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	var count int
	query := `SELECT COUNT(*) FROM interrupt_queue WHERE state = 'pending'`
	if err := pq.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to get pending count: %w", err)
	}

	return count, nil
}

// GetStats returns queue statistics.
func (pq *PersistentQueue) GetStats(ctx context.Context) (map[string]int, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	stats := make(map[string]int)

	// Count by state
	query := `SELECT state, COUNT(*) FROM interrupt_queue GROUP BY state`
	rows, err := pq.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		stats[fmt.Sprintf("state_%s", state)] = count
	}

	return stats, nil
}

// ListPending returns pending interrupts ordered by age.
func (pq *PersistentQueue) ListPending(ctx context.Context, limit int) ([]*QueueEntry, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	query := `
		SELECT id, signal, target_id, payload, sender_id, created_at, enqueued_at,
		       delivered_at, ack_at, retry_count, state, error_message
		FROM interrupt_queue
		WHERE state = 'pending'
		ORDER BY created_at ASC
		LIMIT ?
	`

	rows, err := pq.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending: %w", err)
	}
	defer rows.Close()

	var entries []*QueueEntry
	for rows.Next() {
		entry := &QueueEntry{}
		var createdAtUnix, enqueuedAtUnix int64
		var deliveredAtUnix, ackAtUnix sql.NullInt64

		err := rows.Scan(
			&entry.ID,
			&entry.Signal,
			&entry.TargetID,
			&entry.Payload,
			&entry.SenderID,
			&createdAtUnix,
			&enqueuedAtUnix,
			&deliveredAtUnix,
			&ackAtUnix,
			&entry.RetryCount,
			&entry.State,
			&entry.ErrorMessage,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert Unix timestamps to time.Time
		entry.CreatedAt = time.Unix(createdAtUnix, 0)
		entry.EnqueuedAt = time.Unix(enqueuedAtUnix, 0)
		if deliveredAtUnix.Valid {
			t := time.Unix(deliveredAtUnix.Int64, 0)
			entry.DeliveredAt = &t
		}
		if ackAtUnix.Valid {
			t := time.Unix(ackAtUnix.Int64, 0)
			entry.AckAt = &t
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// Close shuts down the persistent queue gracefully.
func (pq *PersistentQueue) Close() error {
	pq.cancel()

	// Wait for retry loop to finish (with timeout)
	done := make(chan struct{})
	go func() {
		pq.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return pq.db.Close()
	case <-time.After(30 * time.Second):
		pq.db.Close()
		return fmt.Errorf("queue close timeout: retry loop did not finish within 30s")
	}
}

// Acknowledge marks an interrupt as acknowledged (delivery confirmed by handler).
// This provides confirmation that the handler successfully processed the interrupt.
// Use this after the handler completes its work to transition from 'delivered' to 'acknowledged'.
func (pq *PersistentQueue) Acknowledge(ctx context.Context, id int64) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	now := time.Now().Unix()
	query := `UPDATE interrupt_queue SET state = 'acknowledged', ack_at = ? WHERE id = ?`
	_, err := pq.db.ExecContext(ctx, query, now, id)
	if err != nil {
		return fmt.Errorf("failed to acknowledge interrupt %d: %w", id, err)
	}

	return nil
}

// ClearOld removes old acknowledged interrupts from the queue (cleanup).
func (pq *PersistentQueue) ClearOld(ctx context.Context, olderThan time.Duration) (int, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	cutoff := time.Now().Add(-olderThan).Unix()
	query := `DELETE FROM interrupt_queue WHERE state = 'acknowledged' AND ack_at < ?`
	result, err := pq.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to clear old interrupts: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return int(count), nil
}
