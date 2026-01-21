// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/scheduler"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/tls"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MultiAgentServer implements the LoomService gRPC server with support for multiple agents.
// It routes requests to the appropriate agent based on agent_id in the request.
type MultiAgentServer struct {
	loomv1.UnimplementedLoomServiceServer

	agents       map[string]*agent.Agent
	sessionStore *agent.SessionStore
	mu           sync.RWMutex

	defaultAgentID     string                           // Agent to use when no agent_id specified
	patternBroadcaster *PatternEventBroadcaster         // Broadcasts pattern update events
	hotReloaders       map[string]*patterns.HotReloader // Hot-reloaders for each agent's patterns
	sharedMemory       *storage.SharedMemoryStore       // Shared memory singleton for all agents
	tlsManager         *tls.Manager                     // TLS manager for certificate management
	serverConfig       *loomv1.ServerConfig             // Server configuration (for GetServerConfig RPC)
	factory            *factory.ProviderFactory         // LLM provider factory for dynamic model switching
	modelRegistry      *factory.ModelRegistry           // Model registry for available models

	// Communication system (tri-modal: broadcast, point-to-point, shared memory)
	messageBus       *communication.MessageBus        // Broadcast bus for pub/sub
	messageQueue     *communication.MessageQueue      // Point-to-point async messaging queue
	sharedMemoryComm *communication.SharedMemoryStore // Shared memory with namespaces
	refStore         communication.ReferenceStore     // Reference store for large payloads
	commPolicy       *communication.PolicyManager     // Communication policy manager
	commLogger       *zap.Logger                      // Logger for communication operations

	// MCP Server Management
	mcpManager   *manager.Manager       // MCP server manager for runtime management
	toolRegistry *toolregistry.Registry // Tool registry for dynamic tool discovery (tool_search)
	configPath   string                 // Path to looms.yaml for persistence
	logger       *zap.Logger            // Logger for server operations

	// Artifact storage for file management
	artifactStore artifacts.ArtifactStore

	// Progress multiplexers for multi-turn conversations (weaver, mender, etc.)
	progressMultiplexers map[string]*metaagent.ProgressMultiplexer

	// Pending clarification questions for TUI/RPC answer routing
	pendingQuestions   map[string]*metaagent.Question
	pendingQuestionsMu sync.RWMutex

	// Clarification configuration
	clarificationChannelSendTimeoutMs int // Timeout for sending to answer channel (default: 100ms)

	// Workflow execution storage
	workflowStore *WorkflowStore

	// Agent registry for workflow execution
	registry *agent.Registry

	// Workflow scheduler for cron-based execution
	scheduler *scheduler.Scheduler

	// Workflow sub-agent tracking for event-driven message notifications
	workflowSubAgents   map[string]*workflowSubAgentContext // "coordinatorSessionID:agentID" → context
	workflowSubAgentsMu sync.RWMutex

	// Spawned sub-agent tracking for lifecycle management
	spawnedAgents   map[string]*spawnedAgentContext // sessionID → spawned agent context
	spawnedAgentsMu sync.RWMutex

	// LLM concurrency control to prevent rate limiting
	llmSemaphore        chan struct{} // Semaphore to limit concurrent LLM calls
	llmConcurrencyLimit int           // Max concurrent LLM calls (configurable)
}

// workflowSubAgentContext tracks a running workflow sub-agent for message notifications
type workflowSubAgentContext struct {
	agent               *agent.Agent
	sessionID           string
	workflowID          string
	notifyChan          chan struct{} // Channel to signal new messages
	cancelFunc          context.CancelFunc
	lastChecked         time.Time
	consecutiveFailures int // Track failures for exponential backoff
}

// spawnedAgentContext tracks a spawned sub-agent for lifecycle management
type spawnedAgentContext struct {
	parentSessionID    string             // Parent agent's session ID
	parentAgentID      string             // Parent agent's ID
	subAgentID         string             // Spawned agent's ID (may include workflow prefix)
	subSessionID       string             // Spawned agent's session ID
	workflowID         string             // Optional workflow namespace
	agent              *agent.Agent       // Agent instance
	spawnedAt          time.Time          // When the agent was spawned
	subscriptions      []string           // Topics subscribed to (topic names)
	subscriptionIDs    []string           // Subscription IDs for cleanup
	notifyChannels     []chan struct{}    // Notification channels for event-driven processing
	metadata           map[string]string  // Custom metadata
	cancelFunc         context.CancelFunc // Cancel function for session cleanup
	loopCancelFunc     context.CancelFunc // Cancel function for background loop
	autoDespawnTimeout time.Duration      // Inactivity timeout before auto-despawn
}

// NewMultiAgentServer creates a new multi-agent LoomService server.
func NewMultiAgentServer(agents map[string]*agent.Agent, store *agent.SessionStore) *MultiAgentServer {
	// Find default agent (first one in map, or one named "default")
	var defaultID string
	if defaultAgent, ok := agents["default"]; ok {
		defaultID = "default"
		_ = defaultAgent
	} else {
		// Use first agent as default
		for id := range agents {
			defaultID = id
			break
		}
	}

	// Default LLM concurrency limit: 5 (allows coordinator + multiple sub-agents)
	// Fully serialized (1) can cause deadlocks in workflow scenarios where
	// coordinator and sub-agents need to call LLMs concurrently
	// Can be configured via SetLLMConcurrencyLimit()
	defaultLLMConcurrency := 5

	return &MultiAgentServer{
		agents:                            agents,
		sessionStore:                      store,
		defaultAgentID:                    defaultID,
		patternBroadcaster:                NewPatternEventBroadcaster(),
		hotReloaders:                      make(map[string]*patterns.HotReloader),
		sharedMemory:                      nil,                        // Set via ConfigureSharedMemory()
		tlsManager:                        nil,                        // Set via ConfigureTLS()
		serverConfig:                      nil,                        // Set via ConfigureTLS()
		factory:                           nil,                        // Set via SetProviderFactory()
		modelRegistry:                     factory.NewModelRegistry(), // Initialize with all models
		progressMultiplexers:              make(map[string]*metaagent.ProgressMultiplexer),
		pendingQuestions:                  make(map[string]*metaagent.Question),
		clarificationChannelSendTimeoutMs: 100,                                       // Default 100ms, can be configured via SetClarificationConfig()
		workflowStore:                     NewWorkflowStore(),                        // Initialize workflow execution store
		registry:                          nil,                                       // Set via SetAgentRegistry()
		workflowSubAgents:                 make(map[string]*workflowSubAgentContext), // Initialize workflow sub-agent tracking
		spawnedAgents:                     make(map[string]*spawnedAgentContext),     // Initialize spawned sub-agent tracking
		llmConcurrencyLimit:               defaultLLMConcurrency,
		llmSemaphore:                      make(chan struct{}, defaultLLMConcurrency),
	}
}

// ConfigureSharedMemory initializes shared memory for all agents with the given configuration.
// This should be called after NewMultiAgentServer() but before starting the server.
// All agents will share the same memory store, enabling efficient data passing between them.
func (s *MultiAgentServer) ConfigureSharedMemory(config *storage.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create shared memory store
	sharedMemory := storage.NewSharedMemoryStore(config)
	s.sharedMemory = sharedMemory

	// Inject shared memory into all agents
	for _, ag := range s.agents {
		ag.SetSharedMemory(sharedMemory)
	}

	return nil
}

// SetMCPManager injects the MCP manager for runtime management.
// This should be called after NewMultiAgentServer() to enable MCP server management.
func (s *MultiAgentServer) SetMCPManager(mgr *manager.Manager, configPath string, logger *zap.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpManager = mgr
	s.configPath = configPath
	if logger == nil {
		logger = zap.NewNop()
	}
	s.logger = logger
}

// SetLogger injects the logger for server operations.
// This should be called after NewMultiAgentServer() to enable logging.
func (s *MultiAgentServer) SetLogger(logger *zap.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if logger == nil {
		logger = zap.NewNop()
	}
	s.logger = logger
}

// SetToolRegistry injects the tool registry for dynamic tool discovery.
// This should be called after NewMultiAgentServer() to enable tool_search indexing of MCP tools.
func (s *MultiAgentServer) SetToolRegistry(registry *toolregistry.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolRegistry = registry
}

// SetAgentRegistry injects the agent registry for workflow execution.
// This should be called after NewMultiAgentServer() to enable ExecuteWorkflow RPC.
func (s *MultiAgentServer) SetAgentRegistry(registry *agent.Registry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registry = registry
}

// ConfigureScheduler injects the workflow scheduler for cron-based execution.
// This should be called after NewMultiAgentServer() to enable schedule management RPCs.
func (s *MultiAgentServer) ConfigureScheduler(sched *scheduler.Scheduler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler = sched
}

// SetLLMConcurrencyLimit configures the maximum number of concurrent LLM calls.
// This prevents rate limiting by serializing or limiting parallel LLM API calls.
//
// limit = 1: Fully serialized (safest for strict rate limits)
// limit = 2-5: Some parallelism while staying under quota
// limit > 5: Higher throughput, higher risk of rate limiting
//
// This should be called after NewMultiAgentServer() but before spawning workflow sub-agents.
func (s *MultiAgentServer) SetLLMConcurrencyLimit(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit < 1 {
		limit = 1 // Minimum 1 concurrent call
	}

	s.llmConcurrencyLimit = limit
	s.llmSemaphore = make(chan struct{}, limit)

	if s.logger != nil {
		s.logger.Info("LLM concurrency limit configured",
			zap.Int("limit", limit))
	}
}

// GetSharedMemory returns the shared memory store (for testing/inspection).
func (s *MultiAgentServer) SharedMemoryStore() *storage.SharedMemoryStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sharedMemory
}

// ConfigureTLS sets the TLS manager and server configuration for this server.
// This should be called after NewMultiAgentServer() if TLS is enabled.
func (s *MultiAgentServer) ConfigureTLS(manager *tls.Manager, config *loomv1.ServerConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tlsManager = manager
	s.serverConfig = config
}

// SetClarificationConfig sets the clarification question timeout configuration.
func (s *MultiAgentServer) SetClarificationConfig(channelSendTimeoutMs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channelSendTimeoutMs > 0 {
		s.clarificationChannelSendTimeoutMs = channelSendTimeoutMs
	}
}

// SetProviderFactory sets the LLM provider factory for dynamic model switching.
func (s *MultiAgentServer) SetProviderFactory(f *factory.ProviderFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factory = f
}

// getAgent retrieves an agent by ID, using default if not specified
func (s *MultiAgentServer) getAgent(agentID string) (*agent.Agent, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Use default if not specified
	if agentID == "" {
		agentID = s.defaultAgentID
	}

	ag, ok := s.agents[agentID]
	if !ok {
		available := make([]string, 0, len(s.agents))
		for id := range s.agents {
			available = append(available, id)
		}
		return nil, "", status.Errorf(codes.NotFound, "agent not found: %s (available: %v)", agentID, available)
	}

	return ag, agentID, nil
}

