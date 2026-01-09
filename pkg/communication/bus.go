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
	"fmt"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Hawk span constants for bus operations
const (
	SpanBusPublish     = "bus.publish"
	SpanBusSubscribe   = "bus.subscribe"
	SpanBusDeliver     = "bus.deliver"
	SpanBusFilter      = "bus.filter"
	SpanBusUnsubscribe = "bus.unsubscribe"
)

// Default configuration values
const (
	// DefaultMessageBufferSize is the default buffer size for message channels
	DefaultMessageBufferSize = 100
)

// MessageBus provides topic-based pub/sub for agent communication.
// All operations are safe for concurrent use by multiple goroutines.
type MessageBus struct {
	mu sync.RWMutex

	// Topic name → broadcaster
	topics map[string]*TopicBroadcaster

	// Subscriber ID → subscription (for unsubscribe)
	subscriptions map[string]*Subscription

	// Dependencies
	refStore ReferenceStore
	policy   *PolicyManager
	tracer   observability.Tracer
	logger   *zap.Logger

	// Metrics (atomic counters)
	totalPublished atomic.Int64
	totalDelivered atomic.Int64
	totalDropped   atomic.Int64

	// Lifecycle
	closed atomic.Bool
}

// TopicBroadcaster manages subscribers for a single topic.
// Safe for concurrent use.
type TopicBroadcaster struct {
	mu    sync.RWMutex
	topic string

	// Subscriber ID → subscriber
	subscribers map[string]*Subscriber

	// Statistics (atomic for concurrent access)
	totalPublished atomic.Int64
	totalDelivered atomic.Int64
	totalDropped   atomic.Int64
	createdAt      time.Time
	lastPublishAt  atomic.Value // time.Time
}

// Subscriber represents an agent subscribed to a topic.
type Subscriber struct {
	id      string
	agentID string
	filter  *loomv1.SubscriptionFilter
	channel chan *loomv1.BusMessage
	created time.Time
}

// Subscription represents an active subscription.
// Returned to caller to receive messages.
type Subscription struct {
	ID            string
	AgentID       string
	Topic         string
	Filter        *loomv1.SubscriptionFilter // Filter for this subscription
	Channel       <-chan *loomv1.BusMessage  // Receive-only for external consumers
	channel       chan *loomv1.BusMessage    // Internal writable reference
	notifyChannel chan struct{}              // For event-driven notifications (internal)
	Created       time.Time
}

