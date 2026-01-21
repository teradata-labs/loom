// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap"
)

// SpawnSubAgent spawns a new agent as a child of the current session.
// This implements the builtin.SpawnHandler interface.
func (s *MultiAgentServer) SpawnSubAgent(ctx context.Context, req *builtin.SpawnSubAgentRequest) (*builtin.SpawnSubAgentResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("spawn request cannot be nil")
	}

	// Validate required fields
	if req.ParentSessionID == "" {
		return nil, fmt.Errorf("parent session ID is required")
	}
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent ID is required")
	}

	// Check registry is available
	s.mu.RLock()
	registry := s.registry
	logger := s.logger
	messageBus := s.messageBus
	s.mu.RUnlock()

	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	logger.Info("Spawning sub-agent",
		zap.String("parent_session", req.ParentSessionID),
		zap.String("parent_agent", req.ParentAgentID),
		zap.String("agent_id", req.AgentID),
		zap.String("workflow_id", req.WorkflowID))

	// Check spawn limits (prevent spawn bombs)
	existingSpawns := s.countSpawnedAgentsByParent(req.ParentSessionID)
	maxSpawnsPerParent := 10 // TODO: Make configurable
	if existingSpawns >= maxSpawnsPerParent {
		return nil, fmt.Errorf("spawn limit reached: parent has %d spawned agents (max: %d)", existingSpawns, maxSpawnsPerParent)
	}

	// Build full sub-agent ID with namespace (ALWAYS namespaced)
	// If workflow_id provided, use it; otherwise auto-generate namespace from parent
	namespace := req.WorkflowID
	if namespace == "" {
		// Auto-generate namespace: parent-agent-id + session-based suffix
		namespace = fmt.Sprintf("%s-spawn", req.ParentAgentID)
	}
	subAgentID := fmt.Sprintf("%s:%s", namespace, req.AgentID)

	logger.Info("Building namespaced sub-agent ID",
		zap.String("namespace", namespace),
		zap.String("agent_id", req.AgentID),
		zap.String("sub_agent_id", subAgentID))

	// IMPORTANT: Load fresh agent instance for spawned agent (not from cache)
	// This prevents concurrent Chat() calls on the same agent instance which can cause
	// issues with shared state (memory, sessions, etc.)
	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}

	ag, err := registry.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, fmt.Errorf("failed to load agent %s: %w", req.AgentID, err)
	}

	logger.Debug("Loaded fresh agent instance for spawned agent",
		zap.String("agent_id", req.AgentID),
		zap.String("sub_agent_id", subAgentID))

	// Create new session for sub-agent
	sessionID := GenerateSessionID()
	session := &agent.Session{
		ID:              sessionID,
		AgentID:         req.AgentID,
		ParentSessionID: req.ParentSessionID, // Link to parent
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Store session
	if err := s.sessionStore.SaveSession(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	logger.Info("Created sub-agent session",
		zap.String("session_id", sessionID),
		zap.String("sub_agent_id", subAgentID))

	// Auto-subscribe to topics if specified
	var subscribedTopics []string
	var subscriptionIDs []string
	var notifyChannels []chan struct{}

	if messageBus != nil && len(req.AutoSubscribe) > 0 {
		for _, topic := range req.AutoSubscribe {
			subscription, err := messageBus.Subscribe(ctx, subAgentID, topic, nil, 100)
			if err != nil {
				logger.Warn("Failed to auto-subscribe to topic",
					zap.String("topic", topic),
					zap.String("sub_agent_id", subAgentID),
					zap.Error(err))
				continue
			}

			// Create notification channel for event-driven wake-up
			notifyChan := make(chan struct{}, 10)
			messageBus.RegisterNotificationChannel(subscription.ID, notifyChan)

			subscribedTopics = append(subscribedTopics, topic)
			subscriptionIDs = append(subscriptionIDs, subscription.ID)
			notifyChannels = append(notifyChannels, notifyChan)

			logger.Info("Auto-subscribed spawned agent to topic",
				zap.String("sub_agent_id", subAgentID),
				zap.String("topic", topic),
				zap.String("subscription_id", subscription.ID))
		}
	}

	// Create contexts for lifecycle management
	subCtx, cancel := context.WithCancel(context.Background())      // For session monitoring
	loopCtx, loopCancel := context.WithCancel(context.Background()) // For background message loop

	// Determine auto-despawn timeout (default: 15 minutes of inactivity)
	autoDespawnTimeout := 15 * time.Minute
	if timeoutStr, ok := req.Metadata["auto_despawn_minutes"]; ok {
		if minutes, err := time.ParseDuration(timeoutStr + "m"); err == nil {
			autoDespawnTimeout = minutes
		}
	}

	// Track spawned agent
	spawnedAgent := &spawnedAgentContext{
		parentSessionID:    req.ParentSessionID,
		parentAgentID:      req.ParentAgentID,
		subAgentID:         subAgentID,
		subSessionID:       sessionID,
		workflowID:         req.WorkflowID,
		agent:              ag,
		spawnedAt:          time.Now(),
		subscriptions:      subscribedTopics,
		subscriptionIDs:    subscriptionIDs,
		notifyChannels:     notifyChannels,
		metadata:           req.Metadata,
		cancelFunc:         cancel,
		loopCancelFunc:     loopCancel,
		autoDespawnTimeout: autoDespawnTimeout,
	}

	s.spawnedAgentsMu.Lock()
	s.spawnedAgents[sessionID] = spawnedAgent
	s.spawnedAgentsMu.Unlock()

	logger.Info("Spawned sub-agent tracked",
		zap.String("session_id", sessionID),
		zap.String("sub_agent_id", subAgentID),
		zap.Int("subscribed_topics", len(subscribedTopics)))

	// TODO: Send initial message if provided
	// For now, parent must send initial message via send_message or publish
	// The initial_message parameter is stored in metadata for future use
	if req.InitialMessage != "" {
		if spawnedAgent.metadata == nil {
			spawnedAgent.metadata = make(map[string]string)
		}
		spawnedAgent.metadata["initial_message"] = req.InitialMessage
		logger.Info("Initial message stored in metadata (parent should send via send_message/publish)",
			zap.String("session_id", sessionID),
			zap.String("message_preview", truncateString(req.InitialMessage, 50)))
	}

	// Start background monitoring for sub-agent lifecycle
	go s.monitorSpawnedAgent(subCtx, sessionID)

	// Start background message processing loop (active agent)
	if len(subscriptionIDs) > 0 {
		go s.runSpawnedAgentLoop(loopCtx, spawnedAgent)
		logger.Info("Started background message processing loop for spawned agent",
			zap.String("sub_agent_id", subAgentID),
			zap.Int("subscriptions", len(subscriptionIDs)))
	}

	// Build response
	resp := &builtin.SpawnSubAgentResponse{
		SubAgentID:       subAgentID,
		SessionID:        sessionID,
		Status:           "spawned",
		SubscribedTopics: subscribedTopics,
	}

	logger.Info("Sub-agent spawn complete",
		zap.String("sub_agent_id", subAgentID),
		zap.String("session_id", sessionID),
		zap.Int("subscribed_topics", len(subscribedTopics)))

	return resp, nil
}

// countSpawnedAgentsByParent counts how many agents a parent has spawned
func (s *MultiAgentServer) countSpawnedAgentsByParent(parentSessionID string) int {
	s.spawnedAgentsMu.RLock()
	defer s.spawnedAgentsMu.RUnlock()

	count := 0
	for _, spawned := range s.spawnedAgents {
		if spawned.parentSessionID == parentSessionID {
			count++
		}
	}
	return count
}

// monitorSpawnedAgent monitors a spawned agent's lifecycle and cleans up when done
func (s *MultiAgentServer) monitorSpawnedAgent(ctx context.Context, sessionID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	for {
		select {
		case <-ctx.Done():
			// Context canceled (parent shutdown)
			logger.Info("Spawned agent monitor canceled",
				zap.String("session_id", sessionID))
			s.cleanupSpawnedAgent(sessionID, "parent context canceled")
			return

		case <-ticker.C:
			// Check if session is still active
			session, err := s.sessionStore.LoadSession(ctx, sessionID)
			if err != nil {
				logger.Warn("Failed to get spawned agent session",
					zap.String("session_id", sessionID),
					zap.Error(err))
				continue
			}

			// Get agent context to check auto-despawn timeout
			s.spawnedAgentsMu.RLock()
			spawned, exists := s.spawnedAgents[sessionID]
			s.spawnedAgentsMu.RUnlock()

			if !exists {
				// Agent was already cleaned up
				return
			}

			// Check if session expired (exceeded auto-despawn timeout)
			timeout := spawned.autoDespawnTimeout
			if time.Since(session.UpdatedAt) > timeout {
				logger.Info("Spawned agent auto-despawn triggered",
					zap.String("session_id", sessionID),
					zap.String("sub_agent_id", spawned.subAgentID),
					zap.Duration("idle_time", time.Since(session.UpdatedAt)),
					zap.Duration("timeout", timeout))
				s.cleanupSpawnedAgent(sessionID, "auto-despawn: inactivity timeout")
				return
			}
		}
	}
}

// DespawnSubAgent terminates a spawned sub-agent.
// This implements the builtin.DespawnHandler interface.
func (s *MultiAgentServer) DespawnSubAgent(ctx context.Context, req *builtin.DespawnSubAgentRequest) (*builtin.DespawnSubAgentResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("despawn request cannot be nil")
	}
	if req.SubAgentID == "" {
		return nil, fmt.Errorf("sub_agent_id is required")
	}

	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	logger.Info("Despawning sub-agent",
		zap.String("parent_session", req.ParentSessionID),
		zap.String("sub_agent_id", req.SubAgentID),
		zap.String("reason", req.Reason))

	// Find the spawned agent by sub-agent ID
	s.spawnedAgentsMu.Lock()
	var targetSessionID string
	for sessionID, spawned := range s.spawnedAgents {
		if spawned.subAgentID == req.SubAgentID && spawned.parentSessionID == req.ParentSessionID {
			targetSessionID = sessionID
			break
		}
	}
	s.spawnedAgentsMu.Unlock()

	if targetSessionID == "" {
		logger.Warn("Sub-agent not found for despawn",
			zap.String("sub_agent_id", req.SubAgentID),
			zap.String("parent_session", req.ParentSessionID))
		return &builtin.DespawnSubAgentResponse{
			SubAgentID: req.SubAgentID,
			SessionID:  "",
			Status:     "not_found",
		}, nil
	}

	// Clean up the spawned agent
	reason := req.Reason
	if reason == "" {
		reason = "despawned by parent"
	}
	s.cleanupSpawnedAgent(targetSessionID, reason)

	logger.Info("Sub-agent despawned successfully",
		zap.String("sub_agent_id", req.SubAgentID),
		zap.String("session_id", targetSessionID))

	return &builtin.DespawnSubAgentResponse{
		SubAgentID: req.SubAgentID,
		SessionID:  targetSessionID,
		Status:     "despawned",
	}, nil
}

