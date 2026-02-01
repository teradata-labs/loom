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
package communication

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Hawk span constants for queue operations
const (
	SpanQueueEnqueue        = "queue.enqueue"
	SpanQueueDequeue        = "queue.dequeue"
	SpanQueueAck            = "queue.ack"
	SpanQueueRequeue        = "queue.requeue"
	SpanQueuePersist        = "queue.persist"
	SpanQueueRecover        = "queue.recover"
	SpanQueueSendAndReceive = "queue.send_and_receive"
)

// Default queue configuration values
const (
	// DefaultMaxRetries is the default maximum number of retry attempts for a message
	DefaultMaxRetries = 3
	// DefaultMessageTTL is the default time-to-live for messages (24 hours)
	DefaultMessageTTL = 24 * time.Hour
)

// QueueMessage represents a message in the queue.
type QueueMessage struct {
	ID            string
	ToAgent       string
	FromAgent     string
	MessageType   string
	Payload       *loomv1.MessagePayload
	Metadata      map[string]string
	CorrelationID string // For request-response correlation
	Priority      int32
	EnqueuedAt    time.Time
	ExpiresAt     time.Time
	DequeueCount  int32
	MaxRetries    int32
	Status        QueueMessageStatus
}

// QueueMessageStatus represents the state of a queued message.
type QueueMessageStatus int32

const (
	QueueMessageStatusPending QueueMessageStatus = iota
	QueueMessageStatusInFlight
	QueueMessageStatusAcked
	QueueMessageStatusFailed
	QueueMessageStatusExpired
)

// MessageQueue provides persistent message queuing for offline agents.
// All operations are safe for concurrent use.
type MessageQueue struct {
	mu sync.RWMutex

	// Per-agent queues (agent ID → queue of messages)
	queues map[string][]*QueueMessage

	// In-flight messages (message ID → message) for acknowledgment tracking
	inFlight map[string]*QueueMessage

	// Response waiting (correlation ID → response channel) for request-response pattern
	pendingResponses map[string]chan *QueueMessage

	// Event-driven notifications (agent ID → notification channel)
	notificationChannels map[string]chan struct{}

	// Agent validation (optional function to check if agent exists)
	agentValidator func(agentID string) bool

	// Persistent storage
	db     *sql.DB
	dbPath string

	// Dependencies
	tracer observability.Tracer
	logger *zap.Logger

	// Statistics (atomic counters)
	totalEnqueued atomic.Int64
	totalDequeued atomic.Int64
	totalAcked    atomic.Int64
	totalFailed   atomic.Int64
	totalExpired  atomic.Int64

	// Lifecycle
	closed atomic.Bool
}

