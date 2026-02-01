// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConfigureCommunication initializes the tri-modal communication system for all agents.
// This should be called after NewMultiAgentServer() but before starting the server.
//
// The three communication modes:
// 1. MessageBus - Broadcast/pub-sub for topic-based multicast
// 2. MessageQueue - Point-to-point async messaging with queuing
// 3. SharedMemoryStore - Zero-copy data sharing with namespaces
func (s *MultiAgentServer) ConfigureCommunication(
	bus *communication.MessageBus,
	queue *communication.MessageQueue,
	sharedMem *communication.SharedMemoryStore,
	refStore communication.ReferenceStore,
	policy *communication.PolicyManager,
	logger *zap.Logger,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messageBus = bus
	s.messageQueue = queue
	s.sharedMemoryComm = sharedMem
	s.refStore = refStore
	s.commPolicy = policy
	s.commLogger = logger

	// Inject communication components and tools into all agents
	for agentID, ag := range s.agents {
		ag.SetReferenceStore(refStore)
		ag.SetCommunicationPolicy(policy)

		// Inject communication tools (send_message, publish, shared_memory_write, shared_memory_read, presentation tools)
		// Note: Messages are auto-injected via event-driven notifications, no manual receive tool needed
		commTools := builtin.CommunicationTools(queue, bus, sharedMem, agentID)

		// Configure send_message tool with registry for auto-healing agent IDs
		if s.registry != nil {
			for _, tool := range commTools {
				if sendTool, ok := tool.(*builtin.SendMessageTool); ok {
					sendTool.SetAgentRegistry(s.registry, logger)
					logger.Debug("Configured send_message tool with agent registry for auto-healing",
						zap.String("agent_id", agentID))
					break
				}
			}
		}

		ag.RegisterTools(commTools...)

		logger.Debug("Communication tools injected into agent",
			zap.String("agent_id", agentID),
			zap.Int("num_tools", len(commTools)))
	}

	return nil
}

// GetCommunicationComponents returns the communication system components (for testing/inspection).
func (s *MultiAgentServer) GetCommunicationComponents() (
	*communication.MessageBus,
	*communication.MessageQueue,
	*communication.SharedMemoryStore,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messageBus, s.messageQueue, s.sharedMemoryComm
}

// ============================================================================
// Broadcast Bus RPCs
// ============================================================================

