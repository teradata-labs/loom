// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// agentState tracks lifecycle state for an agent managed by the server.
// This is separate from the agent instance itself, which may be nil when stopped.
// Fields are only accessed while holding s.mu (read or write lock).
type agentState struct {
	ID        string
	Name      string
	Status    string // "running", "stopped", "error", "initializing"
	CreatedAt time.Time
	UpdatedAt time.Time
	Config    *loomv1.AgentConfig
	Error     string // Error message if status is "error"
}

// snapshot returns a copy of the agentState for safe use outside the lock.
func (st *agentState) snapshot() agentState {
	return agentState{
		ID:        st.ID,
		Name:      st.Name,
		Status:    st.Status,
		CreatedAt: st.CreatedAt,
		UpdatedAt: st.UpdatedAt,
		Config:    st.Config,
		Error:     st.Error,
	}
}

// getOrInitAgentStates lazily initializes the agentStates map. Must be called with s.mu held.
func (s *MultiAgentServer) getOrInitAgentStates() map[string]*agentState {
	if s.agentStates == nil {
		s.agentStates = make(map[string]*agentState)
	}
	return s.agentStates
}

// buildAgentInfo constructs an AgentInfo proto from an agent and a state snapshot.
// The state parameter must be a value copy (not a pointer into the shared map) to avoid races.
func (s *MultiAgentServer) buildAgentInfo(ag *agent.Agent, agentID string, state *agentState) *loomv1.AgentInfo {
	info := &loomv1.AgentInfo{
		Id:   agentID,
		Name: ag.GetName(),
	}

	// Build metadata
	metadata := make(map[string]string)
	if ag.GetDescription() != "" {
		metadata["description"] = ag.GetDescription()
	}
	info.Metadata = metadata

	// Count active sessions
	info.ActiveSessions = types.SafeInt32(len(ag.ListSessions()))

	if state != nil {
		info.Status = state.Status
		info.CreatedAt = state.CreatedAt.Unix()
		info.UpdatedAt = state.UpdatedAt.Unix()
		info.Config = state.Config
		info.Error = state.Error

		// Calculate uptime if running
		if state.Status == "running" {
			info.UptimeSeconds = int64(time.Since(state.CreatedAt).Seconds())
		}
	} else {
		info.Status = "running"
		info.CreatedAt = time.Now().Unix()
		info.UpdatedAt = time.Now().Unix()
	}

	return info
}

// snapshotStateLocked reads and snapshots agent state. Must be called with s.mu held (read or write).
func (s *MultiAgentServer) snapshotStateLocked(agentID string) *agentState {
	if s.agentStates == nil {
		return nil
	}
	st, ok := s.agentStates[agentID]
	if !ok {
		return nil
	}
	snap := st.snapshot()
	return &snap
}

// ensureConfigDefaults fills in nil nested config fields with sensible defaults.
// This prevents nil pointer dereferences when the inline AgentConfig only sets
// top-level fields (name, description, system_prompt) without nested messages
// like Llm, Tools, Memory, or Behavior.
func ensureConfigDefaults(config *loomv1.AgentConfig) {
	if config.Llm == nil {
		config.Llm = &loomv1.LLMConfig{}
	}
	if config.Tools == nil {
		config.Tools = &loomv1.ToolsConfig{}
	}
	if config.Memory == nil {
		config.Memory = &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 50,
		}
	}
	if config.Behavior == nil {
		config.Behavior = &loomv1.BehaviorConfig{
			MaxIterations:     10,
			TimeoutSeconds:    300,
			MaxTurns:          25,
			MaxToolExecutions: 50,
		}
	}
}