// cleanupSpawnedAgent removes a spawned agent from tracking and cleans up resources
func (s *MultiAgentServer) cleanupSpawnedAgent(sessionID string, reason string) {
	s.spawnedAgentsMu.Lock()
	spawned, exists := s.spawnedAgents[sessionID]
	if !exists {
		s.spawnedAgentsMu.Unlock()
		return
	}
	delete(s.spawnedAgents, sessionID)
	s.spawnedAgentsMu.Unlock()

	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	logger.Info("Cleaning up spawned agent",
		zap.String("session_id", sessionID),
		zap.String("sub_agent_id", spawned.subAgentID),
		zap.String("reason", reason))

	// Cancel background message processing loop
	if spawned.loopCancelFunc != nil {
		spawned.loopCancelFunc()
	}

	// Cancel session monitoring context
	if spawned.cancelFunc != nil {
		spawned.cancelFunc()
	}

	// Close notification channels
	for _, notifyChan := range spawned.notifyChannels {
		close(notifyChan)
	}

	// Unsubscribe from topics using subscription IDs
	if s.messageBus != nil {
		for _, subID := range spawned.subscriptionIDs {
			err := s.messageBus.Unsubscribe(context.Background(), subID)
			if err != nil {
				logger.Warn("Failed to unsubscribe spawned agent",
					zap.String("subscription_id", subID),
					zap.Error(err))
			} else {
				logger.Debug("Unsubscribed spawned agent",
					zap.String("sub_agent_id", spawned.subAgentID),
					zap.String("subscription_id", subID))
			}
		}
	}

	logger.Info("Spawned agent cleanup complete",
		zap.String("session_id", sessionID),
		zap.String("sub_agent_id", spawned.subAgentID))
}