// Publish publishes a message to a topic on the broadcast bus.
// All subscribers to the topic will receive the message.
func (s *MultiAgentServer) Publish(ctx context.Context, req *loomv1.PublishRequest) (*loomv1.PublishResponse, error) {
	if s.messageBus == nil {
		return nil, status.Error(codes.Unavailable, "message bus not configured")
	}

	if req.Topic == "" {
		return nil, status.Error(codes.InvalidArgument, "topic cannot be empty")
	}

	if req.Message == nil {
		return nil, status.Error(codes.InvalidArgument, "message cannot be nil")
	}

	// Ensure message has required fields
	if req.Message.FromAgent == "" {
		return nil, status.Error(codes.InvalidArgument, "message.from_agent cannot be empty")
	}

	// Set topic and timestamp if not set
	if req.Message.Topic == "" {
		req.Message.Topic = req.Topic
	}

	// Publish to bus and get actual delivery counts
	delivered, dropped, err := s.messageBus.Publish(ctx, req.Topic, req.Message)
	if err != nil {
		s.commLogger.Error("failed to publish message",
			zap.String("topic", req.Topic),
			zap.String("from_agent", req.Message.FromAgent),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to publish: %v", err)
	}

	s.commLogger.Debug("message published",
		zap.String("topic", req.Topic),
		zap.String("message_id", req.Message.Id),
		zap.String("from_agent", req.Message.FromAgent),
		zap.Int("subscriber_count", delivered),
		zap.Int("dropped", dropped))

	return &loomv1.PublishResponse{
		MessageId:       req.Message.Id,
		SubscriberCount: int32(delivered),
	}, nil
}

// Subscribe creates a subscription to a topic and streams messages back to the client.
// The stream will remain open until the client closes it or an error occurs.
func (s *MultiAgentServer) Subscribe(req *loomv1.SubscribeRequest, stream loomv1.LoomService_SubscribeServer) error {
	if s.messageBus == nil {
		return status.Error(codes.Unavailable, "message bus not configured")
	}

	if req.AgentId == "" {
		return status.Error(codes.InvalidArgument, "agent_id cannot be empty")
	}

	if req.TopicPattern == "" {
		return status.Error(codes.InvalidArgument, "topic_pattern cannot be empty")
	}

	ctx := stream.Context()

	// Create subscription
	subscription, err := s.messageBus.Subscribe(ctx, req.AgentId, req.TopicPattern, req.Filter, int(req.BufferSize))
	if err != nil {
		s.commLogger.Error("failed to create subscription",
			zap.String("agent_id", req.AgentId),
			zap.String("topic_pattern", req.TopicPattern),
			zap.Error(err))
		return status.Errorf(codes.Internal, "failed to subscribe: %v", err)
	}

	s.commLogger.Info("subscription created",
		zap.String("subscription_id", subscription.ID),
		zap.String("agent_id", req.AgentId),
		zap.String("topic_pattern", req.TopicPattern))

	// Unsubscribe on exit
	defer func() {
		if err := s.messageBus.Unsubscribe(context.Background(), subscription.ID); err != nil {
			s.commLogger.Warn("failed to unsubscribe",
				zap.String("subscription_id", subscription.ID),
				zap.Error(err))
		}
	}()

	// Stream messages to client
	for {
		select {
		case <-ctx.Done():
			s.commLogger.Debug("subscription context done",
				zap.String("subscription_id", subscription.ID),
				zap.Error(ctx.Err()))
			return ctx.Err()

		case msg, ok := <-subscription.Channel:
			if !ok {
				// Channel closed (bus shutdown or unsubscribe)
				s.commLogger.Debug("subscription channel closed",
					zap.String("subscription_id", subscription.ID))
				return nil
			}

			// Send message to client
			if err := stream.Send(msg); err != nil {
				s.commLogger.Warn("failed to send message to subscriber",
					zap.String("subscription_id", subscription.ID),
					zap.String("message_id", msg.Id),
					zap.Error(err))
				return err
			}

			s.commLogger.Debug("message sent to subscriber",
				zap.String("subscription_id", subscription.ID),
				zap.String("message_id", msg.Id),
				zap.String("topic", msg.Topic))
		}
	}
}

// ============================================================================
// Shared Memory RPCs
// ============================================================================

// PutSharedMemory writes or updates a value in shared memory.
func (s *MultiAgentServer) PutSharedMemory(ctx context.Context, req *loomv1.PutSharedMemoryRequest) (*loomv1.PutSharedMemoryResponse, error) {
	if s.sharedMemoryComm == nil {
		return nil, status.Error(codes.Unavailable, "shared memory not configured")
	}

	resp, err := s.sharedMemoryComm.Put(ctx, req)
	if err != nil {
		s.commLogger.Error("failed to put shared memory",
			zap.String("namespace", req.Namespace.String()),
			zap.String("key", req.Key),
			zap.String("agent_id", req.AgentId),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to put: %v", err)
	}

	return resp, nil
}

// GetSharedMemory retrieves a value from shared memory.
func (s *MultiAgentServer) GetSharedMemory(ctx context.Context, req *loomv1.GetSharedMemoryRequest) (*loomv1.GetSharedMemoryResponse, error) {
	if s.sharedMemoryComm == nil {
		return nil, status.Error(codes.Unavailable, "shared memory not configured")
	}

	resp, err := s.sharedMemoryComm.Get(ctx, req)
	if err != nil {
		s.commLogger.Error("failed to get shared memory",
			zap.String("namespace", req.Namespace.String()),
			zap.String("key", req.Key),
			zap.String("agent_id", req.AgentId),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get: %v", err)
	}

	return resp, nil
}

// DeleteSharedMemory removes a value from shared memory.
func (s *MultiAgentServer) DeleteSharedMemory(ctx context.Context, req *loomv1.DeleteSharedMemoryRequest) (*loomv1.DeleteSharedMemoryResponse, error) {
	if s.sharedMemoryComm == nil {
		return nil, status.Error(codes.Unavailable, "shared memory not configured")
	}

	resp, err := s.sharedMemoryComm.Delete(ctx, req)
	if err != nil {
		s.commLogger.Error("failed to delete shared memory",
			zap.String("namespace", req.Namespace.String()),
			zap.String("key", req.Key),
			zap.String("agent_id", req.AgentId),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to delete: %v", err)
	}

	return resp, nil
}

// WatchSharedMemory watches for changes to keys in a namespace and streams updates.
func (s *MultiAgentServer) WatchSharedMemory(req *loomv1.WatchSharedMemoryRequest, stream loomv1.LoomService_WatchSharedMemoryServer) error {
	if s.sharedMemoryComm == nil {
		return status.Error(codes.Unavailable, "shared memory not configured")
	}

	ctx := stream.Context()

	// Create watcher
	watchChan, err := s.sharedMemoryComm.Watch(ctx, req)
	if err != nil {
		s.commLogger.Error("failed to create watcher",
			zap.String("namespace", req.Namespace.String()),
			zap.String("key_pattern", req.KeyPattern),
			zap.String("agent_id", req.AgentId),
			zap.Error(err))
		return status.Errorf(codes.Internal, "failed to watch: %v", err)
	}

	s.commLogger.Info("watcher created",
		zap.String("namespace", req.Namespace.String()),
		zap.String("key_pattern", req.KeyPattern),
		zap.String("agent_id", req.AgentId))

	// Stream updates to client
	for {
		select {
		case <-ctx.Done():
			s.commLogger.Debug("watcher context done",
				zap.String("agent_id", req.AgentId),
				zap.Error(ctx.Err()))
			return ctx.Err()

		case value, ok := <-watchChan:
			if !ok {
				// Channel closed (store shutdown)
				s.commLogger.Debug("watcher channel closed",
					zap.String("agent_id", req.AgentId))
				return nil
			}

			// Send update to client
			if err := stream.Send(value); err != nil {
				s.commLogger.Warn("failed to send update to watcher",
					zap.String("agent_id", req.AgentId),
					zap.String("key", value.Key),
					zap.Error(err))
				return err
			}

			s.commLogger.Debug("update sent to watcher",
				zap.String("agent_id", req.AgentId),
				zap.String("key", value.Key),
				zap.String("namespace", value.Namespace.String()))
		}
	}
}

// ListSharedMemoryKeys lists all keys matching a pattern in a namespace.
func (s *MultiAgentServer) ListSharedMemoryKeys(ctx context.Context, req *loomv1.ListSharedMemoryKeysRequest) (*loomv1.ListSharedMemoryKeysResponse, error) {
	if s.sharedMemoryComm == nil {
		return nil, status.Error(codes.Unavailable, "shared memory not configured")
	}

	resp, err := s.sharedMemoryComm.List(ctx, req)
	if err != nil {
		s.commLogger.Error("failed to list keys",
			zap.String("namespace", req.Namespace.String()),
			zap.String("key_pattern", req.KeyPattern),
			zap.String("agent_id", req.AgentId),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to list: %v", err)
	}

	return resp, nil
}

// GetSharedMemoryStats retrieves statistics for a namespace.
func (s *MultiAgentServer) GetSharedMemoryStats(ctx context.Context, req *loomv1.GetSharedMemoryStatsRequest) (*loomv1.SharedMemoryStats, error) {
	if s.sharedMemoryComm == nil {
		return nil, status.Error(codes.Unavailable, "shared memory not configured")
	}

	stats, err := s.sharedMemoryComm.GetStats(ctx, req.Namespace)
	if err != nil {
		s.commLogger.Error("failed to get stats",
			zap.String("namespace", req.Namespace.String()),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get stats: %v", err)
	}

	return stats, nil
}

// SendAsync sends a fire-and-forget message to another agent via the message queue.
// If the destination agent is offline, the message is queued for later delivery.
func (s *MultiAgentServer) SendAsync(ctx context.Context, req *loomv1.SendAsyncRequest) (*loomv1.SendAsyncResponse, error) {
	if s.messageQueue == nil {
		return nil, status.Error(codes.Unavailable, "message queue not configured")
	}

	// Check if destination agent exists
	destinationOnline := false
	s.mu.RLock()
	if _, exists := s.agents[req.ToAgent]; exists {
		destinationOnline = true
	}
	s.mu.RUnlock()

	// Send message via queue
	messageID, err := s.messageQueue.Send(ctx, req.FromAgent, req.ToAgent, req.MessageType, req.Payload, req.Metadata)
	if err != nil {
		s.commLogger.Error("failed to send async message",
			zap.String("from", req.FromAgent),
			zap.String("to", req.ToAgent),
			zap.String("type", req.MessageType),
			zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to send message: %v", err)
	}

	s.commLogger.Debug("async message sent",
		zap.String("from", req.FromAgent),
		zap.String("to", req.ToAgent),
		zap.String("type", req.MessageType),
		zap.String("message_id", messageID),
		zap.Bool("queued", !destinationOnline))

	return &loomv1.SendAsyncResponse{
		MessageId: messageID,
		Queued:    !destinationOnline,
	}, nil
}

// SendAndReceive sends a request message and waits for a response (RPC-style).
// This implements synchronous request-response communication via the message queue.
func (s *MultiAgentServer) SendAndReceive(ctx context.Context, req *loomv1.SendAndReceiveRequest) (*loomv1.SendAndReceiveResponse, error) {
	if s.messageQueue == nil {
		return nil, status.Error(codes.Unavailable, "message queue not configured")
	}

	// Check if destination agent exists
	s.mu.RLock()
	_, exists := s.agents[req.ToAgent]
	s.mu.RUnlock()

	if !exists {
		return nil, status.Errorf(codes.NotFound, "destination agent not found: %s", req.ToAgent)
	}

	// Create timeout context
	timeoutDur := communication.DefaultQueueTimeout
	if req.TimeoutSeconds > 0 {
		timeoutDur = int(req.TimeoutSeconds)
	}

	// Send request and wait for response
	// Note: MessageQueue.SendAndReceive expects timeout in seconds
	respPayload, err := s.messageQueue.SendAndReceive(ctx, req.FromAgent, req.ToAgent, req.MessageType, req.Payload, req.Metadata, timeoutDur)
	if err != nil {
		s.commLogger.Error("failed to send and receive",
			zap.String("from", req.FromAgent),
			zap.String("to", req.ToAgent),
			zap.String("type", req.MessageType),
			zap.Error(err))

		// Check for timeout error
		if err.Error() == "timeout waiting for response" {
			return nil, status.Error(codes.DeadlineExceeded, "request timed out")
		}

		return nil, status.Errorf(codes.Internal, "failed to send and receive: %v", err)
	}

	s.commLogger.Debug("request-response completed",
		zap.String("from", req.FromAgent),
		zap.String("to", req.ToAgent),
		zap.String("type", req.MessageType))

	return &loomv1.SendAndReceiveResponse{
		Payload: respPayload,
	}, nil
}