// NewMessageQueue creates a new message queue with SQLite persistence.
func NewMessageQueue(dbPath string, tracer observability.Tracer, logger *zap.Logger) (*MessageQueue, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Open SQLite database
	// Add pragmas for better concurrency:
	// - busy_timeout: Wait up to 5s if database is locked
	// - journal_mode=WAL: Write-Ahead Logging for concurrent reads/writes
	// - cache_size: Increase cache for better performance
	dbURL := dbPath
	if dbPath == ":memory:" {
		// For in-memory databases with shared cache, use file URI format
		// This allows multiple connections to share the same in-memory database
		dbURL = "file::memory:?mode=memory&cache=shared&_busy_timeout=5000"
	}
	db, err := sql.Open("sqlite3", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool parameters for better concurrency
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	// Enable WAL mode for better concurrent access (for file-based databases)
	if dbPath != ":memory:" {
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			logger.Warn("Failed to enable WAL mode", zap.Error(err))
			// Continue anyway - not critical
		}
	}

	// Set busy timeout for all connections
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		logger.Warn("Failed to set busy timeout", zap.Error(err))
		// Continue anyway - not critical
	}

	// Create tables
	schema := `
	CREATE TABLE IF NOT EXISTS message_queue (
		id TEXT PRIMARY KEY,
		to_agent TEXT NOT NULL,
		from_agent TEXT NOT NULL,
		message_type TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		metadata_json TEXT,
		correlation_id TEXT,
		priority INTEGER DEFAULT 0,
		enqueued_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		dequeue_count INTEGER DEFAULT 0,
		max_retries INTEGER DEFAULT 3,
		status INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_to_agent ON message_queue(to_agent, status);
	CREATE INDEX IF NOT EXISTS idx_status ON message_queue(status);
	CREATE INDEX IF NOT EXISTS idx_expires_at ON message_queue(expires_at);
	CREATE INDEX IF NOT EXISTS idx_correlation_id ON message_queue(correlation_id);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	q := &MessageQueue{
		queues:               make(map[string][]*QueueMessage),
		inFlight:             make(map[string]*QueueMessage),
		pendingResponses:     make(map[string]chan *QueueMessage),
		notificationChannels: make(map[string]chan struct{}),
		db:                   db,
		dbPath:               dbPath,
		tracer:               tracer,
		logger:               logger,
	}

	// Recover in-flight messages from database
	if err := q.recoverFromDatabase(context.Background()); err != nil {
		logger.Warn("Failed to recover messages from database", zap.Error(err))
		// Continue anyway - recovery is best-effort
	}

	return q, nil
}

// Enqueue adds a message to an agent's queue.
// If the agent is offline, the message is persisted to SQLite.
func (q *MessageQueue) Enqueue(ctx context.Context, msg *QueueMessage) error {
	if q.closed.Load() {
		return fmt.Errorf("message queue is closed")
	}

	if msg.ToAgent == "" {
		return fmt.Errorf("to_agent cannot be empty")
	}

	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueEnqueue)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("to_agent", msg.ToAgent)
		span.SetAttribute("from_agent", msg.FromAgent)
		span.SetAttribute("message_id", msg.ID)
		span.SetAttribute("priority", msg.Priority)
	}

	start := time.Now()

	// Set defaults
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("qmsg-%d", time.Now().UnixNano())
	}
	if msg.EnqueuedAt.IsZero() {
		msg.EnqueuedAt = time.Now()
	}
	if msg.ExpiresAt.IsZero() {
		msg.ExpiresAt = msg.EnqueuedAt.Add(DefaultMessageTTL)
	}
	if msg.MaxRetries == 0 {
		msg.MaxRetries = DefaultMaxRetries
	}
	msg.Status = QueueMessageStatusPending

	// Check if this is a response message (has correlation ID)
	// If so, route it to the waiting request instead of queuing
	// IMPORTANT: Only route if this is a RESPONSE, not a REQUEST
	// Correlation IDs are formatted as "corr-{fromAgent}-{timestamp}"
	// Responses have ToAgent matching the original fromAgent
	if msg.CorrelationID != "" {
		q.mu.RLock()
		responseChan, exists := q.pendingResponses[msg.CorrelationID]
		q.mu.RUnlock()

		if exists {
			// Extract fromAgent from correlation ID to verify this is a response
			// Format: "corr-{fromAgent}-{timestamp}"
			correlationParts := strings.SplitN(msg.CorrelationID, "-", 3)
			if len(correlationParts) >= 2 {
				requestFromAgent := correlationParts[1]

				// Only route if ToAgent matches the original requester
				// This prevents request messages from being routed back to themselves
				if msg.ToAgent == requestFromAgent {
					// This is a response to a waiting request - send it on the channel
					select {
					case responseChan <- msg:
						q.logger.Debug("response routed to waiting request",
							zap.String("correlation_id", msg.CorrelationID),
							zap.String("from_agent", msg.FromAgent),
							zap.String("to_agent", msg.ToAgent))
						return nil // Don't persist or queue - response delivered
					default:
						// Channel buffer full or closed - fall through to normal queuing
						q.logger.Warn("response channel full or closed, falling back to queue",
							zap.String("correlation_id", msg.CorrelationID))
					}
				}
			}
		}
		// No waiting request found or not a valid response - fall through to normal queuing
		// (Request may have timed out already, or this is a request message)
	}

	// Persist to database
	if err := q.persistMessage(ctx, msg); err != nil {
		return fmt.Errorf("failed to persist message: %w", err)
	}

	// Add to in-memory queue
	q.mu.Lock()
	q.queues[msg.ToAgent] = append(q.queues[msg.ToAgent], msg)
	notifyChan, hasNotifications := q.notificationChannels[msg.ToAgent]
	q.mu.Unlock()

	// Notify the target agent immediately if registered for event-driven notifications
	if hasNotifications {
		// Non-blocking send to avoid blocking enqueue
		select {
		case notifyChan <- struct{}{}:
			q.logger.Info("IMMEDIATE NOTIFICATION: notified agent of new message",
				zap.String("agent_id", msg.ToAgent),
				zap.String("message_id", msg.ID))
		default:
			// Channel full, agent already has pending notification
			q.logger.Info("IMMEDIATE NOTIFICATION: channel full, agent already notified",
				zap.String("agent_id", msg.ToAgent))
		}
	} else {
		q.logger.Warn("NO NOTIFICATION CHANNEL registered for agent",
			zap.String("agent_id", msg.ToAgent),
			zap.String("message_id", msg.ID))
	}

	q.totalEnqueued.Add(1)

	latency := time.Since(start)
	if span != nil {
		span.SetAttribute("latency_us", latency.Microseconds())
	}

	q.logger.Debug("message enqueued",
		zap.String("message_id", msg.ID),
		zap.String("to_agent", msg.ToAgent),
		zap.String("from_agent", msg.FromAgent),
		zap.Duration("latency", latency))

	return nil
}

// Dequeue retrieves the next message for an agent.
// Messages are marked as in-flight and must be acknowledged or will be requeued.
func (q *MessageQueue) Dequeue(ctx context.Context, agentID string) (*QueueMessage, error) {
	if q.closed.Load() {
		return nil, fmt.Errorf("message queue is closed")
	}

	if agentID == "" {
		return nil, fmt.Errorf("agent ID cannot be empty")
	}

	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueDequeue)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("agent_id", agentID)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Get agent's queue
	queue, exists := q.queues[agentID]
	if !exists || len(queue) == 0 {
		return nil, nil // No messages
	}

	// Find next pending message (highest priority first)
	var msg *QueueMessage
	highestPriority := int32(-9999)

	for _, m := range queue {
		if m.Status != QueueMessageStatusPending {
			continue
		}

		// Check expiration
		if time.Now().After(m.ExpiresAt) {
			m.Status = QueueMessageStatusExpired
			q.totalExpired.Add(1)
			if err := q.updateMessageStatus(ctx, m.ID, QueueMessageStatusExpired); err != nil {
				q.logger.Warn("Failed to update expired message status", zap.Error(err))
			}
			continue
		}

		// Check retry limit
		if m.DequeueCount >= m.MaxRetries {
			m.Status = QueueMessageStatusFailed
			q.totalFailed.Add(1)
			if err := q.updateMessageStatus(ctx, m.ID, QueueMessageStatusFailed); err != nil {
				q.logger.Warn("Failed to update failed message status", zap.Error(err))
			}
			continue
		}

		if m.Priority > highestPriority {
			highestPriority = m.Priority
			msg = m
		}
	}

	if msg == nil {
		return nil, nil // No pending messages
	}

	// Mark as in-flight
	msg.Status = QueueMessageStatusInFlight
	msg.DequeueCount++
	q.inFlight[msg.ID] = msg

	// Update database with both status and dequeue count
	now := time.Now().UnixMilli()
	_, err := q.db.ExecContext(ctx, `
		UPDATE message_queue
		SET status = ?, dequeue_count = ?, updated_at = ?
		WHERE id = ?
	`, QueueMessageStatusInFlight, msg.DequeueCount, now, msg.ID)
	if err != nil {
		q.logger.Warn("Failed to update message status and dequeue count in database", zap.Error(err))
	}

	q.totalDequeued.Add(1)

	if span != nil {
		span.SetAttribute("message_id", msg.ID)
		span.SetAttribute("priority", msg.Priority)
		span.SetAttribute("dequeue_count", msg.DequeueCount)
	}

	q.logger.Debug("message dequeued",
		zap.String("message_id", msg.ID),
		zap.String("agent_id", agentID),
		zap.Int32("priority", msg.Priority))

	return msg, nil
}

// Acknowledge marks a message as successfully processed.
func (q *MessageQueue) Acknowledge(ctx context.Context, messageID string) error {
	if q.closed.Load() {
		return fmt.Errorf("message queue is closed")
	}

	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueAck)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("message_id", messageID)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	msg, exists := q.inFlight[messageID]
	if !exists {
		return fmt.Errorf("message not in-flight: %s", messageID)
	}

	// Mark as acknowledged
	msg.Status = QueueMessageStatusAcked
	delete(q.inFlight, messageID)

	// Remove from agent's queue
	queue := q.queues[msg.ToAgent]
	for i, m := range queue {
		if m.ID == messageID {
			q.queues[msg.ToAgent] = append(queue[:i], queue[i+1:]...)
			break
		}
	}

	// Update database
	if err := q.updateMessageStatus(ctx, messageID, QueueMessageStatusAcked); err != nil {
		q.logger.Warn("Failed to update message status in database", zap.Error(err))
	}

	q.totalAcked.Add(1)

	q.logger.Debug("message acknowledged",
		zap.String("message_id", messageID),
		zap.String("agent_id", msg.ToAgent))

	return nil
}

// Requeue returns an in-flight message back to pending state for retry.
func (q *MessageQueue) Requeue(ctx context.Context, messageID string) error {
	if q.closed.Load() {
		return fmt.Errorf("message queue is closed")
	}

	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueRequeue)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("message_id", messageID)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	msg, exists := q.inFlight[messageID]
	if !exists {
		return fmt.Errorf("message not in-flight: %s", messageID)
	}

	// Check retry limit
	if msg.DequeueCount >= msg.MaxRetries {
		msg.Status = QueueMessageStatusFailed
		q.totalFailed.Add(1)
		if err := q.updateMessageStatus(ctx, messageID, QueueMessageStatusFailed); err != nil {
			q.logger.Warn("Failed to update failed message status", zap.Error(err))
		}
		delete(q.inFlight, messageID)
		return fmt.Errorf("message exceeded max retries")
	}

	// Return to pending
	msg.Status = QueueMessageStatusPending
	delete(q.inFlight, messageID)

	// Update database
	if err := q.updateMessageStatus(ctx, messageID, QueueMessageStatusPending); err != nil {
		q.logger.Warn("Failed to update message status in database", zap.Error(err))
	}

	q.logger.Debug("message requeued",
		zap.String("message_id", messageID),
		zap.Int32("dequeue_count", msg.DequeueCount))

	return nil
}

// GetQueueDepth returns the number of pending messages for an agent.
func (q *MessageQueue) GetQueueDepth(agentID string) int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	queue, exists := q.queues[agentID]
	if !exists {
		return 0
	}

	pending := 0
	for _, msg := range queue {
		if msg.Status == QueueMessageStatusPending {
			pending++
		}
	}

	return pending
}

// Close closes the message queue and database connection.
func (q *MessageQueue) Close() error {
	if !q.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	q.logger.Info("message queue closing",
		zap.Int64("total_enqueued", q.totalEnqueued.Load()),
		zap.Int64("total_dequeued", q.totalDequeued.Load()),
		zap.Int64("total_acked", q.totalAcked.Load()),
		zap.Int64("total_failed", q.totalFailed.Load()))

	if q.db != nil {
		return q.db.Close()
	}

	return nil
}

// persistMessage saves a message to SQLite.
func (q *MessageQueue) persistMessage(ctx context.Context, msg *QueueMessage) error {
	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueuePersist)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("message_id", msg.ID)
	}

	// Marshal payload using protojson (handles oneof fields correctly)
	payloadJSON, err := protojson.Marshal(msg.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	now := time.Now().UnixMilli()

	_, err = q.db.ExecContext(ctx, `
		INSERT INTO message_queue (
			id, to_agent, from_agent, message_type,
			payload_json, metadata_json, correlation_id, priority,
			enqueued_at, expires_at, dequeue_count,
			max_retries, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			dequeue_count = excluded.dequeue_count,
			updated_at = excluded.updated_at
	`,
		msg.ID, msg.ToAgent, msg.FromAgent, msg.MessageType,
		payloadJSON, metadataJSON, msg.CorrelationID, msg.Priority,
		msg.EnqueuedAt.UnixMilli(), msg.ExpiresAt.UnixMilli(), msg.DequeueCount,
		msg.MaxRetries, msg.Status, now, now)

	return err
}

// updateMessageStatus updates the status of a message in SQLite.
func (q *MessageQueue) updateMessageStatus(ctx context.Context, messageID string, status QueueMessageStatus) error {
	now := time.Now().UnixMilli()
	_, err := q.db.ExecContext(ctx, `
		UPDATE message_queue
		SET status = ?, updated_at = ?
		WHERE id = ?
	`, status, now, messageID)
	return err
}

// recoverFromDatabase loads in-flight messages from SQLite on startup.
func (q *MessageQueue) recoverFromDatabase(ctx context.Context) error {
	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueRecover)
		defer q.tracer.EndSpan(span)
	}

	rows, err := q.db.QueryContext(ctx, `
		SELECT id, to_agent, from_agent, message_type,
		       payload_json, metadata_json, correlation_id, priority,
		       enqueued_at, expires_at, dequeue_count,
		       max_retries, status
		FROM message_queue
		WHERE status IN (?, ?)
	`, QueueMessageStatusPending, QueueMessageStatusInFlight)

	if err != nil {
		return fmt.Errorf("failed to query database: %w", err)
	}
	defer rows.Close()

	recovered := 0
	for rows.Next() {
		var msg QueueMessage
		var payloadJSON, metadataJSON string
		var enqueuedAt, expiresAt int64
		var correlationID sql.NullString

		err := rows.Scan(
			&msg.ID, &msg.ToAgent, &msg.FromAgent, &msg.MessageType,
			&payloadJSON, &metadataJSON, &correlationID, &msg.Priority,
			&enqueuedAt, &expiresAt, &msg.DequeueCount,
			&msg.MaxRetries, &msg.Status)

		if correlationID.Valid {
			msg.CorrelationID = correlationID.String
		}

		if err != nil {
			q.logger.Warn("Failed to scan message row", zap.Error(err))
			continue
		}

		// Unmarshal payload using protojson (handles oneof fields correctly)
		var payload loomv1.MessagePayload
		if err := protojson.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			q.logger.Warn("Failed to unmarshal payload", zap.String("message_id", msg.ID), zap.Error(err))
			continue
		}
		msg.Payload = &payload

		if err := json.Unmarshal([]byte(metadataJSON), &msg.Metadata); err != nil {
			q.logger.Warn("Failed to unmarshal metadata", zap.String("message_id", msg.ID), zap.Error(err))
			msg.Metadata = make(map[string]string)
		}

		msg.EnqueuedAt = time.UnixMilli(enqueuedAt)
		msg.ExpiresAt = time.UnixMilli(expiresAt)

		// Requeue in-flight messages as pending
		if msg.Status == QueueMessageStatusInFlight {
			msg.Status = QueueMessageStatusPending
			if err := q.updateMessageStatus(ctx, msg.ID, QueueMessageStatusPending); err != nil {
				q.logger.Warn("Failed to update message status during recovery", zap.Error(err))
			}
		}

		// Add to in-memory queue
		q.queues[msg.ToAgent] = append(q.queues[msg.ToAgent], &msg)
		recovered++
	}

	if span != nil {
		span.SetAttribute("recovered_count", recovered)
	}

	q.logger.Info("recovered messages from database",
		zap.Int("count", recovered))

	return rows.Err()
}

// DefaultQueueTimeout is the default timeout for request-response operations (30 seconds).
const DefaultQueueTimeout = 30

// Send is a convenience wrapper around Enqueue for fire-and-forget messaging.
// It creates a QueueMessage and enqueues it for the destination agent.
func (q *MessageQueue) Send(ctx context.Context, fromAgent, toAgent, messageType string, payload *loomv1.MessagePayload, metadata map[string]string) (string, error) {
	msg := &QueueMessage{
		ID:          fmt.Sprintf("%s-%d", toAgent, time.Now().UnixNano()),
		ToAgent:     toAgent,
		FromAgent:   fromAgent,
		MessageType: messageType,
		Payload:     payload,
		Metadata:    metadata,
		Priority:    0,
		EnqueuedAt:  time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour), // Default 24h expiry
		MaxRetries:  3,
		Status:      QueueMessageStatusPending,
	}

	err := q.Enqueue(ctx, msg)
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// SendAndReceive implements request-response messaging with timeout.
// It sends a request and waits for a response with the specified timeout in seconds.
//
// The correlation ID is used to match the response to this specific request.
// When the destination agent sends a response with the same correlation ID,
// it will be routed to this waiting channel.
func (q *MessageQueue) SendAndReceive(ctx context.Context, fromAgent, toAgent, messageType string, payload *loomv1.MessagePayload, metadata map[string]string, timeoutSeconds int) (*loomv1.MessagePayload, error) {
	if q.closed.Load() {
		return nil, fmt.Errorf("message queue is closed")
	}

	// Instrument with Hawk
	var span *observability.Span
	if q.tracer != nil {
		ctx, span = q.tracer.StartSpan(ctx, SpanQueueSendAndReceive)
		defer q.tracer.EndSpan(span)
		span.SetAttribute("from_agent", fromAgent)
		span.SetAttribute("to_agent", toAgent)
		span.SetAttribute("message_type", messageType)
		span.SetAttribute("timeout_seconds", timeoutSeconds)
	}

	start := time.Now()

	// Generate unique correlation ID
	correlationID := fmt.Sprintf("corr-%s-%d", fromAgent, time.Now().UnixNano())
	if span != nil {
		span.SetAttribute("correlation_id", correlationID)
	}

	// Create buffered response channel (size 1 so response doesn't block)
	responseChan := make(chan *QueueMessage, 1)

	// Register response channel
	q.mu.Lock()
	q.pendingResponses[correlationID] = responseChan
	q.mu.Unlock()

	// Cleanup registration on return (whether success, timeout, or error)
	defer func() {
		q.mu.Lock()
		delete(q.pendingResponses, correlationID)
		close(responseChan)
		q.mu.Unlock()
	}()

	// Send request message with correlation ID
	msg := &QueueMessage{
		ID:            fmt.Sprintf("%s-req-%d", toAgent, time.Now().UnixNano()),
		ToAgent:       toAgent,
		FromAgent:     fromAgent,
		MessageType:   messageType,
		Payload:       payload,
		Metadata:      metadata,
		CorrelationID: correlationID,
		Priority:      0,
		EnqueuedAt:    time.Now(),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		MaxRetries:    3,
		Status:        QueueMessageStatusPending,
	}

	if err := q.Enqueue(ctx, msg); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	q.logger.Debug("request sent, waiting for response",
		zap.String("correlation_id", correlationID),
		zap.String("from_agent", fromAgent),
		zap.String("to_agent", toAgent),
		zap.Int("timeout_seconds", timeoutSeconds))

	// Wait for response with timeout
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeoutSeconds == 0 {
		timeout = 30 * time.Second // Default 30s timeout
	}

	select {
	case responseMsg := <-responseChan:
		latency := time.Since(start)
		if span != nil {
			span.SetAttribute("success", true)
			span.SetAttribute("latency_ms", latency.Milliseconds())
		}
		q.logger.Debug("response received",
			zap.String("correlation_id", correlationID),
			zap.String("from_agent", responseMsg.FromAgent),
			zap.Duration("latency", latency))
		return responseMsg.Payload, nil

	case <-time.After(timeout):
		if span != nil {
			span.SetAttribute("success", false)
			span.SetAttribute("timeout", true)
		}
		q.logger.Warn("request-response timeout",
			zap.String("correlation_id", correlationID),
			zap.String("from_agent", fromAgent),
			zap.String("to_agent", toAgent),
			zap.Int("timeout_seconds", timeoutSeconds))
		return nil, fmt.Errorf("request-response timeout after %ds", timeoutSeconds)

	case <-ctx.Done():
		if span != nil {
			span.SetAttribute("success", false)
			span.SetAttribute("canceled", true)
		}
		return nil, fmt.Errorf("request-response canceled: %w", ctx.Err())
	}
}

// GetAgentsWithPendingMessages returns a list of agent IDs that have pending messages.
// This is used by the message queue monitor to trigger agents event-driven instead of polling.
func (q *MessageQueue) GetAgentsWithPendingMessages(ctx context.Context) []string {
	if q.closed.Load() {
		return nil
	}

	q.mu.RLock()
	defer q.mu.RUnlock()

	var agents []string
	for agentID, queue := range q.queues {
		// Check if agent has any pending messages
		hasPending := false
		for _, msg := range queue {
			if msg.Status == QueueMessageStatusPending && time.Now().Before(msg.ExpiresAt) {
				hasPending = true
				break
			}
		}
		if hasPending {
			agents = append(agents, agentID)
		}
	}

	return agents
}

// RegisterNotificationChannel registers a notification channel for event-driven message handling.
// When messages arrive for this agent, the channel will be notified.
func (q *MessageQueue) RegisterNotificationChannel(agentID string, notifyChan chan struct{}) {
	if q.closed.Load() {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.notificationChannels[agentID] = notifyChan
	q.logger.Info("registered notification channel for agent",
		zap.String("agent_id", agentID))
}

// UnregisterNotificationChannel removes a notification channel for an agent.
func (q *MessageQueue) UnregisterNotificationChannel(agentID string) {
	if q.closed.Load() {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.notificationChannels, agentID)
}

// GetNotificationChannel returns the notification channel for an agent, if registered.
func (q *MessageQueue) GetNotificationChannel(agentID string) (chan struct{}, bool) {
	if q.closed.Load() {
		return nil, false
	}

	q.mu.RLock()
	defer q.mu.RUnlock()

	ch, exists := q.notificationChannels[agentID]
	return ch, exists
}

// SetAgentValidator sets the function used to validate if an agent exists.
// This is used by send_message to prevent messages being sent to non-existent agents.
func (q *MessageQueue) SetAgentValidator(validator func(agentID string) bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.agentValidator = validator
}

// AgentExists checks if an agent exists using the registered validator.
// If no validator is set, returns true (permissive - allows all agents).
func (q *MessageQueue) AgentExists(agentID string) bool {
	q.mu.RLock()
	validator := q.agentValidator
	q.mu.RUnlock()

	if validator == nil {
		// No validator set - permissive mode (allow all agents)
		return true
	}

	return validator(agentID)
}

// PendingMessageInfo contains metadata about a pending message without the full payload.
type PendingMessageInfo struct {
	MessageID   string
	FromAgent   string
	MessageType string
	EnqueuedAt  time.Time
	SizeBytes   int
}

// GetPendingMessagesInfo returns info about pending messages for an agent without dequeuing them.
// This is used for rich event notifications to tell the coordinator exactly what's waiting.
func (q *MessageQueue) GetPendingMessagesInfo(agentID string) []PendingMessageInfo {
	if q.closed.Load() {
		return nil
	}

	q.mu.RLock()
	defer q.mu.RUnlock()

	queue, exists := q.queues[agentID]
	if !exists {
		return nil
	}

	var infos []PendingMessageInfo
	for _, msg := range queue {
		if msg.Status == QueueMessageStatusPending && time.Now().Before(msg.ExpiresAt) {
			sizeBytes := 0
			if msg.Payload != nil && msg.Payload.GetValue() != nil {
				sizeBytes = len(msg.Payload.GetValue())
			}
			infos = append(infos, PendingMessageInfo{
				MessageID:   msg.ID,
				FromAgent:   msg.FromAgent,
				MessageType: msg.MessageType,
				EnqueuedAt:  msg.EnqueuedAt,
				SizeBytes:   sizeBytes,
			})
		}
	}

	return infos
}