// AddAgent adds a new agent to the server at runtime
func (s *MultiAgentServer) AddAgent(id string, ag *agent.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.agents[id] = ag

	// Set as default if no default exists
	if s.defaultAgentID == "" {
		s.defaultAgentID = id
	}

	// Inject shared memory if configured (legacy storage package)
	if s.sharedMemory != nil {
		ag.SetSharedMemory(s.sharedMemory)
	}

	// Inject communication components if configured
	if s.refStore != nil {
		ag.SetReferenceStore(s.refStore)
	}
	if s.commPolicy != nil {
		ag.SetCommunicationPolicy(s.commPolicy)
	}

	// Inject communication tools if communication is configured
	if s.messageQueue != nil || s.messageBus != nil || s.sharedMemoryComm != nil {
		commTools := builtin.CommunicationTools(s.messageQueue, s.messageBus, s.sharedMemoryComm, id)
		ag.RegisterTools(commTools...)

		if s.commLogger != nil {
			s.commLogger.Debug("Communication tools injected into new agent",
				zap.String("agent_id", id),
				zap.Int("num_tools", len(commTools)))
		}
	}

	// Note: MessageBus, MessageQueue, and SharedMemoryComm are server-level singletons
	// accessed via gRPC RPCs, not directly injected into agents
}

// UpdateAgent replaces an existing agent with a new instance (for hot-reload).
// The new agent will inherit shared memory and communication configuration if set.
func (s *MultiAgentServer) UpdateAgent(id string, ag *agent.Agent) error {
	// Prepare agent completely BEFORE acquiring lock to minimize critical section
	// This prevents deadlock when multiple agents are being reloaded concurrently

	// Inject shared memory if configured
	if s.sharedMemory != nil {
		ag.SetSharedMemory(s.sharedMemory)
	}

	// Inject communication components if configured
	if s.refStore != nil {
		ag.SetReferenceStore(s.refStore)
	}
	if s.commPolicy != nil {
		ag.SetCommunicationPolicy(s.commPolicy)
	}

	// Inject communication tools if communication is configured
	if s.messageQueue != nil || s.messageBus != nil || s.sharedMemoryComm != nil {
		commTools := builtin.CommunicationTools(s.messageQueue, s.messageBus, s.sharedMemoryComm, id)
		ag.RegisterTools(commTools...)

		if s.commLogger != nil {
			s.commLogger.Debug("Communication tools injected into updated agent",
				zap.String("agent_id", id),
				zap.Int("num_tools", len(commTools)))
		}
	}

	// Now acquire lock ONLY for the agent swap (minimal critical section)
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if agent exists
	if _, ok := s.agents[id]; !ok {
		return fmt.Errorf("agent not found: %s", id)
	}

	// Atomic swap
	s.agents[id] = ag

	return nil
}

// RemoveAgent removes an agent from the server
func (s *MultiAgentServer) RemoveAgent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.agents[id]; !ok {
		return fmt.Errorf("agent not found: %s", id)
	}

	delete(s.agents, id)

	// Update default if removed
	if s.defaultAgentID == id {
		for newDefault := range s.agents {
			s.defaultAgentID = newDefault
			break
		}
	}

	return nil
}

// GetAgentIDs returns available agent IDs (internal helper)
func (s *MultiAgentServer) GetAgentIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agents := make([]string, 0, len(s.agents))
	for id := range s.agents {
		agents = append(agents, id)
	}
	return agents
}

// ListAgents implements the gRPC ListAgents RPC method
func (s *MultiAgentServer) ListAgents(ctx context.Context, req *loomv1.ListAgentsRequest) (*loomv1.ListAgentsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agents := make([]*loomv1.AgentInfo, 0, len(s.agents))
	for id, ag := range s.agents {
		// Count active sessions for this agent
		activeSessions := int32(len(ag.ListSessions()))

		// Create metadata with description
		metadata := make(map[string]string)
		if ag.GetDescription() != "" {
			metadata["description"] = ag.GetDescription()
		}

		agents = append(agents, &loomv1.AgentInfo{
			Id:             id,
			Name:           ag.GetName(),
			Status:         "running",
			ActiveSessions: activeSessions,
			Metadata:       metadata,
		})
	}

	return &loomv1.ListAgentsResponse{
		Agents: agents,
	}, nil
}

// Weave executes a user query using the specified agent.
func (s *MultiAgentServer) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	if req.Query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}

	// Get agent
	ag, agentID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	// Get or create session
	sessionID := req.SessionId
	if sessionID == "" {
		sessionID = GenerateSessionID()
	}

	// Add progress multiplexer to context if available for this agent
	s.mu.RLock()
	if pm, ok := s.progressMultiplexers[agentID]; ok {
		ctx = metaagent.WithProgress(ctx, pm)
	}
	s.mu.RUnlock()

	// Register manage_ephemeral_agents tool if not already registered
	// This allows agents to spawn and despawn sub-agents dynamically
	toolNames := ag.ListTools()
	hasManageTool := false
	for _, name := range toolNames {
		if name == "manage_ephemeral_agents" {
			hasManageTool = true
			break
		}
	}
	if !hasManageTool {
		manageTool := builtin.NewManageEphemeralAgentsTool(s, sessionID, agentID)
		ag.RegisterTool(manageTool)
		if s.logger != nil {
			s.logger.Debug("Registered manage_ephemeral_agents tool for session",
				zap.String("session_id", sessionID),
				zap.String("agent_id", agentID))
		}
	}

	// Execute agent chat
	resp, err := ag.Chat(ctx, sessionID, req.Query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agent execution failed: %v", err)
	}

	// Convert response to proto format
	return &loomv1.WeaveResponse{
		Text:      resp.Content,
		SessionId: sessionID,
		AgentId:   agentID,
		Cost: &loomv1.CostInfo{
			LlmCost: &loomv1.LLMCost{
				Provider:     ag.GetLLMProviderName(),
				Model:        ag.GetLLMModel(),
				InputTokens:  int32(resp.Usage.InputTokens),
				OutputTokens: int32(resp.Usage.OutputTokens),
				CostUsd:      resp.Usage.CostUSD,
			},
			TotalCostUsd: resp.Usage.CostUSD,
		},
	}, nil
}

// StreamWeave streams agent execution progress.
func (s *MultiAgentServer) StreamWeave(req *loomv1.WeaveRequest, stream loomv1.LoomService_StreamWeaveServer) error {
	// Debug: log what agent ID we received
	if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] StreamWeave: received agentID='%s', sessionID='%s' in request\n", time.Now().Format("15:04:05"), req.AgentId, req.SessionId)
		f.Close()
	}

	// Validate query
	if req.Query == "" {
		return status.Error(codes.InvalidArgument, "query cannot be empty")
	}

	// Get agent
	ag, resolvedAgentID, err := s.getAgent(req.AgentId)
	if err != nil {
		return err
	}

	// Debug: log which agent was resolved
	if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] StreamWeave: resolved to agentID='%s' (default='%s')\n", time.Now().Format("15:04:05"), resolvedAgentID, s.defaultAgentID)
		f.Close()
	}

	// Generate session ID if not provided
	sessionID := req.SessionId
	if sessionID == "" {
		sessionID = GenerateSessionID()
	}

	// Register manage_ephemeral_agents tool if not already registered
	// This allows agents to spawn and despawn sub-agents dynamically
	toolNames := ag.ListTools()
	hasManageTool := false
	for _, name := range toolNames {
		if name == "manage_ephemeral_agents" {
			hasManageTool = true
			break
		}
	}
	if !hasManageTool {
		manageTool := builtin.NewManageEphemeralAgentsTool(s, sessionID, resolvedAgentID)
		ag.RegisterTool(manageTool)
		if s.logger != nil {
			s.logger.Debug("Registered manage_ephemeral_agents tool for streaming session",
				zap.String("session_id", sessionID),
				zap.String("agent_id", resolvedAgentID))
		}
	}

	// Spawn workflow sub-agents if this is a workflow coordinator
	if err := s.spawnWorkflowSubAgents(stream.Context(), ag, resolvedAgentID, sessionID); err != nil {
		s.logger.Warn("Failed to spawn workflow sub-agents (workflow may run with limited functionality)",
			zap.String("workflow", resolvedAgentID),
			zap.Error(err))
	}

	// NOTE: We do NOT deregister the coordinator here because:
	// - Sub-agents may still be processing in background
	// - Coordinator needs to stay registered to receive notifications when sub-agents respond
	// - Coordinator persists across multiple StreamWeave calls in the same session
	// - Cleanup happens on session timeout or explicit session end
	//
	// The coordinator's lifecycle is tied to the session, not to a single StreamWeave call.
	// This allows asynchronous workflow responses to trigger coordinator notifications
	// even after the coordinator has returned an initial response to the user.

	// Channel to receive agent result
	type agentResult struct {
		resp *agent.Response
		err  error
	}
	resultChan := make(chan agentResult, 1)

	// Channel to receive progress events
	progressChan := make(chan agent.ProgressEvent, 10)

	// Create progress callback that sends events to channel
	progressCallback := func(event agent.ProgressEvent) {
		select {
		case progressChan <- event:
		case <-stream.Context().Done():
			// Context cancelled, stop sending
		}
	}

	// Debug: log agent execution details
	if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] StreamWeave: About to execute agent.ChatWithProgress with sessionID='%s'\n", time.Now().Format("15:04:05"), sessionID)
		// Log agent details
		if ag != nil {
			fmt.Fprintf(f, "[%s] StreamWeave: Agent object pointer: %p\n", time.Now().Format("15:04:05"), ag)
			// Try to get agent config
			if config := ag.GetConfig(); config != nil {
				fmt.Fprintf(f, "[%s] StreamWeave: Agent name='%s', description='%s'\n", time.Now().Format("15:04:05"), config.Name, config.Description)
				fmt.Fprintf(f, "[%s] StreamWeave: Agent system_prompt length=%d chars\n", time.Now().Format("15:04:05"), len(config.SystemPrompt))
				if len(config.SystemPrompt) > 0 {
					preview := config.SystemPrompt
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					fmt.Fprintf(f, "[%s] StreamWeave: Agent system_prompt preview='%s'\n", time.Now().Format("15:04:05"), preview)
				}
			}
		}
		f.Close()
	}

	// Execute agent with progress callback
	go func() {
		resp, err := ag.ChatWithProgress(stream.Context(), sessionID, req.Query, progressCallback)
		// Debug: log response
		if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			if resp != nil {
				fmt.Fprintf(f, "[%s] StreamWeave: Agent responded, response length=%d chars\n", time.Now().Format("15:04:05"), len(resp.Content))
			} else {
				fmt.Fprintf(f, "[%s] StreamWeave: Agent response was nil, error=%v\n", time.Now().Format("15:04:05"), err)
			}
			f.Close()
		}
		resultChan <- agentResult{resp: resp, err: err}
		close(progressChan)
	}()

	// Stream progress events to client
	var finalResult *agentResult
	agentDone := false

	for {
		// If agent is done and progress channel is closed, process result
		if agentDone && progressChan == nil {
			break
		}

		select {
		case event, ok := <-progressChan:
			// Receive progress event from agent
			if !ok {
				// Channel closed, all progress events delivered
				progressChan = nil
				continue
			}

			// Convert agent progress to proto format
			protoProgress := &loomv1.WeaveProgress{
				Stage:          convertAgentStageToProto(event.Stage),
				Progress:       event.Progress,
				Message:        event.Message,
				ToolName:       event.ToolName,
				Timestamp:      event.Timestamp.Unix(),
				PartialContent: event.PartialContent, // Stream partial content to TUI
			}

			// Include HITL request if present
			if event.HITLRequest != nil {
				protoProgress.HitlRequest = &loomv1.HITLRequestInfo{
					RequestId:      event.HITLRequest.RequestID,
					Question:       event.HITLRequest.Question,
					RequestType:    event.HITLRequest.RequestType,
					Priority:       event.HITLRequest.Priority,
					TimeoutSeconds: int32(event.HITLRequest.Timeout.Seconds()),
				}
			}

			// Send to client
			if err := stream.Send(protoProgress); err != nil {
				return err
			}

		case result := <-resultChan:
			// Agent finished - save result
			finalResult = &result
			agentDone = true
			// Continue to drain remaining progress events

		case <-stream.Context().Done():
			return status.Error(codes.Canceled, "client cancelled request")
		}
	}

	// Check final result
	if finalResult == nil {
		return status.Error(codes.Internal, "no result received from agent")
	}

	if finalResult.err != nil {
		return status.Errorf(codes.Internal, "agent execution failed: %v", finalResult.err)
	}

	// Send final completion event with result and cost
	resp := finalResult.resp
	completionProgress := &loomv1.WeaveProgress{
		Stage:     loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
		Progress:  100,
		Message:   "Query completed successfully",
		Timestamp: time.Now().Unix(),
		PartialResult: &loomv1.ExecutionResult{
			Type:     "text",
			DataJson: resp.Content,
		},
		PartialContent: resp.Content,
		Cost: &loomv1.CostInfo{
			TotalCostUsd: resp.Usage.CostUSD,
			LlmCost: &loomv1.LLMCost{
				Provider:     ag.GetLLMProviderName(),
				Model:        ag.GetLLMModel(),
				InputTokens:  int32(resp.Usage.InputTokens),
				OutputTokens: int32(resp.Usage.OutputTokens),
				CostUsd:      resp.Usage.CostUSD,
			},
		},
	}

	return stream.Send(completionProgress)
}