// NewMessageBus creates a new message bus.
func NewMessageBus(refStore ReferenceStore, policy *PolicyManager, tracer observability.Tracer, logger *zap.Logger) *MessageBus {
	if policy == nil {
		policy = NewPolicyManager()
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	return &MessageBus{
		topics:        make(map[string]*TopicBroadcaster),
		subscriptions: make(map[string]*Subscription),
		refStore:      refStore,
		policy:        policy,
		tracer:        tracer,
		logger:        logger,
	}
}

// Publish sends a message to all subscribers of a topic.
// Returns (delivered, dropped, error).
// Does NOT block on slow subscribers - messages are dropped if subscriber buffers are full.
func (b *MessageBus) Publish(ctx context.Context, topic string, msg *loomv1.BusMessage) (int, int, error) {
	if b.closed.Load() {
		return 0, 0, fmt.Errorf("message bus is closed")
	}

	if topic == "" {
		return 0, 0, fmt.Errorf("topic cannot be empty")
	}
	if msg == nil {
		return 0, 0, fmt.Errorf("message cannot be nil")
	}

	// Instrument with Hawk
	var span *observability.Span
	if b.tracer != nil {
		_, span = b.tracer.StartSpan(ctx, SpanBusPublish)
		defer b.tracer.EndSpan(span)
		span.SetAttribute("topic", topic)
		span.SetAttribute("from_agent", msg.FromAgent)
		span.SetAttribute("message_id", msg.Id)
	}

	start := time.Now()

	// Broadcast to pattern-matched and filtered subscribers
	delivered := 0
	dropped := 0

	b.mu.RLock()
	for _, subscription := range b.subscriptions {
		// Check if topic matches subscription pattern
		if !matchesTopicPattern(subscription.Topic, topic) {
			continue
		}

		// Check if message matches subscription filter
		if !matchesFilter(subscription.Filter, msg) {
			continue
		}

		// Try to deliver message (non-blocking)
		select {
		case subscription.channel <- msg:
			delivered++
			// Send event-driven notification if registered
			if subscription.notifyChannel != nil {
				select {
				case subscription.notifyChannel <- struct{}{}:
					// Notification sent
				default:
					// Notification channel full, agent already has pending notification
				}
			}
		default:
			// Channel full - drop message to avoid blocking publisher
			dropped++
		}
	}
	b.mu.RUnlock()

	// Update MessageBus metrics
	b.totalPublished.Add(1)
	b.totalDelivered.Add(int64(delivered))
	b.totalDropped.Add(int64(dropped))

	// Update TopicBroadcaster metrics
	broadcaster := b.getOrCreateTopic(topic)
	broadcaster.totalPublished.Add(1)
	broadcaster.totalDelivered.Add(int64(delivered))
	broadcaster.totalDropped.Add(int64(dropped))
	broadcaster.lastPublishAt.Store(time.Now())

	latency := time.Since(start)

	// Log and trace
	if span != nil {
		span.SetAttribute("delivered", delivered)
		span.SetAttribute("dropped", dropped)
		span.SetAttribute("latency_us", latency.Microseconds())
	}

	b.logger.Debug("bus publish",
		zap.String("topic", topic),
		zap.String("from_agent", msg.FromAgent),
		zap.String("message_id", msg.Id),
		zap.Int("delivered", delivered),
		zap.Int("dropped", dropped),
		zap.Duration("latency", latency))

	return delivered, dropped, nil
}

// Subscribe creates a new subscription to a topic pattern.
// Topic patterns support wildcards: "workflow.*" matches "workflow.started", "workflow.completed"
// Returns a Subscription that contains a channel for receiving messages.
func (b *MessageBus) Subscribe(ctx context.Context, agentID string, topicPattern string, filter *loomv1.SubscriptionFilter, bufferSize int) (*Subscription, error) {
	if b.closed.Load() {
		return nil, fmt.Errorf("message bus is closed")
	}

	if agentID == "" {
		return nil, fmt.Errorf("agent ID cannot be empty")
	}
	if topicPattern == "" {
		return nil, fmt.Errorf("topic pattern cannot be empty")
	}

	if bufferSize <= 0 {
		bufferSize = DefaultMessageBufferSize
	}

	// Instrument with Hawk
	var span *observability.Span
	if b.tracer != nil {
		_, span = b.tracer.StartSpan(ctx, SpanBusSubscribe)
		defer b.tracer.EndSpan(span)
		span.SetAttribute("agent_id", agentID)
		span.SetAttribute("topic_pattern", topicPattern)
		span.SetAttribute("buffer_size", bufferSize)
	}

	// Create subscriber
	subID := fmt.Sprintf("%s-%s-%d", agentID, topicPattern, time.Now().UnixNano())
	channel := make(chan *loomv1.BusMessage, bufferSize)

	subscriber := &Subscriber{
		id:      subID,
		agentID: agentID,
		filter:  filter,
		channel: channel,
		created: time.Now(),
	}

	// Add to topic (handles wildcards internally)
	broadcaster := b.getOrCreateTopic(topicPattern)
	broadcaster.addSubscriber(subscriber)

	// Create subscription handle
	subscription := &Subscription{
		ID:      subID,
		AgentID: agentID,
		Topic:   topicPattern,
		Filter:  filter,  // Store filter for message filtering
		Channel: channel, // Read-only view for external consumers
		channel: channel, // Writable reference for internal publish
		Created: subscriber.created,
	}

	// Store subscription for later unsubscribe
	b.mu.Lock()
	b.subscriptions[subID] = subscription
	b.mu.Unlock()

	b.logger.Info("bus subscribe",
		zap.String("subscription_id", subID),
		zap.String("agent_id", agentID),
		zap.String("topic_pattern", topicPattern),
		zap.Int("buffer_size", bufferSize))

	return subscription, nil
}

// Unsubscribe removes a subscription.
// The subscription's channel will be closed.
func (b *MessageBus) Unsubscribe(ctx context.Context, subscriptionID string) error {
	if subscriptionID == "" {
		return fmt.Errorf("subscription ID cannot be empty")
	}

	// Instrument with Hawk
	var span *observability.Span
	if b.tracer != nil {
		_, span = b.tracer.StartSpan(ctx, SpanBusUnsubscribe)
		defer b.tracer.EndSpan(span)
		span.SetAttribute("subscription_id", subscriptionID)
	}

	b.mu.Lock()
	subscription, found := b.subscriptions[subscriptionID]
	if !found {
		b.mu.Unlock()
		return fmt.Errorf("subscription not found: %s", subscriptionID)
	}
	delete(b.subscriptions, subscriptionID)
	b.mu.Unlock()

	// Remove from topic broadcaster
	broadcaster := b.getTopic(subscription.Topic)
	if broadcaster != nil {
		broadcaster.removeSubscriber(subscriptionID)
	}

	b.logger.Info("bus unsubscribe",
		zap.String("subscription_id", subscriptionID),
		zap.String("agent_id", subscription.AgentID),
		zap.String("topic", subscription.Topic))

	return nil
}

// ListTopics returns all active topics.
func (b *MessageBus) ListTopics(ctx context.Context) ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	topics := make([]string, 0, len(b.topics))
	for topic := range b.topics {
		topics = append(topics, topic)
	}

	return topics, nil
}