// CreateAgentFromConfig creates a new agent from the provided configuration or config file path.
func (s *MultiAgentServer) CreateAgentFromConfig(ctx context.Context, req *loomv1.CreateAgentRequest) (*loomv1.AgentInfo, error) {
	if req.Config == nil && req.ConfigPath == "" {
		return nil, status.Error(codes.InvalidArgument, "either config or config_path must be provided")
	}

	var agentConfig *loomv1.AgentConfig

	if req.ConfigPath != "" {
		// Load config from file
		cfg, err := agent.LoadAgentConfig(req.ConfigPath)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "failed to load config from %s: %v", req.ConfigPath, err)
		}
		agentConfig = cfg
	} else {
		agentConfig = req.Config
	}

	// Validate config
	if err := agent.ValidateAgentConfig(agentConfig); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid agent config: %v", err)
	}

	// Ensure nested config fields have defaults to prevent nil pointer dereferences.
	// Inline configs from gRPC clients may only set top-level fields (name, description,
	// system_prompt) and leave Llm, Tools, Memory, Behavior as nil.
	ensureConfigDefaults(agentConfig)

	// If we have a registry, use it to create the agent with proper LLM provider resolution
	if s.registry != nil {
		// Register the config so the registry knows about it
		s.registry.RegisterConfig(agentConfig)

		// Create agent through registry (handles LLM provider, tools, etc.)
		ag, err := s.registry.CreateAgent(ctx, agentConfig.Name)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create agent from registry: %v", err)
		}

		agentID := ag.GetID()
		s.AddAgent(agentID, ag)

		now := time.Now()
		state := &agentState{
			ID:        agentID,
			Name:      agentConfig.Name,
			Status:    "running",
			CreatedAt: now,
			UpdatedAt: now,
			Config:    agentConfig,
		}

		s.mu.Lock()
		s.getOrInitAgentStates()[agentID] = state
		snap := state.snapshot()
		s.mu.Unlock()

		if s.logger != nil {
			s.logger.Info("Agent created from config via registry",
				zap.String("agent_id", agentID),
				zap.String("name", agentConfig.Name))
		}

		return s.buildAgentInfo(ag, agentID, &snap), nil
	}

	// Fallback: create agent directly without registry (for testing or simple setups)
	agentID := uuid.New().String()

	opts := []agent.Option{
		agent.WithName(agentConfig.Name),
	}
	if agentConfig.Description != "" {
		opts = append(opts, agent.WithDescription(agentConfig.Description))
	}
	if agentConfig.SystemPrompt != "" {
		opts = append(opts, agent.WithSystemPrompt(agentConfig.SystemPrompt))
	}

	ag := agent.NewAgent(nil, nil, opts...)
	ag.SetID(agentID)

	s.AddAgent(agentID, ag)

	now := time.Now()
	state := &agentState{
		ID:        agentID,
		Name:      agentConfig.Name,
		Status:    "running",
		CreatedAt: now,
		UpdatedAt: now,
		Config:    agentConfig,
	}

	s.mu.Lock()
	s.getOrInitAgentStates()[agentID] = state
	snap := state.snapshot()
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("Agent created from config (direct)",
			zap.String("agent_id", agentID),
			zap.String("name", agentConfig.Name))
	}

	return s.buildAgentInfo(ag, agentID, &snap), nil
}

// GetAgent retrieves information about a specific agent.
func (s *MultiAgentServer) GetAgent(ctx context.Context, req *loomv1.GetAgentRequest) (*loomv1.AgentInfo, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	ag, resolvedID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err // getAgent already returns gRPC status errors
	}

	s.mu.RLock()
	snap := s.snapshotStateLocked(resolvedID)
	s.mu.RUnlock()

	return s.buildAgentInfo(ag, resolvedID, snap), nil
}