// spawnWorkflowSubAgents spawns background sub-agents for a workflow coordinator
func (s *MultiAgentServer) spawnWorkflowSubAgents(ctx context.Context, coordinatorAgent *agent.Agent, coordinatorID, sessionID string) error {
	// Check if this is a workflow coordinator by looking up its proto config in the registry
	if s.registry == nil {
		return nil // No registry available
	}

	protoConfig := s.registry.GetConfig(coordinatorID)
	if protoConfig == nil || protoConfig.Metadata == nil {
		return nil // Not a workflow agent
	}

	role, hasRole := protoConfig.Metadata["role"]
	workflowName, hasWorkflow := protoConfig.Metadata["workflow"]

	if !hasRole || role != "coordinator" || !hasWorkflow {
		return nil // Not a workflow coordinator
	}

	s.logger.Info("Detected workflow coordinator, spawning sub-agents",
		zap.String("coordinator", coordinatorID),
		zap.String("workflow", workflowName),
		zap.String("session", sessionID))

	// Find all sub-agents with matching workflow prefix
	s.mu.RLock()
	var subAgentIDs []string
	for agentID := range s.agents {
		// Sub-agents have format: workflow-name:agent-id
		if agentID != coordinatorID && len(agentID) > len(workflowName)+1 &&
			agentID[:len(workflowName)] == workflowName && agentID[len(workflowName)] == ':' {
			subAgentIDs = append(subAgentIDs, agentID)
		}
	}
	s.mu.RUnlock()

	if len(subAgentIDs) == 0 {
		return fmt.Errorf("no sub-agents found for workflow '%s'", workflowName)
	}

	s.logger.Info("Found workflow sub-agents",
		zap.String("workflow", workflowName),
		zap.Strings("sub_agents", subAgentIDs))

	// Register coordinator for event-driven message notifications
	coordinatorNotifyChan := make(chan struct{}, 10)

	// Create context for coordinator notification handler lifecycle
	coordinatorCtx, coordinatorCancel := context.WithCancel(context.Background())

	// Use composite key: sessionID:agentID to allow multiple concurrent workflow sessions
	coordinatorKey := fmt.Sprintf("%s:%s", sessionID, coordinatorID)

	s.workflowSubAgentsMu.Lock()
	s.workflowSubAgents[coordinatorKey] = &workflowSubAgentContext{
		agent:       coordinatorAgent,
		sessionID:   sessionID,
		workflowID:  workflowName,
		notifyChan:  coordinatorNotifyChan,
		cancelFunc:  coordinatorCancel, // Store cancel func for cleanup on session end
		lastChecked: time.Now(),
	}
	s.workflowSubAgentsMu.Unlock()

	// DO NOT register coordinator channel with MessageQueue
	// The coordinator's StreamWeave conversation calls receive_message which would compete
	// with the event-driven goroutine below for the same notification channel.
	// Instead, ONLY the message queue monitor (StartMessageQueueMonitor) detects pending
	// messages and signals the coordinator via the event-driven goroutine below.
	// The coordinator's receive_message tool will use polling (no event-driven notifications).

	s.logger.Info("Registered coordinator for event-driven message notifications (monitor-based)",
		zap.String("coordinator", coordinatorID))

	go func() {
		defer func() {
			s.logger.Info("Coordinator notification handler stopped",
				zap.String("coordinator", coordinatorID))
		}()

		for {
			select {
			case <-coordinatorCtx.Done():
				return
			case <-coordinatorNotifyChan:
				// Coordinator has pending messages - retrieve and inject actual message content
				// This makes sub-agent responses visible in the session and triggers coordinator to process them

				// Dequeue message to get actual content
				queueMsg, err := s.messageQueue.Dequeue(context.Background(), coordinatorID)
				if err != nil {
					s.logger.Warn("Failed to dequeue message for coordinator",
						zap.String("coordinator", coordinatorID),
						zap.Error(err))
					continue
				}

				if queueMsg == nil {
					// No message available (race condition)
					continue
				}

				// Extract message content from payload
				var messageContent string
				if queueMsg.Payload != nil {
					if valueData := queueMsg.Payload.GetValue(); valueData != nil {
						messageContent = string(valueData)
					} else if ref := queueMsg.Payload.GetReference(); ref != nil {
						// Handle reference-based payloads (resolve from reference store)
						messageContent = fmt.Sprintf("[Reference: %s]", ref.Id)
					}
				}

				if messageContent == "" {
					messageContent = "[Empty message]"
				}

				// Format as a message from the sub-agent
				// This will be stored in the session and visible via SubscribeToSession
				injectedPrompt := fmt.Sprintf("[MESSAGE FROM %s]:\n\n%s", queueMsg.FromAgent, messageContent)

				s.logger.Info("Injecting sub-agent response into coordinator session",
					zap.String("coordinator", coordinatorID),
					zap.String("from_agent", queueMsg.FromAgent),
					zap.String("message_type", queueMsg.MessageType),
					zap.Int("content_length", len(messageContent)))

				// Acquire semaphore to limit concurrent LLM calls
				s.logger.Info("Coordinator acquiring LLM semaphore for message injection",
					zap.String("coordinator", coordinatorID))
				s.llmSemaphore <- struct{}{}
				s.logger.Info("Coordinator acquired LLM semaphore",
					zap.String("coordinator", coordinatorID))

				// Inject the actual message content into coordinator's session
				// This triggers the coordinator to process the sub-agent's response and generate a synthesis
				s.logger.Info("Coordinator calling Chat() for sub-agent response",
					zap.String("coordinator", coordinatorID))
				_, err = coordinatorAgent.Chat(context.Background(), sessionID, injectedPrompt)
				s.logger.Info("Coordinator Chat() completed",
					zap.String("coordinator", coordinatorID),
					zap.Bool("has_error", err != nil))

				// Release semaphore
				<-s.llmSemaphore
				s.logger.Info("Coordinator released LLM semaphore",
					zap.String("coordinator", coordinatorID))

				if err != nil {
					s.logger.Warn("Failed to inject sub-agent response to coordinator",
						zap.String("coordinator", coordinatorID),
						zap.String("from_agent", queueMsg.FromAgent),
						zap.Error(err))
				}

				// Acknowledge the message
				if ackErr := s.messageQueue.Acknowledge(context.Background(), queueMsg.ID); ackErr != nil {
					s.logger.Warn("Failed to acknowledge message",
						zap.String("message_id", queueMsg.ID),
						zap.Error(ackErr))
				}
			}
		}
	}()

	// Spawn each sub-agent in a background goroutine with long-lived context
	for _, subAgentID := range subAgentIDs {
		subAgent, _, err := s.getAgent(subAgentID)
		if err != nil {
			s.logger.Warn("Failed to get sub-agent",
				zap.String("sub_agent", subAgentID),
				zap.Error(err))
			continue
		}

		// Generate unique session ID for each sub-agent (not shared with coordinator)
		subAgentSessionID := GenerateSessionID()

		// Create context for sub-agent lifecycle
		subAgentCtx, cancel := context.WithCancel(context.Background())

		// Create notification channel for event-driven message handling
		notifyChan := make(chan struct{}, 10) // Buffered to avoid blocking monitor

		// Use composite key: coordinatorSessionID:subAgentID to allow multiple concurrent workflow sessions
		// This prevents different workflow sessions from cancelling each other's sub-agents
		subAgentKey := fmt.Sprintf("%s:%s", sessionID, subAgentID)

		// Register sub-agent for message notifications
		// CRITICAL: Cancel any existing sub-agent with the same key first to prevent orphaned goroutines
		s.workflowSubAgentsMu.Lock()
		if existingCtx, exists := s.workflowSubAgents[subAgentKey]; exists {
			s.logger.Info("Cancelling existing sub-agent before spawning new one",
				zap.String("agent", subAgentID),
				zap.String("key", subAgentKey),
				zap.String("old_session", existingCtx.sessionID),
				zap.String("new_session", subAgentSessionID))
			existingCtx.cancelFunc() // Cancel the old goroutine
		}
		s.workflowSubAgents[subAgentKey] = &workflowSubAgentContext{
			agent:       subAgent,
			sessionID:   subAgentSessionID,
			workflowID:  workflowName,
			notifyChan:  notifyChan,
			cancelFunc:  cancel,
			lastChecked: time.Now(),
		}
		s.workflowSubAgentsMu.Unlock()

		// Register with message queue for event-driven notifications
		if s.messageQueue != nil {
			s.messageQueue.RegisterNotificationChannel(subAgentID, notifyChan)
		}

		// Start sub-agent with notification channel (pass subAgentKey for deregistration)
		go s.runWorkflowSubAgent(subAgentCtx, subAgent, subAgentID, subAgentKey, subAgentSessionID, workflowName, notifyChan)
	}

	return nil
}