// cleanupSpawnedAgentsByParent cleans up all spawned agents for a parent session
func (s *MultiAgentServer) cleanupSpawnedAgentsByParent(parentSessionID string) {
	s.spawnedAgentsMu.Lock()
	var toCleanup []string
	for sessionID, spawned := range s.spawnedAgents {
		if spawned.parentSessionID == parentSessionID {
			toCleanup = append(toCleanup, sessionID)
		}
	}
	s.spawnedAgentsMu.Unlock()

	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	if len(toCleanup) > 0 {
		logger.Info("Cleaning up spawned agents for parent",
			zap.String("parent_session", parentSessionID),
			zap.Int("spawned_count", len(toCleanup)))

		for _, sessionID := range toCleanup {
			s.cleanupSpawnedAgent(sessionID, "parent session ended")
		}
	}
}

// truncateString truncates a string to maxLen characters
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// runSpawnedAgentLoop runs a background loop that processes messages from subscribed topics.
// This makes spawned agents "active" - they wake up when messages arrive and respond.
func (s *MultiAgentServer) runSpawnedAgentLoop(ctx context.Context, spawned *spawnedAgentContext) {
	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	logger.Info("Starting spawned agent message processing loop",
		zap.String("agent", spawned.subAgentID),
		zap.String("session", spawned.subSessionID),
		zap.Int("subscriptions", len(spawned.subscriptionIDs)))

	// Process messages from all subscriptions
	for {
		select {
		case <-ctx.Done():
			logger.Info("Spawned agent loop terminated",
				zap.String("agent", spawned.subAgentID))
			return

		default:
			// Wait for message notifications from any subscription
			// Use a select with all notification channels
			if len(spawned.notifyChannels) == 0 {
				return
			}

			// For simplicity, handle single subscription first
			// TODO: Support multiple subscriptions with dynamic select
			notifyChan := spawned.notifyChannels[0]

			select {
			case <-ctx.Done():
				return
			case <-notifyChan:
				// Message available! Process all pending messages
				s.processSpawnedAgentMessages(ctx, spawned)
			case <-time.After(1 * time.Second):
				// Periodic check for context cancellation
				continue
			}
		}
	}
}

