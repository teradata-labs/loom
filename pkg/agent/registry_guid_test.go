// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAgentConfig is a test helper that creates and loads an agent config
func setupAgentConfig(t *testing.T, registry *Registry, tmpDir, agentName string) {
	ctx := context.Background()

	// Create agents directory
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	// Create and save config
	config := createTestAgentConfig(agentName)
	err = SaveAgentConfig(config, filepath.Join(agentsDir, agentName+".yaml"))
	require.NoError(t, err)

	// Load agents to register the config
	err = registry.LoadAgents(ctx)
	require.NoError(t, err)
}

// TestRegistry_GUID_Generation tests that agents are assigned stable UUIDs on creation.
func TestRegistry_GUID_Generation(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	_, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get agent info
	info, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)

	// Verify GUID is a valid UUID
	_, err = uuid.Parse(info.ID)
	assert.NoError(t, err, "agent ID should be a valid UUID")

	// Verify GUID is not the same as name
	assert.NotEqual(t, info.Name, info.ID, "agent ID should not be the same as name")
}

// TestRegistry_GUID_Stability tests that agent GUIDs remain stable across operations.
func TestRegistry_GUID_Stability(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	_, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get initial GUID
	info1, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)
	initialGUID := info1.ID

	// Reload agent
	err = registry.ReloadAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get GUID after reload
	info2, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)

	// Verify GUID hasn't changed
	assert.Equal(t, initialGUID, info2.ID, "agent GUID should remain stable after reload")
}

// TestRegistry_GUID_Lookup tests that agents can be looked up by both name and GUID.
func TestRegistry_GUID_Lookup(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	_, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get agent info by name
	infoByName, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)

	// Get agent info by GUID
	infoByGUID, err := registry.GetAgentInfo(infoByName.ID)
	require.NoError(t, err)

	// Verify both lookups return the same agent
	assert.Equal(t, infoByName.ID, infoByGUID.ID)
	assert.Equal(t, infoByName.Name, infoByGUID.Name)
}

// TestRegistry_GUID_Operations tests that all agent operations work with GUIDs.
func TestRegistry_GUID_Operations(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	_, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get agent GUID
	info, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)
	agentGUID := info.ID

	// Test start by GUID
	err = registry.StartAgent(ctx, agentGUID)
	require.NoError(t, err)

	// Verify status changed
	info, err = registry.GetAgentInfo(agentGUID)
	require.NoError(t, err)
	assert.Equal(t, "running", info.Status)

	// Test stop by GUID
	err = registry.StopAgent(ctx, agentGUID)
	require.NoError(t, err)

	// Verify status changed
	info, err = registry.GetAgentInfo(agentGUID)
	require.NoError(t, err)
	assert.Equal(t, "stopped", info.Status)

	// Test delete by GUID
	err = registry.DeleteAgent(ctx, agentGUID, false)
	require.NoError(t, err)

	// Verify agent no longer exists
	_, err = registry.GetAgentInfo(agentGUID)
	assert.Error(t, err, "agent should not exist after deletion")
}

// TestRegistry_GUID_Persistence tests that agent GUIDs persist across registry restarts.
func TestRegistry_GUID_Persistence(t *testing.T) {
	// Create first registry instance
	registry1, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry1, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent and get its GUID
	_, err := registry1.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	info1, err := registry1.GetAgentInfo("test-agent")
	require.NoError(t, err)
	originalGUID := info1.ID

	// Close first registry
	err = registry1.Close()
	require.NoError(t, err)

	// Create second registry instance (same database)
	dbPath := filepath.Join(tmpDir, "test_registry.db")
	registry2, err := NewRegistry(RegistryConfig{
		ConfigDir: tmpDir,
		DBPath:    dbPath,
	})
	require.NoError(t, err)
	defer registry2.Close()

	// Load agent from database
	err = registry2.LoadAgents(ctx)
	require.NoError(t, err)

	// Verify GUID persisted
	info2, err := registry2.GetAgentInfo("test-agent")
	require.NoError(t, err)
	assert.Equal(t, originalGUID, info2.ID, "agent GUID should persist across registry restarts")
}