// StartMessageQueueMonitor starts a background goroutine that monitors the message queue
// and notifies workflow sub-agents when they have pending messages (event-driven, not polling).
func (s *MultiAgentServer) StartMessageQueueMonitor(ctx context.Context) {
	if s.messageQueue == nil {
		s.logger.Warn("Message queue not configured, skipping queue monitor")
		return
	}

	s.logger.Info("Starting message queue monitor for event-driven agent notifications")

	go func() {
		ticker := time.NewTicker(1 * time.Second) // Check queue every second (cheap, no LLM calls)
		defer ticker.Stop()

		// Cleanup all coordinators and sub-agents when monitor stops
		defer func() {
			s.workflowSubAgentsMu.Lock()
			defer s.workflowSubAgentsMu.Unlock()

			for agentID, subAgentCtx := range s.workflowSubAgents {
				// Cancel goroutines
				if subAgentCtx.cancelFunc != nil {
					subAgentCtx.cancelFunc()
				}

				// Unregister from message queue
				if s.messageQueue != nil {
					s.messageQueue.UnregisterNotificationChannel(agentID)
				}

				s.logger.Debug("Cleaned up workflow agent on shutdown",
					zap.String("agent", agentID))
			}

			// Clear the map
			s.workflowSubAgents = make(map[string]*workflowSubAgentContext)
		}()

		for {
			select {
			case <-ctx.Done():
				s.logger.Info("Message queue monitor stopped - cleaning up workflow agents")
				return
			case <-ticker.C:
				// Get agents with pending messages
				agentsWithMessages := s.messageQueue.GetAgentsWithPendingMessages(ctx)

				if len(agentsWithMessages) > 0 {
					s.logger.Info("MONITOR: Found agents with pending messages",
						zap.Strings("agents", agentsWithMessages))
				}

				for _, agentID := range agentsWithMessages {
					// Check if this is a workflow sub-agent we're tracking
					// Since we now use composite keys (coordinatorSessionID:agentID), we need to find matching entries
					s.workflowSubAgentsMu.RLock()
					var matchingContexts []*workflowSubAgentContext
					for key, ctx := range s.workflowSubAgents {
						// Check if the key ends with ":agentID"
						if len(key) > len(agentID)+1 && key[len(key)-len(agentID)-1:] == ":"+agentID {
							matchingContexts = append(matchingContexts, ctx)
						}
					}
					s.workflowSubAgentsMu.RUnlock()

					if len(matchingContexts) > 0 {
						// Notify all matching sub-agents (different sessions may have same sub-agent)
						for _, subAgentCtx := range matchingContexts {
							select {
							case subAgentCtx.notifyChan <- struct{}{}:
								s.logger.Info("MONITOR: Notified workflow agent of pending message",
									zap.String("agent", agentID),
									zap.String("workflow", subAgentCtx.workflowID))
							default:
								// Channel full, agent already has pending notification
								s.logger.Info("MONITOR: Channel full for agent",
									zap.String("agent", agentID))
							}
						}
					} else {
						// WORKFLOW AUTO-SPAWN: Check if this is a workflow sub-agent that needs spawning
						// Format: "workflow-name:agent-id" (contains colon)
						if strings.Contains(agentID, ":") {
							s.logger.Info("MONITOR: Detected workflow sub-agent with pending messages (not yet spawned)",
								zap.String("agent", agentID))

							// Try to spawn this workflow sub-agent
							if err := s.autoSpawnWorkflowSubAgent(ctx, agentID); err != nil {
								s.logger.Warn("MONITOR: Failed to auto-spawn workflow sub-agent",
									zap.String("agent", agentID),
									zap.Error(err))
							} else {
								s.logger.Info("MONITOR: Successfully auto-spawned workflow sub-agent",
									zap.String("agent", agentID))
							}
						} else {
							s.logger.Debug("MONITOR: Agent has pending messages but not tracked as workflow sub-agent",
								zap.String("agent", agentID))
						}
					}
				}
			}
		}
	}()
}

// autoSpawnWorkflowSubAgent automatically spawns a workflow sub-agent when the monitor detects
// pending messages for it. This handles the case where workflows are registered post-startup
// (e.g., by weaver) and messages arrive before the coordinator connects.
func (s *MultiAgentServer) autoSpawnWorkflowSubAgent(ctx context.Context, agentID string) error {
	// Parse workflow name from agent ID (format: "workflow-name:agent-id")
	parts := strings.SplitN(agentID, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid workflow agent ID format: %s", agentID)
	}
	workflowName := parts[0]

	s.logger.Info("Auto-spawning workflow sub-agent",
		zap.String("agent", agentID),
		zap.String("workflow", workflowName))

	// Get the agent from registry
	subAgent, _, err := s.getAgent(agentID)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Generate unique session ID for this sub-agent
	subAgentSessionID := GenerateSessionID()

	// Create notification channel
	notifyChan := make(chan struct{}, 10)

	// Create context for sub-agent lifecycle
	subAgentCtx, cancel := context.WithCancel(context.Background())

	// Use special composite key for auto-spawned agents: "auto:agentID"
	// This allows us to track them separately from coordinator-spawned agents
	subAgentKey := fmt.Sprintf("auto:%s", agentID)

	// Register sub-agent for message notifications
	s.workflowSubAgentsMu.Lock()
	// Cancel any existing auto-spawned agent with the same key
	if existingCtx, exists := s.workflowSubAgents[subAgentKey]; exists {
		s.logger.Info("Cancelling existing auto-spawned sub-agent before re-spawning",
			zap.String("agent", agentID),
			zap.String("key", subAgentKey))
		existingCtx.cancelFunc()
	}
	s.workflowSubAgents[subAgentKey] = &workflowSubAgentContext{
		agent:       subAgent,
		sessionID:   subAgentSessionID,
		workflowID:  workflowName,
		notifyChan:  notifyChan,
		cancelFunc:  cancel,
		lastChecked: time.Now(),
	}
	s.workflowSubAgentsMu.Unlock()

	// Register with message queue for event-driven notifications
	if s.messageQueue != nil {
		s.messageQueue.RegisterNotificationChannel(agentID, notifyChan)
	}

	// Start sub-agent goroutine
	go s.runWorkflowSubAgent(subAgentCtx, subAgent, agentID, subAgentKey, subAgentSessionID, workflowName, notifyChan)

	return nil
}

// runWorkflowSubAgent runs a workflow sub-agent that processes messages from the coordinator.
// Uses event-driven notifications via notifyChan instead of polling.
// subAgentKey is the composite key "coordinatorSessionID:agentID" used for registration/deregistration.
func (s *MultiAgentServer) runWorkflowSubAgent(ctx context.Context, ag *agent.Agent, agentID, subAgentKey, sessionID, workflowName string, notifyChan <-chan struct{}) {
	s.logger.Info("Workflow sub-agent started (event-driven)",
		zap.String("agent", agentID),
		zap.String("key", subAgentKey),
		zap.String("workflow", workflowName),
		zap.String("session", sessionID))

	defer func() {
		// Deregister sub-agent when stopping (use composite key)
		s.workflowSubAgentsMu.Lock()
		delete(s.workflowSubAgents, subAgentKey)
		s.workflowSubAgentsMu.Unlock()

		// Unregister from message queue (use agent ID, not composite key)
		if s.messageQueue != nil {
			s.messageQueue.UnregisterNotificationChannel(agentID)
		}

		s.logger.Info("Workflow sub-agent stopped",
			zap.String("agent", agentID),
			zap.String("key", subAgentKey),
			zap.String("workflow", workflowName))
	}()

	// Wait for message notifications (event-driven, no polling!)
	// No initialization prompt needed - sub-agent has instructions in its system prompt
	for {
		select {
		case <-ctx.Done():
			return
		case <-notifyChan:
			// Received notification of pending message
			s.logger.Info("Workflow sub-agent notified of pending message",
				zap.String("agent", agentID))

			// Acquire semaphore to limit concurrent LLM calls (prevents rate limiting)
			s.logger.Info("Sub-agent acquiring LLM semaphore",
				zap.String("agent", agentID))
			s.llmSemaphore <- struct{}{}
			s.logger.Info("Sub-agent acquired LLM semaphore",
				zap.String("agent", agentID))

			// Prompt agent to check messages
			checkPrompt := "You have pending messages. Use receive_message to check and process them now."
			s.logger.Info("Sub-agent calling Chat() to process messages",
				zap.String("agent", agentID))

			// Use the parent context directly (no timeout) to allow sub-agent full time to process
			// The agent's own config has max_turns and timeout_seconds configured
			_, err := ag.Chat(ctx, sessionID, checkPrompt)

			s.logger.Info("Sub-agent Chat() completed",
				zap.String("agent", agentID),
				zap.Bool("has_error", err != nil))

			// Release semaphore immediately after LLM call completes
			<-s.llmSemaphore
			s.logger.Info("Sub-agent released LLM semaphore",
				zap.String("agent", agentID))

			if err != nil {
				s.logger.Warn("Workflow sub-agent message check failed",
					zap.String("agent", agentID),
					zap.Error(err))

				// Increment failure counter and apply exponential backoff
				s.workflowSubAgentsMu.Lock()
				if subAgentCtx, exists := s.workflowSubAgents[subAgentKey]; exists {
					subAgentCtx.consecutiveFailures++
					failures := subAgentCtx.consecutiveFailures

					// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s (capped at 32s)
					backoffSeconds := min(1<<(failures-1), 32)
					backoffDuration := time.Duration(backoffSeconds) * time.Second

					s.logger.Info("Applying exponential backoff after failure",
						zap.String("agent", agentID),
						zap.Int("consecutive_failures", failures),
						zap.Duration("backoff", backoffDuration))

					subAgentCtx.lastChecked = time.Now()
					s.workflowSubAgentsMu.Unlock()

					// Sleep with context cancellation support
					select {
					case <-time.After(backoffDuration):
						// Backoff completed
					case <-ctx.Done():
						// Context cancelled during backoff
						return
					}
				} else {
					s.workflowSubAgentsMu.Unlock()
				}
			} else {
				// Success - reset failure counter
				s.workflowSubAgentsMu.Lock()
				if subAgentCtx, exists := s.workflowSubAgents[subAgentKey]; exists {
					if subAgentCtx.consecutiveFailures > 0 {
						s.logger.Info("Sub-agent recovered after failures",
							zap.String("agent", agentID),
							zap.Int("previous_failures", subAgentCtx.consecutiveFailures))
						subAgentCtx.consecutiveFailures = 0
					}
					subAgentCtx.lastChecked = time.Now()
				}
				s.workflowSubAgentsMu.Unlock()
			}
		}
	}
}