// StartAgent starts a stopped agent, making it available for requests.
func (s *MultiAgentServer) StartAgent(ctx context.Context, req *loomv1.StartAgentRequest) (*loomv1.AgentInfo, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	ag, resolvedID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	states := s.getOrInitAgentStates()
	state, exists := states[resolvedID]
	if !exists {
		// No tracked state yet -- agent was added externally; treat as already running
		now := time.Now()
		state = &agentState{
			ID:        resolvedID,
			Name:      ag.GetName(),
			Status:    "running",
			CreatedAt: now,
			UpdatedAt: now,
		}
		states[resolvedID] = state
		snap := state.snapshot()
		s.mu.Unlock()
		return s.buildAgentInfo(ag, resolvedID, &snap), nil
	}

	if state.Status == "running" {
		// Already running, return current info
		snap := state.snapshot()
		s.mu.Unlock()
		return s.buildAgentInfo(ag, resolvedID, &snap), nil
	}

	// Transition to running
	state.Status = "running"
	state.UpdatedAt = time.Now()
	state.Error = ""
	snap := state.snapshot()
	s.mu.Unlock()

	// If registry is available, delegate start to registry for proper state tracking
	if s.registry != nil {
		if startErr := s.registry.StartAgent(ctx, resolvedID); startErr != nil {
			// Non-fatal: registry might not track this agent
			if s.logger != nil {
				s.logger.Debug("Registry.StartAgent returned error (non-fatal)",
					zap.String("agent_id", resolvedID),
					zap.Error(startErr))
			}
		}
	}

	if s.logger != nil {
		s.logger.Info("Agent started",
			zap.String("agent_id", resolvedID),
			zap.String("name", ag.GetName()))
	}

	return s.buildAgentInfo(ag, resolvedID, &snap), nil
}

// StopAgent stops a running agent. The agent remains registered but will not process requests.
func (s *MultiAgentServer) StopAgent(ctx context.Context, req *loomv1.StopAgentRequest) (*loomv1.AgentInfo, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	ag, resolvedID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	states := s.getOrInitAgentStates()
	state, exists := states[resolvedID]
	if !exists {
		now := time.Now()
		state = &agentState{
			ID:        resolvedID,
			Name:      ag.GetName(),
			Status:    "running",
			CreatedAt: now,
			UpdatedAt: now,
		}
		states[resolvedID] = state
	}

	if state.Status == "stopped" {
		snap := state.snapshot()
		s.mu.Unlock()
		return s.buildAgentInfo(ag, resolvedID, &snap), nil
	}

	state.Status = "stopped"
	state.UpdatedAt = time.Now()
	snap := state.snapshot()
	s.mu.Unlock()

	// If registry is available, delegate stop to registry
	if s.registry != nil {
		if stopErr := s.registry.StopAgent(ctx, resolvedID); stopErr != nil {
			if s.logger != nil {
				s.logger.Debug("Registry.StopAgent returned error (non-fatal)",
					zap.String("agent_id", resolvedID),
					zap.Error(stopErr))
			}
		}
	}

	if s.logger != nil {
		s.logger.Info("Agent stopped",
			zap.String("agent_id", resolvedID),
			zap.String("name", ag.GetName()))
	}

	return s.buildAgentInfo(ag, resolvedID, &snap), nil
}

// DeleteAgent removes an agent from the server.
// If force is false and the agent is running, returns FailedPrecondition.
// If force is true, the agent is stopped first then deleted.
func (s *MultiAgentServer) DeleteAgent(ctx context.Context, req *loomv1.DeleteAgentRequest) (*loomv1.DeleteAgentResponse, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	_, resolvedID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	states := s.getOrInitAgentStates()
	state := states[resolvedID]

	// Check if agent is running and force is not set
	isRunning := state == nil || state.Status == "running"
	if isRunning && !req.Force {
		s.mu.Unlock()
		return nil, status.Errorf(codes.FailedPrecondition,
			"agent %s is running; set force=true to delete a running agent", resolvedID)
	}
	s.mu.Unlock()

	// If force and running, stop first
	if isRunning && req.Force {
		if s.registry != nil {
			_ = s.registry.StopAgent(ctx, resolvedID)
		}
	}

	// Remove from agents map
	if removeErr := s.RemoveAgent(resolvedID); removeErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove agent: %v", removeErr)
	}

	// Clean up state tracking and hot-reloaders in a single lock
	s.mu.Lock()
	delete(states, resolvedID)
	if s.hotReloaders != nil {
		if hr, ok := s.hotReloaders[resolvedID]; ok {
			_ = hr.Stop()
			delete(s.hotReloaders, resolvedID)
		}
	}
	s.mu.Unlock()

	// If registry available, delegate delete for database cleanup
	if s.registry != nil {
		if delErr := s.registry.DeleteAgent(ctx, resolvedID, true); delErr != nil {
			if s.logger != nil {
				s.logger.Debug("Registry.DeleteAgent returned error (non-fatal, already removed from server)",
					zap.String("agent_id", resolvedID),
					zap.Error(delErr))
			}
		}
	}

	if s.logger != nil {
		s.logger.Info("Agent deleted",
			zap.String("agent_id", resolvedID),
			zap.Bool("force", req.Force))
	}

	return &loomv1.DeleteAgentResponse{
		Success: true,
		AgentId: resolvedID,
	}, nil
}

