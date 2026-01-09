// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/patterns"
	"go.uber.org/zap"
)

// TestCreatePattern_WithHotReload tests end-to-end pattern creation with hot-reload.
// This is a comprehensive integration test that verifies:
// 1. Pattern creation via CreatePattern RPC
// 2. Atomic file writing
// 3. Hot-reload detection
// 4. Pattern availability after creation
func TestCreatePattern_WithHotReload(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock backend and LLM
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	// Create agent with patterns directory
	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		Description: "Test agent for hot-reload",
		PatternsDir: tmpDir,
	}))

	// Get the pattern library from the agent's orchestrator
	orchestrator := ag.GetOrchestrator()
	library := orchestrator.GetLibrary()

	// Create hot-reloader for the library
	hotReloader, err := patterns.NewHotReloader(library, patterns.HotReloadConfig{
		Enabled:    true,
		DebounceMs: 100, // Fast for testing
		Logger:     zap.NewNop(),
	})
	require.NoError(t, err)

	// Start hot-reload watcher
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = hotReloader.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = hotReloader.Stop() }()

	// Wait for watcher to be ready
	time.Sleep(200 * time.Millisecond)

	// Create server
	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}
	server := NewMultiAgentServer(agents, nil)

	// Step 1: Create pattern via RPC
	patternYAML := `name: hotreload_integration_test
title: Hot Reload Integration Test
description: End-to-end test of pattern creation with hot-reload
category: testing
difficulty: beginner
backend_type: sql
templates:
  default:
    description: Test query
    content: SELECT 'Integration test passed!' AS result
`

	req := &loomv1.CreatePatternRequest{
		AgentId:     "test-agent",
		Name:        "hotreload_integration_test",
		YamlContent: patternYAML,
	}

	resp, err := server.CreatePattern(ctx, req)
	require.NoError(t, err)
	require.True(t, resp.Success, "CreatePattern should succeed")
	assert.Equal(t, "hotreload_integration_test", resp.PatternName)

	// Step 2: Wait for hot-reload to detect the new file
	time.Sleep(500 * time.Millisecond) // Debounce (100ms) + processing

	// Step 3: Verify pattern is available via library
	pattern, err := library.Load("hotreload_integration_test")
	require.NoError(t, err, "Pattern should be loadable after hot-reload")
	assert.Equal(t, "Hot Reload Integration Test", pattern.Title)
	assert.Equal(t, "testing", pattern.Category)
	assert.Equal(t, "beginner", pattern.Difficulty)
	assert.Contains(t, pattern.Templates, "default")
}

// TestCreatePattern_WithHotReload_InvalidPattern tests that invalid patterns
// are rejected and don't break the system.
func TestCreatePattern_WithHotReload_InvalidPattern(t *testing.T) {
	tmpDir := t.TempDir()

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		PatternsDir: tmpDir,
	}))

	orchestrator := ag.GetOrchestrator()
	library := orchestrator.GetLibrary()

	// Create hot-reloader
	hotReloader, err := patterns.NewHotReloader(library, patterns.HotReloadConfig{
		Enabled:    true,
		DebounceMs: 100,
		Logger:     zap.NewNop(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = hotReloader.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = hotReloader.Stop() }()

	time.Sleep(200 * time.Millisecond)

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}
	server := NewMultiAgentServer(agents, nil)

	// Create valid pattern first
	validYAML := `name: valid_pattern
title: Valid Pattern
description: A valid pattern
category: testing
templates:
  default:
    content: SELECT 1
`

	req := &loomv1.CreatePatternRequest{
		AgentId:     "test-agent",
		Name:        "valid_pattern",
		YamlContent: validYAML,
	}

	resp, err := server.CreatePattern(ctx, req)
	require.NoError(t, err)
	require.True(t, resp.Success)

	// Wait for hot-reload
	time.Sleep(500 * time.Millisecond)

	// Verify valid pattern is loaded
	pattern, err := library.Load("valid_pattern")
	require.NoError(t, err)
	assert.Equal(t, "Valid Pattern", pattern.Title)

	// Now try creating an invalid pattern (malformed YAML that can't be parsed)
	invalidYAML := `name: invalid_pattern
title: Invalid Pattern
description: Malformed YAML
templates:
  default:
    content: SELECT 1
    invalid_indent_here
this_breaks_yaml_syntax
`

	req2 := &loomv1.CreatePatternRequest{
		AgentId:     "test-agent",
		Name:        "invalid_pattern",
		YamlContent: invalidYAML,
	}

	_, err = server.CreatePattern(ctx, req2)
	require.NoError(t, err)
	// File will be created, but hot-reload will reject it during validation

	// Wait for hot-reload attempt
	time.Sleep(500 * time.Millisecond)

	// Invalid pattern should not be loadable
	_, err = library.Load("invalid_pattern")
	assert.Error(t, err, "Invalid pattern should not be loadable")

	// Valid pattern should still be available (not corrupted)
	pattern, err = library.Load("valid_pattern")
	require.NoError(t, err)
	assert.Equal(t, "Valid Pattern", pattern.Title)
}

// TestCreatePattern_MultipleAgents tests pattern creation for different agents
// with separate pattern directories.
func TestCreatePattern_MultipleAgents(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	agent1 := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "agent1",
		PatternsDir: tmpDir1,
	}))

	agent2 := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "agent2",
		PatternsDir: tmpDir2,
	}))

	agents := map[string]*agent.Agent{
		"agent1": agent1,
		"agent2": agent2,
	}

	server := NewMultiAgentServer(agents, nil)

	// Create pattern for agent1
	pattern1YAML := `name: agent1_pattern
title: Agent 1 Pattern
description: Pattern for agent 1
category: testing
templates:
  default:
    content: SELECT 'Agent 1'
`

	req1 := &loomv1.CreatePatternRequest{
		AgentId:     "agent1",
		Name:        "agent1_pattern",
		YamlContent: pattern1YAML,
	}

	resp1, err := server.CreatePattern(context.Background(), req1)
	require.NoError(t, err)
	require.True(t, resp1.Success)
	assert.Contains(t, resp1.FilePath, tmpDir1)

	// Create pattern for agent2
	pattern2YAML := `name: agent2_pattern
title: Agent 2 Pattern
description: Pattern for agent 2
category: testing
templates:
  default:
    content: SELECT 'Agent 2'
`

	req2 := &loomv1.CreatePatternRequest{
		AgentId:     "agent2",
		Name:        "agent2_pattern",
		YamlContent: pattern2YAML,
	}

	resp2, err := server.CreatePattern(context.Background(), req2)
	require.NoError(t, err)
	require.True(t, resp2.Success)
	assert.Contains(t, resp2.FilePath, tmpDir2)

	// Verify patterns went to different directories
	assert.NotEqual(t, tmpDir1, tmpDir2)
	assert.Contains(t, resp1.FilePath, tmpDir1)
	assert.Contains(t, resp2.FilePath, tmpDir2)
}