// CreatePattern creates a new pattern at runtime for a specific agent.
func (s *MultiAgentServer) CreatePattern(ctx context.Context, req *loomv1.CreatePatternRequest) (*loomv1.CreatePatternResponse, error) {
	// Validate request
	if req.AgentId == "" {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   "agent_id is required",
		}, nil
	}
	if req.Name == "" {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   "pattern name is required",
		}, nil
	}
	if req.YamlContent == "" {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   "yaml_content is required",
		}, nil
	}

	// Get agent
	ag, _, err := s.getAgent(req.AgentId)
	if err != nil {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   fmt.Sprintf("agent not found: %v", err),
		}, nil
	}

	// Get agent's patterns directory
	patternsDir := ag.GetConfig().PatternsDir
	if patternsDir == "" {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   "agent does not have patterns_dir configured",
		}, nil
	}

	// Ensure patterns directory exists
	if err := os.MkdirAll(patternsDir, 0750); err != nil {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create patterns directory: %v", err),
		}, nil
	}

	// Write pattern file atomically
	// Step 1: Write to temp file
	patternFile := filepath.Join(patternsDir, req.Name+".yaml")
	tempFile := patternFile + ".tmp"

	if err := os.WriteFile(tempFile, []byte(req.YamlContent), 0600); err != nil {
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to write temp file: %v", err),
		}, nil
	}

	// Step 2: Atomic rename (replaces existing file if any)
	if err := os.Rename(tempFile, patternFile); err != nil {
		os.Remove(tempFile) // Clean up temp file
		return &loomv1.CreatePatternResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to rename temp file: %v", err),
		}, nil
	}

	// Hot-reload will detect the new file automatically
	// But we can also trigger manual reload for immediate availability
	// (This requires the agent's orchestrator to expose the library)

	// Broadcast pattern creation event to all subscribers
	if s.patternBroadcaster != nil {
		s.patternBroadcaster.BroadcastPatternCreated(req.AgentId, req.Name, "", patternFile)
	}

	return &loomv1.CreatePatternResponse{
		Success:     true,
		PatternName: req.Name,
		FilePath:    patternFile,
	}, nil
}

// StreamPatternUpdates streams pattern update events to clients in real-time.
// Used by TUI and other clients to show live pattern changes without polling.
func (s *MultiAgentServer) StreamPatternUpdates(req *loomv1.StreamPatternUpdatesRequest, stream loomv1.LoomService_StreamPatternUpdatesServer) error {
	// Subscribe to pattern events
	eventCh := s.patternBroadcaster.Subscribe()
	defer s.patternBroadcaster.Unsubscribe(eventCh)

	// Stream events to client
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed
				return nil
			}

			// Apply filters if specified
			if req.AgentId != "" && event.AgentId != req.AgentId {
				continue
			}
			if req.Category != "" && event.Category != req.Category {
				continue
			}

			// Send event to client
			if err := stream.Send(event); err != nil {
				return err
			}

		case <-stream.Context().Done():
			// Client disconnected
			return stream.Context().Err()
		}
	}
}

// StartHotReload initializes hot-reload watchers for all agents with pattern directories.
// Hot-reload events are automatically broadcast to clients via StreamPatternUpdates.
func (s *MultiAgentServer) StartHotReload(ctx context.Context, logger *zap.Logger) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if logger == nil {
		logger = zap.NewNop()
	}

	for agentID, ag := range s.agents {
		// Skip agents without pattern directories
		if ag.GetConfig().PatternsDir == "" {
			logger.Debug("Agent has no patterns directory, skipping hot-reload",
				zap.String("agent_id", agentID))
			continue
		}

		// Get the pattern library from agent's orchestrator
		orchestrator := ag.GetOrchestrator()
		if orchestrator == nil {
			logger.Warn("Agent has no orchestrator, skipping hot-reload",
				zap.String("agent_id", agentID))
			continue
		}

		library := orchestrator.GetLibrary()
		if library == nil {
			logger.Warn("Agent orchestrator has no library, skipping hot-reload",
				zap.String("agent_id", agentID))
			continue
		}

		// Create callback that broadcasts events
		callback := s.createHotReloadCallback(agentID, logger)

		// Create hot-reloader
		hotReloader, err := patterns.NewHotReloader(library, patterns.HotReloadConfig{
			Enabled:    true,
			DebounceMs: 500, // 500ms debounce for file changes
			Logger:     logger,
			OnUpdate:   callback,
		})
		if err != nil {
			logger.Error("Failed to create hot-reloader for agent",
				zap.String("agent_id", agentID),
				zap.Error(err))
			continue
		}

		// Start hot-reloader
		if err := hotReloader.Start(ctx); err != nil {
			logger.Error("Failed to start hot-reloader for agent",
				zap.String("agent_id", agentID),
				zap.Error(err))
			continue
		}

		s.hotReloaders[agentID] = hotReloader
		logger.Info("Hot-reload enabled for agent",
			zap.String("agent_id", agentID),
			zap.String("patterns_dir", ag.GetConfig().PatternsDir))
	}

	logger.Info("Hot-reload initialization complete",
		zap.Int("active_watchers", len(s.hotReloaders)))

	return nil
}

// StopHotReload stops all hot-reload watchers.
// Should be called during server shutdown.
func (s *MultiAgentServer) StopHotReload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for agentID, hotReloader := range s.hotReloaders {
		if err := hotReloader.Stop(); err != nil {
			// Log error but continue stopping others
			fmt.Printf("Error stopping hot-reloader for agent %s: %v\n", agentID, err)
		}
	}

	// Clear the map
	s.hotReloaders = make(map[string]*patterns.HotReloader)

	return nil
}

// createHotReloadCallback creates a callback function that broadcasts pattern events.
func (s *MultiAgentServer) createHotReloadCallback(agentID string, logger *zap.Logger) patterns.PatternUpdateCallback {
	return func(eventType string, patternName string, filePath string, err error) {
		// Map hot-reload event types to broadcaster methods
		switch eventType {
		case "create":
			// Extract category from pattern if possible
			// For now, leave category empty - it will be populated when pattern is loaded
			s.patternBroadcaster.BroadcastPatternCreated(agentID, patternName, "", filePath)
			logger.Info("Pattern created",
				zap.String("agent_id", agentID),
				zap.String("pattern", patternName),
				zap.String("file", filePath))

		case "modify":
			s.patternBroadcaster.BroadcastPatternModified(agentID, patternName, "", filePath)
			logger.Info("Pattern modified",
				zap.String("agent_id", agentID),
				zap.String("pattern", patternName),
				zap.String("file", filePath))

		case "delete":
			s.patternBroadcaster.BroadcastPatternDeleted(agentID, patternName, "")
			logger.Info("Pattern deleted",
				zap.String("agent_id", agentID),
				zap.String("pattern", patternName))

		case "validation_failed":
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			s.patternBroadcaster.BroadcastPatternValidationFailed(agentID, patternName, errMsg)
			logger.Error("Pattern validation failed",
				zap.String("agent_id", agentID),
				zap.String("pattern", patternName),
				zap.String("file", filePath),
				zap.Error(err))
		}
	}
}

// GetServerConfig returns the current server configuration.
func (s *MultiAgentServer) GetServerConfig(ctx context.Context, req *loomv1.GetServerConfigRequest) (*loomv1.ServerConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.serverConfig == nil {
		return &loomv1.ServerConfig{
			Tls: &loomv1.TLSConfig{
				Enabled: false,
			},
		}, nil
	}

	return s.serverConfig, nil
}

// GetTLSStatus returns the current TLS/certificate status.
func (s *MultiAgentServer) GetTLSStatus(ctx context.Context, req *loomv1.GetTLSStatusRequest) (*loomv1.TLSStatus, error) {
	s.mu.RLock()
	manager := s.tlsManager
	s.mu.RUnlock()

	if manager == nil {
		return &loomv1.TLSStatus{
			Enabled: false,
			Mode:    "",
		}, nil
	}

	tlsStatus, err := manager.Status(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get TLS status: %v", err)
	}

	return tlsStatus, nil
}

// RenewCertificate manually triggers certificate renewal.
func (s *MultiAgentServer) RenewCertificate(ctx context.Context, req *loomv1.RenewCertificateRequest) (*loomv1.RenewCertificateResponse, error) {
	s.mu.RLock()
	manager := s.tlsManager
	s.mu.RUnlock()

	if manager == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "TLS is not enabled")
	}

	// Trigger renewal (force=true if requested)
	if err := manager.Renew(ctx, req.Force); err != nil {
		return &loomv1.RenewCertificateResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &loomv1.RenewCertificateResponse{
		Success: true,
		Error:   "",
	}, nil
}

// ConfigureLegacyCommunication initializes basic inter-agent communication with reference store and policy.
// Deprecated: Use ConfigureCommunication() from communication_handlers.go for tri-modal communication.
func (s *MultiAgentServer) ConfigureLegacyCommunication(refStore communication.ReferenceStore, policy *communication.PolicyManager) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.refStore = refStore
	s.commPolicy = policy

	// Inject communication components into all agents
	for _, ag := range s.agents {
		ag.SetReferenceStore(refStore)
		ag.SetCommunicationPolicy(policy)
	}

	return nil
}

// GetReferenceStore returns the reference store (for testing/inspection).
func (s *MultiAgentServer) GetReferenceStore() communication.ReferenceStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.refStore
}

// GetCommunicationPolicy returns the communication policy (for testing/inspection).
func (s *MultiAgentServer) GetCommunicationPolicy() *communication.PolicyManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commPolicy
}

// ListAvailableModels lists all LLM models available to this server instance.
// Uses the provider factory to dynamically determine which models are actually available
// based on configured credentials and environment variables.
func (s *MultiAgentServer) ListAvailableModels(ctx context.Context, req *loomv1.ListAvailableModelsRequest) (*loomv1.ListAvailableModelsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Use factory to determine which models are actually available
	var models []*loomv1.ModelInfo
	if s.factory != nil {
		// Dynamic: only return models from configured providers
		models = s.modelRegistry.GetAvailableModels(s.factory)
	} else {
		// Fallback: return all models (legacy behavior)
		models = s.modelRegistry.GetAllModels()
	}

	// Apply provider filter if specified
	if req.ProviderFilter != "" {
		filtered := make([]*loomv1.ModelInfo, 0)
		for _, m := range models {
			if m.Provider == req.ProviderFilter {
				filtered = append(filtered, m)
			}
		}
		models = filtered
	}

	return &loomv1.ListAvailableModelsResponse{
		Models:     models,
		TotalCount: int32(len(models)),
	}, nil
}

// CreateSession creates a new conversation session for an agent.
func (s *MultiAgentServer) CreateSession(ctx context.Context, req *loomv1.CreateSessionRequest) (*loomv1.Session, error) {
	// Debug: log what agent ID we received
	if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] CreateSession: received agentID='%s' in request\n", time.Now().Format("15:04:05"), req.AgentId)
		f.Close()
	}

	// Get agent (use agent_id if specified, otherwise default)
	ag, resolvedAgentID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	// Debug: log which agent was resolved
	if f, err := os.OpenFile("/tmp/looms-server-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] CreateSession: resolved to agentID='%s' (default='%s')\n", time.Now().Format("15:04:05"), resolvedAgentID, s.defaultAgentID)
		f.Close()
	}

	sessionID := GenerateSessionID()

	// Create session without sending a message to the LLM
	session := ag.CreateSession(sessionID)

	return ConvertSession(session), nil
}