// GetTopicStats retrieves statistics for a topic.
func (b *MessageBus) GetTopicStats(ctx context.Context, topic string) (*loomv1.TopicStats, error) {
	broadcaster := b.getTopic(topic)
	if broadcaster == nil {
		return nil, fmt.Errorf("topic not found: %s", topic)
	}

	return broadcaster.stats(), nil
}

// Close shuts down the message bus and closes all subscriber channels.
func (b *MessageBus) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Close all subscriber channels
	for _, broadcaster := range b.topics {
		broadcaster.closeAll()
	}

	b.logger.Info("message bus closed",
		zap.Int64("total_published", b.totalPublished.Load()),
		zap.Int64("total_delivered", b.totalDelivered.Load()),
		zap.Int64("total_dropped", b.totalDropped.Load()))

	return nil
}

// getOrCreateTopic gets or creates a topic broadcaster.
func (b *MessageBus) getOrCreateTopic(topic string) *TopicBroadcaster {
	b.mu.Lock()
	defer b.mu.Unlock()

	broadcaster, exists := b.topics[topic]
	if !exists {
		broadcaster = &TopicBroadcaster{
			topic:       topic,
			subscribers: make(map[string]*Subscriber),
			createdAt:   time.Now(),
		}
		b.topics[topic] = broadcaster
	}

	return broadcaster
}

// getTopic gets a topic broadcaster (or nil if not found).
func (b *MessageBus) getTopic(topic string) *TopicBroadcaster {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.topics[topic]
}

// TopicBroadcaster methods

// addSubscriber adds a subscriber to this topic.
func (tb *TopicBroadcaster) addSubscriber(sub *Subscriber) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.subscribers[sub.id] = sub
}

// removeSubscriber removes a subscriber and closes their channel.
func (tb *TopicBroadcaster) removeSubscriber(subID string) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if sub, found := tb.subscribers[subID]; found {
		close(sub.channel)
		delete(tb.subscribers, subID)
	}
}