// TestRegistry_GUID_UniquePerAgent tests that each agent gets a unique GUID.
func TestRegistry_GUID_UniquePerAgent(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)

	// Setup configs for multiple agents
	setupAgentConfig(t, registry, tmpDir, "agent-1")
	setupAgentConfig(t, registry, tmpDir, "agent-2")
	setupAgentConfig(t, registry, tmpDir, "agent-3")

	ctx := context.Background()

	// Create multiple agents
	_, err := registry.CreateAgent(ctx, "agent-1")
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "agent-2")
	require.NoError(t, err)

	_, err = registry.CreateAgent(ctx, "agent-3")
	require.NoError(t, err)

	// Get all agent infos
	info1, err := registry.GetAgentInfo("agent-1")
	require.NoError(t, err)

	info2, err := registry.GetAgentInfo("agent-2")
	require.NoError(t, err)

	info3, err := registry.GetAgentInfo("agent-3")
	require.NoError(t, err)

	// Verify all GUIDs are unique
	assert.NotEqual(t, info1.ID, info2.ID, "agent GUIDs should be unique")
	assert.NotEqual(t, info2.ID, info3.ID, "agent GUIDs should be unique")
	assert.NotEqual(t, info1.ID, info3.ID, "agent GUIDs should be unique")
}

// TestRegistry_GetAgentByID tests the GetAgentByID method.
func TestRegistry_GetAgentByID(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	_, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Get agent info by name to get GUID
	info, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)

	// Test GetAgentByID with valid GUID
	infoByID, err := registry.GetAgentByID(info.ID)
	require.NoError(t, err)
	assert.Equal(t, info.ID, infoByID.ID)

	// Test GetAgentByID with invalid GUID
	_, err = registry.GetAgentByID("invalid-guid")
	assert.Error(t, err, "GetAgentByID should fail with invalid GUID")

	// Test GetAgentByID with agent name (should fail)
	_, err = registry.GetAgentByID("test-agent")
	assert.Error(t, err, "GetAgentByID should not accept agent names")
}

// TestRegistry_EphemeralAgent_GUID tests that ephemeral agents get stable GUIDs.
func TestRegistry_EphemeralAgent_GUID(t *testing.T) {
	registry, _ := createTestRegistry(t)

	ctx := context.Background()

	// Create an ephemeral agent
	agent, err := registry.CreateEphemeralAgent(ctx, "test-role")
	require.NoError(t, err)
	require.NotNil(t, agent)

	agentName := agent.GetName()
	assert.Contains(t, agentName, "ephemeral-", "ephemeral agent should have ephemeral prefix in name")

	// Verify agent has an ID
	agentID := agent.GetID()
	assert.NotEmpty(t, agentID, "ephemeral agent should have an ID")

	// Get agent info by name
	infoByName, err := registry.GetAgentInfo(agentName)
	require.NoError(t, err)

	// Verify agent.GetID() matches registry info
	assert.Equal(t, agentID, infoByName.ID, "agent.GetID() should match registry info")

	// Verify GUID is a valid UUID
	_, err = uuid.Parse(infoByName.ID)
	assert.NoError(t, err, "ephemeral agent ID should be a valid UUID")

	// Verify GUID is not the same as name
	assert.NotEqual(t, infoByName.Name, infoByName.ID, "ephemeral agent ID should not be the same as name")

	// Get agent info by GUID
	infoByGUID, err := registry.GetAgentInfo(infoByName.ID)
	require.NoError(t, err)

	// Verify both lookups return the same agent
	assert.Equal(t, infoByName.ID, infoByGUID.ID)
	assert.Equal(t, infoByName.Name, infoByGUID.Name)
	assert.Equal(t, "running", infoByGUID.Status)
}