// GetSession retrieves session details.
func (s *MultiAgentServer) GetSession(ctx context.Context, req *loomv1.GetSessionRequest) (*loomv1.Session, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	// Try to find the session in any agent
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ag := range s.agents {
		session, ok := ag.GetSession(req.SessionId)
		if ok {
			return ConvertSession(session), nil
		}
	}

	return nil, status.Error(codes.NotFound, "session not found")
}

// ListSessions lists all sessions across all agents.
func (s *MultiAgentServer) ListSessions(ctx context.Context, req *loomv1.ListSessionsRequest) (*loomv1.ListSessionsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var allSessions []*loomv1.Session
	for _, ag := range s.agents {
		sessions := ag.ListSessions()
		for _, sess := range sessions {
			allSessions = append(allSessions, ConvertSession(sess))
		}
	}

	return &loomv1.ListSessionsResponse{
		Sessions: allSessions,
	}, nil
}

// DeleteSession deletes a session.
func (s *MultiAgentServer) DeleteSession(ctx context.Context, req *loomv1.DeleteSessionRequest) (*loomv1.DeleteSessionResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	// Try to delete the session from any agent that has it
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for _, ag := range s.agents {
		if _, ok := ag.GetSession(req.SessionId); ok {
			ag.DeleteSession(req.SessionId)
			found = true
			break
		}
	}

	// Cleanup any spawned sub-agents before deleting parent session
	s.cleanupSpawnedAgentsByParent(req.SessionId)

	// Also delete from persistent store
	if s.sessionStore != nil {
		if err := s.sessionStore.DeleteSession(ctx, req.SessionId); err != nil {
			// Log but don't fail
			fmt.Printf("Failed to delete session from store: %v\n", err)
		}
	}

	if !found {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	return &loomv1.DeleteSessionResponse{
		Success: true,
	}, nil
}

// GetConversationHistory retrieves conversation history.
func (s *MultiAgentServer) GetConversationHistory(ctx context.Context, req *loomv1.GetConversationHistoryRequest) (*loomv1.ConversationHistory, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	// Try to find the session in any agent to verify it exists
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ag := range s.agents {
		_, ok := ag.GetSession(req.SessionId)
		if ok {
			// Reload messages from database to ensure IDs are populated
			// (in-memory messages don't have database IDs until reloaded)
			messages, err := s.sessionStore.LoadMessages(ctx, req.SessionId)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to load messages: %v", err)
			}

			protoMessages := make([]*loomv1.Message, len(messages))
			for i, msg := range messages {
				protoMessages[i] = ConvertMessage(&msg)
			}

			return &loomv1.ConversationHistory{
				SessionId: req.SessionId,
				Messages:  protoMessages,
			}, nil
		}
	}

	return nil, status.Error(codes.NotFound, "session not found")
}

// ListTools lists all registered tools from the default agent.
// If req.Backend is specified, only tools for that backend are returned.
func (s *MultiAgentServer) ListTools(ctx context.Context, req *loomv1.ListToolsRequest) (*loomv1.ListToolsResponse, error) {
	// Get default agent's tools
	ag, _, err := s.getAgent("")
	if err != nil {
		return nil, err
	}

	// Get tools from agent's registry, optionally filtered by backend
	var tools []shuttle.Tool
	if req.Backend != "" {
		tools = ag.RegisteredToolsByBackend(req.Backend)
	} else {
		tools = ag.RegisteredTools()
	}

	protoTools := make([]*loomv1.ToolDefinition, len(tools))
	for i, tool := range tools {
		protoTools[i] = ConvertTool(tool)
	}

	return &loomv1.ListToolsResponse{
		Tools: protoTools,
	}, nil
}

// GetHealth performs a health check.
func (s *MultiAgentServer) GetHealth(ctx context.Context, req *loomv1.GetHealthRequest) (*loomv1.HealthStatus, error) {
	return &loomv1.HealthStatus{
		Status:  "healthy",
		Version: "0.1.0",
	}, nil
}

// SwitchModel switches the LLM provider/model for a specific agent.
// The agent is identified by agent_id in the request.
func (s *MultiAgentServer) SwitchModel(ctx context.Context, req *loomv1.SwitchModelRequest) (*loomv1.SwitchModelResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.Provider == "" {
		return nil, status.Error(codes.InvalidArgument, "provider is required")
	}
	if req.Model == "" {
		return nil, status.Error(codes.InvalidArgument, "model is required")
	}

	// Get the agent (uses agent_id from request, or default if not specified)
	ag, _, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	// Get previous model info
	previousModel := &loomv1.ModelInfo{
		Id:       ag.GetLLMModel(),
		Name:     ag.GetLLMModel(),
		Provider: ag.GetLLMProviderName(),
	}

	// Check if factory is configured
	s.mu.RLock()
	factoryConfigured := s.factory != nil
	s.mu.RUnlock()

	if !factoryConfigured {
		return nil, status.Error(codes.FailedPrecondition, "model switching not available: provider factory not configured")
	}

	// Create new LLM provider using factory
	s.mu.RLock()
	newProviderIface, err := s.factory.CreateProvider(req.Provider, req.Model)
	s.mu.RUnlock()

	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to create provider: %v", err)
	}

	// Type assert to agent.LLMProvider
	newProvider, ok := newProviderIface.(agent.LLMProvider)
	if !ok {
		return nil, status.Error(codes.Internal, "failed to cast provider to LLMProvider interface")
	}

	// Switch the agent's LLM provider
	ag.SetLLMProvider(newProvider)

	// Verify the switch by checking the agent's current model
	actualModel := ag.GetLLMModel()
	actualProvider := ag.GetLLMProviderName()

	// Get new model info (use actual values from agent to verify)
	newModel := &loomv1.ModelInfo{
		Id:       actualModel,
		Name:     actualModel,
		Provider: actualProvider,
	}

	return &loomv1.SwitchModelResponse{
		PreviousModel: previousModel,
		NewModel:      newModel,
		Success:       true,
	}, nil
}

// SetProgressMultiplexer sets the progress multiplexer for an agent.
// This enables multi-turn conversations and progress event broadcasting.
func (s *MultiAgentServer) SetProgressMultiplexer(agentID string, pm *metaagent.ProgressMultiplexer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progressMultiplexers[agentID] = pm
}

// SetArtifactStore sets the artifact store for file management.
// This should be called after NewMultiAgentServer() to enable artifact operations.
func (s *MultiAgentServer) SetArtifactStore(store artifacts.ArtifactStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.artifactStore = store
}

// AnswerClarificationQuestion provides an answer to a clarification question asked by an agent.
func (s *MultiAgentServer) AnswerClarificationQuestion(ctx context.Context, req *loomv1.AnswerClarificationRequest) (*loomv1.AnswerClarificationResponse, error) {
	// Log RPC invocation with all request details
	if s.logger != nil {
		s.logger.Info("AnswerClarificationQuestion RPC invoked",
			zap.String("question_id", req.QuestionId),
			zap.String("session_id", req.SessionId),
			zap.String("agent_id", req.AgentId),
			zap.Int("answer_length", len(req.Answer)))
	}

	if req.QuestionId == "" {
		if s.logger != nil {
			s.logger.Warn("AnswerClarificationQuestion: validation failed - missing question_id",
				zap.String("session_id", req.SessionId))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "question_id is required",
			Accepted: false,
		}, status.Error(codes.InvalidArgument, "question_id is required")
	}

	if req.Answer == "" {
		if s.logger != nil {
			s.logger.Warn("AnswerClarificationQuestion: validation failed - empty answer",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "answer cannot be empty",
			Accepted: false,
		}, status.Error(codes.InvalidArgument, "answer cannot be empty")
	}

	// Look up the pending question
	s.pendingQuestionsMu.Lock()
	question, exists := s.pendingQuestions[req.QuestionId]
	if !exists {
		s.pendingQuestionsMu.Unlock()
		if s.logger != nil {
			s.logger.Warn("AnswerClarificationQuestion: question not found",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId),
				zap.String("agent_id", req.AgentId))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "question not found or already answered",
			Accepted: false,
		}, nil
	}
	// Remove from pending map immediately to prevent double-answer
	delete(s.pendingQuestions, req.QuestionId)
	s.pendingQuestionsMu.Unlock()

	// Send answer to the question's answer channel (non-blocking)
	if question.AnswerChan == nil {
		if s.logger != nil {
			s.logger.Error("AnswerClarificationQuestion: question has nil answer channel",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "question has no answer channel",
			Accepted: false,
		}, nil
	}

	// Try to send answer with panic recovery for closed channels
	return s.sendAnswerToChannel(ctx, question.AnswerChan, req)
}

// sendAnswerToChannel attempts to send answer to channel with panic recovery
func (s *MultiAgentServer) sendAnswerToChannel(ctx context.Context, answerChan chan<- string, req *loomv1.AnswerClarificationRequest) (resp *loomv1.AnswerClarificationResponse, err error) {
	// Protect against sending to closed channel (will panic)
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed - this is expected if agent timed out
			if s.logger != nil {
				s.logger.Debug("Recovered from closed channel send",
					zap.String("question_id", req.QuestionId),
					zap.Any("panic", r))
			}
			resp = &loomv1.AnswerClarificationResponse{
				Success:  false,
				Error:    "answer channel closed or timeout",
				Accepted: false,
			}
			err = nil
		}
	}()

	select {
	case answerChan <- req.Answer:
		// Answer successfully sent
		if s.logger != nil {
			s.logger.Info("Clarification answer received via RPC",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId),
				zap.Int("answer_length", len(req.Answer)))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  true,
			Accepted: true,
		}, nil
	case <-time.After(time.Duration(s.clarificationChannelSendTimeoutMs) * time.Millisecond):
		// Channel full or timeout - answer not accepted
		if s.logger != nil {
			s.logger.Warn("AnswerClarificationQuestion: channel send timeout",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId),
				zap.Int("timeout_ms", s.clarificationChannelSendTimeoutMs))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "answer channel closed or timeout",
			Accepted: false,
		}, nil
	case <-ctx.Done():
		if s.logger != nil {
			s.logger.Warn("AnswerClarificationQuestion: context cancelled",
				zap.String("question_id", req.QuestionId),
				zap.String("session_id", req.SessionId),
				zap.Error(ctx.Err()))
		}
		return &loomv1.AnswerClarificationResponse{
			Success:  false,
			Error:    "context cancelled",
			Accepted: false,
		}, status.Error(codes.Canceled, "context cancelled")
	}
}

// serverProgressListener is a progress listener that registers pending questions for TUI/RPC routing
type serverProgressListener struct {
	server *MultiAgentServer
	logger *zap.Logger
}