// processSpawnedAgentMessages drains and processes all pending messages for a spawned agent
func (s *MultiAgentServer) processSpawnedAgentMessages(ctx context.Context, spawned *spawnedAgentContext) {
	logger := s.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// Get subscriptions for this agent
	subscriptions := s.messageBus.GetSubscriptionsByAgent(spawned.subAgentID)
	if len(subscriptions) == 0 {
		return
	}

	// Drain all pending messages from all subscriptions
	var messages []*BusMessage
	for _, sub := range subscriptions {
		// Non-blocking drain
		for {
			select {
			case msg := <-sub.Channel:
				messages = append(messages, &BusMessage{
					msg:   msg,
					topic: sub.Topic,
				})
			default:
				goto nextSubscription
			}
		}
	nextSubscription:
	}

	if len(messages) == 0 {
		return
	}

	logger.Debug("Spawned agent processing messages",
		zap.String("agent", spawned.subAgentID),
		zap.Int("message_count", len(messages)))

	// Process each message
	for _, busMsg := range messages {
		msg := busMsg.msg

		// Skip messages from self
		if msg.FromAgent == spawned.subAgentID {
			continue
		}

		// Extract message content
		var content string
		if msg.Payload != nil {
			if value := msg.Payload.GetValue(); value != nil {
				content = string(value)
			} else if ref := msg.Payload.GetReference(); ref != nil {
				content = fmt.Sprintf("[Reference: %s]", ref.Id)
			}
		}

		if content == "" {
			continue
		}

		logger.Info("Spawned agent received message",
			zap.String("agent", spawned.subAgentID),
			zap.String("from", msg.FromAgent),
			zap.String("topic", msg.Topic),
			zap.String("message_preview", truncateString(content, 50)))

		// Call agent.Chat() to process the message with timeout
		logger.Info("Calling agent.Chat() for spawned agent",
			zap.String("agent", spawned.subAgentID),
			zap.String("session", spawned.subSessionID))

		chatCtx, chatCancel := context.WithTimeout(ctx, 2*time.Minute)
		resp, err := spawned.agent.Chat(chatCtx, spawned.subSessionID, content)
		chatCancel()

		if err != nil {
			logger.Warn("Spawned agent failed to process message",
				zap.String("agent", spawned.subAgentID),
				zap.String("from", msg.FromAgent),
				zap.Error(err))
			continue
		}

		logger.Info("agent.Chat() returned successfully",
			zap.String("agent", spawned.subAgentID),
			zap.Int("response_len", len(resp.Content)))

		// Publish response back to the same topic
		responseMsg := &loomv1.BusMessage{
			Id:        fmt.Sprintf("%s-response-%d", msg.Id, time.Now().UnixNano()),
			Topic:     msg.Topic,
			FromAgent: spawned.subAgentID,
			Payload: &loomv1.MessagePayload{
				Data: &loomv1.MessagePayload_Value{
					Value: []byte(resp.Content),
				},
			},
			Metadata:  map[string]string{"in_reply_to": msg.Id},
			Timestamp: time.Now().UnixMilli(),
		}

		delivered, dropped, err := s.messageBus.Publish(ctx, msg.Topic, responseMsg)
		if err != nil {
			logger.Warn("Spawned agent failed to publish response",
				zap.String("agent", spawned.subAgentID),
				zap.String("topic", msg.Topic),
				zap.Error(err))
			continue
		}

		logger.Info("Spawned agent published response",
			zap.String("agent", spawned.subAgentID),
			zap.String("topic", msg.Topic),
			zap.Int("delivered", delivered),
			zap.Int("dropped", dropped),
			zap.String("response_preview", truncateString(resp.Content, 50)))

		// Emit SSE event for real-time visibility (if parent session has progress multiplexer)
		s.emitPubSubEvent(spawned.parentSessionID, &PubSubEvent{
			Type:      "agent_message",
			Topic:     msg.Topic,
			FromAgent: spawned.subAgentID,
			ToAgents:  delivered,
			Content:   resp.Content,
			Timestamp: time.Now(),
		})
	}
}