// closeAll closes all subscriber channels.
func (tb *TopicBroadcaster) closeAll() {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	for _, sub := range tb.subscribers {
		close(sub.channel)
	}
	tb.subscribers = make(map[string]*Subscriber)
}

// stats returns statistics for this topic.
func (tb *TopicBroadcaster) stats() *loomv1.TopicStats {
	tb.mu.RLock()
	defer tb.mu.RUnlock()

	lastPublish := int64(0)
	if val := tb.lastPublishAt.Load(); val != nil {
		if t, ok := val.(time.Time); ok {
			lastPublish = t.UnixMilli()
		}
	}

	return &loomv1.TopicStats{
		Topic:             tb.topic,
		TotalPublished:    tb.totalPublished.Load(),
		TotalDelivered:    tb.totalDelivered.Load(),
		TotalDropped:      tb.totalDropped.Load(),
		ActiveSubscribers: int32(len(tb.subscribers)),
		CreatedAt:         tb.createdAt.UnixMilli(),
		LastPublishAt:     lastPublish,
	}
}

// Subscriber methods

// matchesTopicPattern checks if a topic matches a subscription pattern.
// Supports wildcards: "workflow.*" matches "workflow.started", "workflow.completed"
func matchesTopicPattern(pattern, topic string) bool {
	// Exact match
	if pattern == topic {
		return true
	}

	// Wildcard match using filepath.Match semantics
	matched, err := path.Match(pattern, topic)
	if err != nil {
		return false
	}
	return matched
}

// matchesFilter checks if a message matches a subscription filter.
// Returns true if the message passes all filter criteria.
func matchesFilter(filter *loomv1.SubscriptionFilter, msg *loomv1.BusMessage) bool {
	if filter == nil {
		return true // No filter = accept all
	}

	// Check FromAgents filter
	if len(filter.FromAgents) > 0 {
		found := false
		for _, agent := range filter.FromAgents {
			if agent == msg.FromAgent {
				found = true
				break
			}
		}
		if !found {
			return false // Message from agent not in allowed list
		}
	}

	// Check Metadata filter (all filter metadata must match message metadata)
	if len(filter.Metadata) > 0 {
		for key, value := range filter.Metadata {
			msgValue, exists := msg.Metadata[key]
			if !exists || msgValue != value {
				return false // Missing key or value mismatch
			}
		}
	}

	return true
}

// GetSubscriptionsByAgent returns all active subscriptions for an agent.
func (b *MessageBus) GetSubscriptionsByAgent(agentID string) []*Subscription {
	if b.closed.Load() {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	subscriptions := make([]*Subscription, 0)
	for _, sub := range b.subscriptions {
		if sub.AgentID == agentID {
			subscriptions = append(subscriptions, sub)
		}
	}

	return subscriptions
}

// RegisterNotificationChannel registers a notification channel for a subscription.
// When messages arrive on this subscription, the channel will be notified.
func (b *MessageBus) RegisterNotificationChannel(subscriptionID string, notifyChan chan struct{}) {
	if b.closed.Load() {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, exists := b.subscriptions[subscriptionID]; exists {
		sub.notifyChannel = notifyChan
	}
}

// GetNotificationChannel returns the notification channel for a subscription, if registered.
func (b *MessageBus) GetNotificationChannel(subscriptionID string) (chan struct{}, bool) {
	if b.closed.Load() {
		return nil, false
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	if sub, exists := b.subscriptions[subscriptionID]; exists && sub.notifyChannel != nil {
		return sub.notifyChannel, true
	}

	return nil, false
}

// UnregisterNotificationChannel removes a notification channel for a subscription.
func (b *MessageBus) UnregisterNotificationChannel(subscriptionID string) {
	if b.closed.Load() {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, exists := b.subscriptions[subscriptionID]; exists {
		sub.notifyChannel = nil
	}
}