// OnProgress implements metaagent.ProgressListener
func (l *serverProgressListener) OnProgress(event *metaagent.ProgressEvent) {
	if event.Type == metaagent.EventQuestionAsked && event.Question != nil {
		// Register the question in the pending map
		l.server.pendingQuestionsMu.Lock()
		l.server.pendingQuestions[event.Question.ID] = event.Question
		l.server.pendingQuestionsMu.Unlock()

		if l.logger != nil {
			l.logger.Debug("Registered pending clarification question",
				zap.String("question_id", event.Question.ID),
				zap.String("prompt", event.Question.Prompt))
		}
	}

	// Cleanup orphaned questions when answered or timed out
	if event.Type == metaagent.EventQuestionAnswered {
		// Extract question ID from event details
		if questionID, ok := event.Details["question_id"].(string); ok && questionID != "" {
			l.server.pendingQuestionsMu.Lock()
			delete(l.server.pendingQuestions, questionID)
			l.server.pendingQuestionsMu.Unlock()

			if l.logger != nil {
				l.logger.Debug("Cleaned up answered/timed-out clarification question",
					zap.String("question_id", questionID))
			}
		}
	}
}

// RegisterServerProgressListener creates and registers a progress listener for the server.
// This listener tracks pending clarification questions for TUI/RPC answer routing.
func (s *MultiAgentServer) RegisterServerProgressListener(agentName string, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}

	listener := &serverProgressListener{
		server: s,
		logger: logger,
	}

	// Find the progress multiplexer for this agent
	s.mu.RLock()
	multiplexer, exists := s.progressMultiplexers[agentName]
	s.mu.RUnlock()

	if exists && multiplexer != nil {
		multiplexer.AddListener(listener)
		logger.Info("Server progress listener registered for clarification question tracking",
			zap.String("agent", agentName))
	} else {
		logger.Warn("No progress multiplexer found for agent",
			zap.String("agent", agentName))
	}
}

// Shutdown gracefully shuts down the server, closing all pending question channels.
// This should be called during server shutdown to notify waiting agents that no more
// answers will be received.
func (s *MultiAgentServer) Shutdown(ctx context.Context) error {
	s.pendingQuestionsMu.Lock()
	defer s.pendingQuestionsMu.Unlock()

	if s.logger != nil {
		s.logger.Info("Shutting down server, closing pending clarification questions",
			zap.Int("pending_count", len(s.pendingQuestions)))
	}

	// Close all pending question channels to notify waiting agents
	for id, question := range s.pendingQuestions {
		if question.AnswerChan != nil {
			close(question.AnswerChan)
			if s.logger != nil {
				s.logger.Debug("Closed pending question channel",
					zap.String("question_id", id))
			}
		}
		delete(s.pendingQuestions, id)
	}

	return nil
}

// ExecuteWorkflow executes a workflow pattern loaded from YAML or programmatically defined.
// This RPC enables automatic execution of multi-agent workflows.
func (s *MultiAgentServer) ExecuteWorkflow(ctx context.Context, req *loomv1.ExecuteWorkflowRequest) (*loomv1.ExecuteWorkflowResponse, error) {
	if req.Pattern == nil {
		return nil, status.Error(codes.InvalidArgument, "pattern is required")
	}

	// Check registry is configured
	s.mu.RLock()
	registry := s.registry
	s.mu.RUnlock()

	if registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "agent registry not configured")
	}

	// Generate execution ID
	executionID := GenerateSessionID() // Reuse session ID generation

	// Create workflow execution record
	exec := &WorkflowExecution{
		ExecutionID: executionID,
		Pattern:     req.Pattern,
		StartTime:   time.Now(),
		Status:      WorkflowStatusRunning,
	}
	s.workflowStore.Store(exec)

	// Apply timeout if specified
	execCtx := ctx
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Apply variable interpolation
	pattern := req.Pattern
	if len(req.Variables) > 0 {
		pattern = orchestration.InterpolateVariables(pattern, req.Variables)
	}

	// Execute workflow (no progress callback for synchronous execution)
	result, err := s.executeWorkflowInternalWithProgress(execCtx, executionID, pattern, registry, nil)
	if err != nil {
		// Update store with error
		if updateErr := s.workflowStore.UpdateStatus(executionID, WorkflowStatusFailed, err.Error()); updateErr != nil && s.logger != nil {
			s.logger.Warn("Failed to update workflow status", zap.String("execution_id", executionID), zap.Error(updateErr))
		}
		return nil, status.Errorf(codes.Internal, "workflow execution failed: %v", err)
	}

	// Store result
	if err := s.workflowStore.StoreResult(executionID, result); err != nil {
		if s.logger != nil {
			s.logger.Warn("Failed to store workflow result",
				zap.String("execution_id", executionID),
				zap.Error(err))
		}
	}

	return &loomv1.ExecuteWorkflowResponse{
		ExecutionId: executionID,
		Result:      result,
	}, nil
}

// executeWorkflowInternalWithProgress executes a workflow pattern using the orchestrator with optional progress callback.
func (s *MultiAgentServer) executeWorkflowInternalWithProgress(ctx context.Context, executionID string, pattern *loomv1.WorkflowPattern, registry *agent.Registry, progressCallback orchestration.WorkflowProgressCallback) (*loomv1.WorkflowResult, error) {
	// Extract agent IDs from pattern
	agentIDs := orchestration.ExtractAgentIDs(pattern)
	if len(agentIDs) == 0 {
		return nil, fmt.Errorf("no agents found in workflow pattern")
	}

	// Load agents from registry
	agents := make(map[string]*agent.Agent)
	for _, agentID := range agentIDs {
		ag, err := s.getOrLoadAgent(ctx, agentID, registry)
		if err != nil {
			return nil, fmt.Errorf("failed to load agent %s: %w", agentID, err)
		}
		agents[agentID] = ag
	}

	// Create orchestrator with progress callback
	orchestrator := createOrchestratorWithProgress(agents, registry, s.logger, progressCallback)

	// Execute pattern
	result, err := orchestrator.ExecutePattern(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("orchestrator execution failed: %w", err)
	}

	return result, nil
}

// getOrLoadAgent retrieves an agent from the server or loads it from the registry.
func (s *MultiAgentServer) getOrLoadAgent(ctx context.Context, agentID string, registry *agent.Registry) (*agent.Agent, error) {
	// First check if agent is already loaded in server
	s.mu.RLock()
	ag, exists := s.agents[agentID]
	s.mu.RUnlock()

	if exists {
		return ag, nil
	}

	// Load from registry
	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}

	ag, err := registry.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent from registry: %w", err)
	}

	// Optionally cache in server for future use
	// (Uncomment if you want to cache loaded agents)
	// s.AddAgent(agentID, ag)

	return ag, nil
}

// StreamWorkflow executes a workflow and streams progress updates to the client.
// This provides real-time feedback during long-running multi-agent workflows.
func (s *MultiAgentServer) StreamWorkflow(req *loomv1.ExecuteWorkflowRequest, stream loomv1.LoomService_StreamWorkflowServer) error {
	if req.Pattern == nil {
		return status.Error(codes.InvalidArgument, "pattern is required")
	}

	// Check registry is configured
	s.mu.RLock()
	registry := s.registry
	s.mu.RUnlock()

	if registry == nil {
		return status.Error(codes.FailedPrecondition, "agent registry not configured")
	}

	// Generate execution ID
	executionID := GenerateSessionID()

	// Get pattern type for progress messages
	patternType := orchestration.GetPatternType(req.Pattern)

	// Send initial progress
	if err := stream.Send(&loomv1.WorkflowProgress{
		ExecutionId: executionID,
		PatternType: patternType,
		Progress:    0,
		Message:     fmt.Sprintf("Starting %s workflow execution", patternType),
		Timestamp:   time.Now().Unix(),
	}); err != nil {
		return err
	}

	// Create workflow execution record
	exec := &WorkflowExecution{
		ExecutionID: executionID,
		Pattern:     req.Pattern,
		StartTime:   time.Now(),
		Status:      WorkflowStatusRunning,
	}
	s.workflowStore.Store(exec)

	// Apply timeout if specified
	execCtx := stream.Context()
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(stream.Context(), time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Apply variable interpolation
	pattern := req.Pattern
	if len(req.Variables) > 0 {
		pattern = orchestration.InterpolateVariables(pattern, req.Variables)
	}

	// Send loading agents progress
	if err := stream.Send(&loomv1.WorkflowProgress{
		ExecutionId: executionID,
		PatternType: patternType,
		Progress:    10,
		Message:     "Loading agents from registry",
		Timestamp:   time.Now().Unix(),
	}); err != nil {
		return err
	}

	// Channel for progress events from orchestrator
	progressChan := make(chan orchestration.WorkflowProgressEvent, 10)

	// Progress callback that sends events to channel
	progressCallback := func(event orchestration.WorkflowProgressEvent) {
		select {
		case progressChan <- event:
		case <-execCtx.Done():
			// Context cancelled, stop sending
		}
	}

	// Execute workflow in goroutine
	type workflowResult struct {
		result *loomv1.WorkflowResult
		err    error
	}
	resultChan := make(chan workflowResult, 1)

	go func() {
		result, err := s.executeWorkflowInternalWithProgress(execCtx, executionID, pattern, registry, progressCallback)
		resultChan <- workflowResult{result: result, err: err}
		close(progressChan) // Close progress channel when done
	}()

	// Stream progress events to client
	for {
		select {
		case event, ok := <-progressChan:
			// Receive real progress event from orchestrator
			if !ok {
				// Channel closed, continue to result
				progressChan = nil
				continue
			}

			// Convert orchestrator event to proto
			if err := stream.Send(&loomv1.WorkflowProgress{
				ExecutionId:    executionID,
				PatternType:    event.PatternType,
				Progress:       event.Progress,
				Message:        event.Message,
				CurrentAgentId: event.CurrentAgentID,
				Timestamp:      time.Now().Unix(),
				PartialResults: event.PartialResults,
			}); err != nil {
				return err
			}

		case result := <-resultChan:
			// Workflow completed
			if result.err != nil {
				// Update store with error
				if updateErr := s.workflowStore.UpdateStatus(executionID, WorkflowStatusFailed, result.err.Error()); updateErr != nil && s.logger != nil {
					s.logger.Warn("Failed to update workflow status", zap.String("execution_id", executionID), zap.Error(updateErr))
				}

				// Send error progress
				if err := stream.Send(&loomv1.WorkflowProgress{
					ExecutionId: executionID,
					PatternType: patternType,
					Progress:    0,
					Message:     fmt.Sprintf("Workflow failed: %v", result.err),
					Timestamp:   time.Now().Unix(),
				}); err != nil {
					return err
				}

				return status.Errorf(codes.Internal, "workflow execution failed: %v", result.err)
			}

			// Store successful result
			if err := s.workflowStore.StoreResult(executionID, result.result); err != nil {
				if s.logger != nil {
					s.logger.Warn("Failed to store workflow result",
						zap.String("execution_id", executionID),
						zap.Error(err))
				}
			}

			// Send completion progress with results
			return stream.Send(&loomv1.WorkflowProgress{
				ExecutionId:    executionID,
				PatternType:    patternType,
				Progress:       100,
				Message:        "Workflow completed successfully",
				Timestamp:      time.Now().Unix(),
				PartialResults: result.result.AgentResults,
			})

		case <-stream.Context().Done():
			// Client disconnected
			if updateErr := s.workflowStore.UpdateStatus(executionID, WorkflowStatusCanceled, "client disconnected"); updateErr != nil && s.logger != nil {
				s.logger.Warn("Failed to update workflow status", zap.String("execution_id", executionID), zap.Error(updateErr))
			}
			return status.Error(codes.Canceled, "client cancelled request")
		}
	}
}

// createOrchestratorWithProgress creates an orchestrator with the given agents, configuration, and optional progress callback.
func createOrchestratorWithProgress(agents map[string]*agent.Agent, registry *agent.Registry, logger *zap.Logger, progressCallback orchestration.WorkflowProgressCallback) *orchestration.Orchestrator {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Create orchestrator with configuration including progress callback
	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:         registry,
		LLMProvider:      nil, // Orchestrator will use agents' LLM providers as needed
		Tracer:           observability.NewNoOpTracer(),
		Logger:           logger,
		ProgressCallback: progressCallback, // Wire up progress callback
	})

	// Register all agents with orchestrator
	for id, ag := range agents {
		orchestrator.RegisterAgent(id, ag)
	}

	return orchestrator
}