// TestAgent_AutoAssignID tests that NewAgent automatically assigns a UUID to every agent.
func TestAgent_AutoAssignID(t *testing.T) {
	// Create a standalone agent (not via registry)
	ag := NewAgent(nil, nil)

	// Verify agent has an ID
	id := ag.GetID()
	assert.NotEmpty(t, id, "NewAgent should automatically assign an ID")

	// Verify ID is a valid UUID
	_, err := uuid.Parse(id)
	assert.NoError(t, err, "ID should be a valid UUID v4")
}

// TestAgent_GetIDThreadSafe tests that GetID is thread-safe.
func TestAgent_GetIDThreadSafe(t *testing.T) {
	ag := NewAgent(nil, nil)

	// Verify agent has an ID
	expectedID := ag.GetID()
	assert.NotEmpty(t, expectedID, "agent should have an ID")

	// Test concurrent GetID calls
	const goroutines = 100
	done := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			id := ag.GetID()
			done <- id
		}()
	}

	// Verify all goroutines got the same ID
	for i := 0; i < goroutines; i++ {
		id := <-done
		assert.Equal(t, expectedID, id, "GetID should return consistent ID across concurrent calls")
	}
}

// TestRegistry_AgentIDPersistence tests that agent.GetID() returns stable GUID after CreateAgent.
func TestRegistry_AgentIDPersistence(t *testing.T) {
	// Create first registry instance
	registry1, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry1, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent and get its GUID
	agent1, err := registry1.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Verify agent has an ID
	agentID1 := agent1.GetID()
	assert.NotEmpty(t, agentID1, "agent should have an ID")

	// Verify agent.GetID() matches registry info
	info1, err := registry1.GetAgentInfo("test-agent")
	require.NoError(t, err)
	assert.Equal(t, agentID1, info1.ID, "agent.GetID() should match registry info")

	// Close first registry
	err = registry1.Close()
	require.NoError(t, err)

	// Create second registry instance (same database)
	dbPath := filepath.Join(tmpDir, "test_registry.db")
	registry2, err := NewRegistry(RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      dbPath,
		LLMProvider: &mockLLMProvider{}, // Add LLM provider for agent creation
	})
	require.NoError(t, err)
	defer registry2.Close()

	// Load agent from database
	err = registry2.LoadAgents(ctx)
	require.NoError(t, err)

	// Create agent again (should reuse stable GUID from database)
	agent2, err := registry2.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Verify agent.GetID() returns the same stable GUID
	agentID2 := agent2.GetID()
	assert.Equal(t, agentID1, agentID2, "agent.GetID() should return stable GUID after registry restart")

	// Verify registry info matches
	info2, err := registry2.GetAgentInfo("test-agent")
	require.NoError(t, err)
	assert.Equal(t, agentID1, info2.ID, "registry should preserve stable GUID across restarts")
}

// TestRegistry_AgentID_CreationFlow tests the complete agent ID creation flow.
func TestRegistry_AgentID_CreationFlow(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	setupAgentConfig(t, registry, tmpDir, "test-agent")

	ctx := context.Background()

	// Create an agent
	agent, err := registry.CreateAgent(ctx, "test-agent")
	require.NoError(t, err)

	// Verify agent has an ID (from NewAgent or stable GUID)
	agentID := agent.GetID()
	assert.NotEmpty(t, agentID, "agent should have an ID")

	// Verify ID is a valid UUID
	_, err = uuid.Parse(agentID)
	assert.NoError(t, err, "agent ID should be a valid UUID")

	// Verify agent.GetID() matches registry info
	info, err := registry.GetAgentInfo("test-agent")
	require.NoError(t, err)
	assert.Equal(t, agentID, info.ID, "agent.GetID() should match registry info.ID")

	// Verify ID is not the same as name
	assert.NotEqual(t, agent.GetName(), agentID, "agent ID should not be the same as name")
}