// ReloadAgent hot-reloads an agent's configuration.
// If reload_from_file is true, the agent's config is re-read from disk.
// If config is provided, it replaces the current configuration.
func (s *MultiAgentServer) ReloadAgent(ctx context.Context, req *loomv1.ReloadAgentRequest) (*loomv1.AgentInfo, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	ag, resolvedID, err := s.getAgent(req.AgentId)
	if err != nil {
		return nil, err
	}

	// Determine the new config to apply
	var newConfig *loomv1.AgentConfig

	if req.ReloadFromFile {
		// Delegate to registry for file-based reload
		if s.registry != nil {
			if reloadErr := s.registry.ReloadAgent(ctx, resolvedID); reloadErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to reload agent from file: %v", reloadErr)
			}

			// Retrieve updated agent info from registry
			info, infoErr := s.registry.GetAgentInfo(resolvedID)
			if infoErr == nil {
				newConfig = s.registry.GetConfig(info.Name)
			}
		} else {
			return nil, status.Error(codes.FailedPrecondition, "reload_from_file requires an agent registry; no registry configured")
		}
	} else if req.Config != nil {
		newConfig = req.Config

		// Validate the new config
		if err := agent.ValidateAgentConfig(newConfig); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid agent config: %v", err)
		}

		// Ensure nested config fields have defaults to prevent nil pointer dereferences.
		ensureConfigDefaults(newConfig)

		// If registry is available, use it for proper rebuild
		if s.registry != nil {
			s.registry.RegisterConfig(newConfig)

			// Remove the existing agent from the registry's runtime map so
			// CreateAgent does not fail with "already running". This only
			// clears the in-memory entry; database records are preserved.
			s.registry.RemoveAgentRuntime(newConfig.Name)

			newAgent, createErr := s.registry.CreateAgent(ctx, newConfig.Name)
			if createErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to rebuild agent: %v", createErr)
			}

			// Set the same ID for consistency
			newAgent.SetID(resolvedID)

			if updateErr := s.UpdateAgent(resolvedID, newAgent); updateErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to update agent: %v", updateErr)
			}

			ag = newAgent
		} else {
			// Simple reload without registry: create new agent with updated opts
			opts := []agent.Option{
				agent.WithName(newConfig.Name),
			}
			if newConfig.Description != "" {
				opts = append(opts, agent.WithDescription(newConfig.Description))
			}
			if newConfig.SystemPrompt != "" {
				opts = append(opts, agent.WithSystemPrompt(newConfig.SystemPrompt))
			}

			newAgent := agent.NewAgent(nil, nil, opts...)
			newAgent.SetID(resolvedID)

			if updateErr := s.UpdateAgent(resolvedID, newAgent); updateErr != nil {
				return nil, status.Errorf(codes.Internal, "failed to update agent: %v", updateErr)
			}

			ag = newAgent
		}
	} else {
		return nil, status.Error(codes.InvalidArgument, "either config or reload_from_file must be specified")
	}

	// Update state tracking and snapshot under lock
	now := time.Now()
	s.mu.Lock()
	states := s.getOrInitAgentStates()
	state, exists := states[resolvedID]
	if exists {
		state.UpdatedAt = now
		if newConfig != nil {
			state.Config = newConfig
		}
	} else {
		state = &agentState{
			ID:        resolvedID,
			Name:      ag.GetName(),
			Status:    "running",
			CreatedAt: now,
			UpdatedAt: now,
			Config:    newConfig,
		}
		states[resolvedID] = state
	}
	snap := state.snapshot()
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("Agent reloaded",
			zap.String("agent_id", resolvedID),
			zap.String("name", ag.GetName()),
			zap.Bool("from_file", req.ReloadFromFile))
	}

	return s.buildAgentInfo(ag, resolvedID, &snap), nil
}