// ScheduleWorkflow creates a new scheduled workflow via RPC.
// The schedule will be persisted in SQLite and executed automatically by the cron engine.
func (s *MultiAgentServer) ScheduleWorkflow(ctx context.Context, req *loomv1.ScheduleWorkflowRequest) (*loomv1.ScheduleWorkflowResponse, error) {
	// Validate request
	if req.WorkflowName == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_name is required")
	}
	if req.Pattern == nil {
		return nil, status.Error(codes.InvalidArgument, "pattern is required")
	}
	if req.Schedule == nil {
		return nil, status.Error(codes.InvalidArgument, "schedule is required")
	}
	if req.Schedule.Cron == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule.cron is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	// Generate schedule ID with nanosecond precision for uniqueness
	now := time.Now()
	scheduleID := fmt.Sprintf("rpc-%s-%d-%d", req.WorkflowName, now.Unix(), now.Nanosecond())

	// Create ScheduledWorkflow proto
	schedule := &loomv1.ScheduledWorkflow{
		Id:           scheduleID,
		WorkflowName: req.WorkflowName,
		YamlPath:     "", // Empty for RPC-created schedules
		Pattern:      req.Pattern,
		Schedule:     req.Schedule,
		CreatedAt:    time.Now().Unix(),
		UpdatedAt:    time.Now().Unix(),
	}

	// Add to scheduler
	if err := sched.AddSchedule(ctx, schedule); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add schedule: %v", err)
	}

	return &loomv1.ScheduleWorkflowResponse{
		ScheduleId: scheduleID,
		Schedule:   schedule,
	}, nil
}

// UpdateScheduledWorkflow updates an existing scheduled workflow.
// YAML-sourced schedules cannot be updated via RPC - they must be modified by editing the YAML file.
func (s *MultiAgentServer) UpdateScheduledWorkflow(ctx context.Context, req *loomv1.UpdateScheduledWorkflowRequest) (*loomv1.ScheduleWorkflowResponse, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	// Get existing schedule
	existing, err := sched.GetSchedule(ctx, req.ScheduleId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "schedule not found: %v", err)
	}

	// Check if this is a YAML-sourced schedule
	if existing.YamlPath != "" {
		return nil, status.Error(codes.FailedPrecondition,
			"cannot update YAML-sourced schedules via RPC; edit the YAML file instead")
	}

	// Update fields if provided
	if req.Pattern != nil {
		existing.Pattern = req.Pattern
	}
	if req.Schedule != nil {
		existing.Schedule = req.Schedule
	}
	existing.UpdatedAt = time.Now().Unix()

	// Update in scheduler (removes old cron entry, adds new)
	if err := sched.UpdateSchedule(ctx, existing); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update schedule: %v", err)
	}

	return &loomv1.ScheduleWorkflowResponse{
		ScheduleId: req.ScheduleId,
		Schedule:   existing,
	}, nil
}

// GetScheduledWorkflow retrieves a scheduled workflow by ID.
func (s *MultiAgentServer) GetScheduledWorkflow(ctx context.Context, req *loomv1.GetScheduledWorkflowRequest) (*loomv1.ScheduledWorkflow, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	schedule, err := sched.GetSchedule(ctx, req.ScheduleId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "schedule not found: %v", err)
	}

	return schedule, nil
}

// ListScheduledWorkflows lists all scheduled workflows.
// Can optionally filter to only enabled schedules.
func (s *MultiAgentServer) ListScheduledWorkflows(ctx context.Context, req *loomv1.ListScheduledWorkflowsRequest) (*loomv1.ListScheduledWorkflowsResponse, error) {
	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	schedules, err := sched.ListSchedules(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list schedules: %v", err)
	}

	// Filter if requested
	if req.EnabledOnly {
		filtered := make([]*loomv1.ScheduledWorkflow, 0)
		for _, sched := range schedules {
			if sched.Schedule.Enabled {
				filtered = append(filtered, sched)
			}
		}
		schedules = filtered
	}

	// TODO: Implement pagination if needed (page_size, page_token)

	return &loomv1.ListScheduledWorkflowsResponse{
		Schedules: schedules,
	}, nil
}

// DeleteScheduledWorkflow deletes a scheduled workflow.
// YAML-sourced schedules cannot be deleted via RPC - they must be removed by deleting the YAML file.
func (s *MultiAgentServer) DeleteScheduledWorkflow(ctx context.Context, req *loomv1.DeleteScheduledWorkflowRequest) (*emptypb.Empty, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	// Check if this is a YAML-sourced schedule
	existing, err := sched.GetSchedule(ctx, req.ScheduleId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "schedule not found: %v", err)
	}

	if existing.YamlPath != "" {
		return nil, status.Error(codes.FailedPrecondition,
			"cannot delete YAML-sourced schedules via RPC; remove the YAML file instead")
	}

	if err := sched.RemoveSchedule(ctx, req.ScheduleId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete schedule: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// TriggerScheduledWorkflow manually triggers a scheduled workflow immediately.
// This bypasses the cron schedule and executes the workflow right away.
func (s *MultiAgentServer) TriggerScheduledWorkflow(ctx context.Context, req *loomv1.TriggerScheduledWorkflowRequest) (*loomv1.ExecuteWorkflowResponse, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	// Trigger workflow execution
	executionID, err := sched.TriggerNow(ctx, req.ScheduleId, req.SkipIfRunning, req.Variables)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to trigger workflow: %v", err)
	}

	return &loomv1.ExecuteWorkflowResponse{
		ExecutionId: executionID,
	}, nil
}

// PauseSchedule pauses a schedule without deleting it.
// The schedule will remain in the database but will not execute until resumed.
func (s *MultiAgentServer) PauseSchedule(ctx context.Context, req *loomv1.PauseScheduleRequest) (*emptypb.Empty, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	if err := sched.PauseSchedule(ctx, req.ScheduleId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to pause schedule: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// ResumeSchedule resumes a paused schedule.
// The schedule will start executing again according to its cron expression.
func (s *MultiAgentServer) ResumeSchedule(ctx context.Context, req *loomv1.ResumeScheduleRequest) (*emptypb.Empty, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	if err := sched.ResumeSchedule(ctx, req.ScheduleId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resume schedule: %v", err)
	}

	return &emptypb.Empty{}, nil
}

// GetScheduleHistory returns execution history for a schedule.
// Includes the last N executions with their status, timing, and error information.
func (s *MultiAgentServer) GetScheduleHistory(ctx context.Context, req *loomv1.GetScheduleHistoryRequest) (*loomv1.GetScheduleHistoryResponse, error) {
	if req.ScheduleId == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_id is required")
	}

	// Check if scheduler is configured
	s.mu.RLock()
	sched := s.scheduler
	s.mu.RUnlock()

	if sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "scheduler not configured")
	}

	limit := int(req.Limit)
	if limit == 0 {
		limit = 50 // Default limit
	}

	executions, err := sched.GetHistory(ctx, req.ScheduleId, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get history: %v", err)
	}

	return &loomv1.GetScheduleHistoryResponse{
		Executions: executions,
	}, nil
}

// SubscribeToSession subscribes to real-time updates for a session.
// Streams updates when new messages arrive in the session conversation.
// This allows clients to receive asynchronous responses from workflow coordinators
// and sub-agents without polling.
func (s *MultiAgentServer) SubscribeToSession(req *loomv1.SubscribeToSessionRequest, stream loomv1.LoomService_SubscribeToSessionServer) error {
	if req.SessionId == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}

	ctx := stream.Context()

	s.logger.Info("Client subscribed to session updates",
		zap.String("session_id", req.SessionId),
		zap.String("agent_id", req.AgentId))

	// Verify session exists
	s.mu.RLock()
	sessionExists := false
	for _, ag := range s.agents {
		if _, ok := ag.GetSession(req.SessionId); ok {
			sessionExists = true
			break
		}
	}
	s.mu.RUnlock()

	if !sessionExists {
		return status.Error(codes.NotFound, "session not found")
	}

	// Track the last message count we've seen
	lastMessageCount := 0

	// Poll for new messages every 500ms (faster for better responsiveness)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug("Session subscription cancelled",
				zap.String("session_id", req.SessionId),
				zap.Error(ctx.Err()))
			return ctx.Err()

		case <-ticker.C:
			// Load all messages from database
			messages, err := s.sessionStore.LoadMessages(ctx, req.SessionId)
			if err != nil {
				s.logger.Warn("Failed to load messages for subscription",
					zap.String("session_id", req.SessionId),
					zap.Error(err))
				continue
			}

			// Check if we have new messages
			if len(messages) <= lastMessageCount {
				continue // No new messages
			}

			// Send updates for new messages only
			for i := lastMessageCount; i < len(messages); i++ {
				msg := messages[i]

				// Create session update
				update := &loomv1.SessionUpdate{
					SessionId: req.SessionId,
					AgentId:   req.AgentId, // Use requested agent ID (since messages don't have agent_id field)
					Timestamp: msg.Timestamp.Unix(),
				}

				// Populate based on message role
				if msg.Role == "assistant" || msg.Role == "user" || msg.Role == "tool" {
					update.UpdateType = &loomv1.SessionUpdate_NewMessage{
						NewMessage: &loomv1.NewMessageUpdate{
							Role:             msg.Role,
							Content:          msg.Content,
							MessageTimestamp: msg.Timestamp.Unix(),
						},
					}

					// Send to client
					if err := stream.Send(update); err != nil {
						s.logger.Warn("Failed to send session update",
							zap.String("session_id", req.SessionId),
							zap.Error(err))
						return err
					}

					s.logger.Debug("Sent session update",
						zap.String("session_id", req.SessionId),
						zap.String("role", msg.Role),
						zap.Int("message_index", i))
				}
			}

			// Update last message count
			lastMessageCount = len(messages)
		}
	}
}