// BusMessage wraps a bus message with its topic for processing
type BusMessage struct {
	msg   *loomv1.BusMessage
	topic string
}

// PubSubEvent represents a pub/sub message event for SSE streaming
type PubSubEvent struct {
	Type      string    // "agent_message"
	Topic     string    // Topic name
	FromAgent string    // Agent ID that sent the message
	ToAgents  int       // Number of agents that received the message
	Content   string    // Message content
	Timestamp time.Time // When the message was published
}

// emitPubSubEvent emits a pub/sub event to the SSE stream if available
func (s *MultiAgentServer) emitPubSubEvent(sessionID string, event *PubSubEvent) {
	s.mu.RLock()
	pm, ok := s.progressMultiplexers[sessionID]
	s.mu.RUnlock()

	if !ok || pm == nil {
		// No progress multiplexer for this session
		return
	}

	// Emit event to progress multiplexer for SSE stream
	pm.Emit(&metaagent.ProgressEvent{
		Type:      "pub_sub_message", // Custom event type
		Timestamp: event.Timestamp,
		Message:   fmt.Sprintf("💬 %s → %s", event.FromAgent, event.Topic),
		Details: map[string]interface{}{
			"from_agent":   event.FromAgent,
			"topic":        event.Topic,
			"delivered_to": event.ToAgents,
			"content":      truncateString(event.Content, 150),
		},
	})
}
