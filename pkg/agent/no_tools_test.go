// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestWithoutToolsKeepsGraphMemoryButHidesAllTools(t *testing.T) {
	store := newTestGraphStore(t)
	gmCfg := DefaultGraphMemoryConfig()
	gmCfg.Enabled = true
	gmCfg.EnableExtraction = true

	ag := NewAgent(nil, nil, WithGraphMemoryStore(store, gmCfg), WithoutTools())
	assert.Same(t, store, ag.graphMemoryStore)
	assert.True(t, ag.graphMemoryConfig.Enabled)
	assert.True(t, ag.enableGraphMemoryExtraction)
	assert.Zero(t, ag.ToolCount())
	assert.Empty(t, ag.ListTools())

	ag.RegisterTool(mockTool("dynamic"))
	ag.RegisterTools(mockTool("batch"))
	ag.RegisterLazyTools([]shuttle.Tool{mockTool("lazy")}, alwaysTrigger)
	ag.evaluateLazyTools("trigger")
	ag.checkAndRegisterGraphMemoryTool()
	ag.checkAndRegisterManageSkillsTool()
	ag.checkAndRegisterLoadPatternTool()
	assert.Zero(t, ag.ToolCount(), "later registrations must not restore tools")

	ordinary := NewAgent(nil, nil, WithGraphMemoryStore(store, gmCfg))
	require.Positive(t, ordinary.ToolCount())
	assert.Contains(t, ordinary.ListTools(), "graph_memory")
	ordinary.RegisterTool(mockTool("dynamic"))
	assert.Contains(t, ordinary.ListTools(), "dynamic")
}

func TestRegistry_PerAgentNoToolsKeepsGraphMemory(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	store := newTestGraphStore(t)
	registry.SetGraphMemoryStore(store, nil)

	agentsDir := filepath.Join(tmpDir, "agents")
	noTools := createTestAgentConfig("no-tools")
	noTools.Tools = &loomv1.ToolsConfig{None: true}
	noTools.Memory.GraphMemory = &loomv1.GraphMemoryConfig{Enabled: true, EnableExtraction: true}
	require.NoError(t, SaveAgentConfig(noTools, filepath.Join(agentsDir, "no-tools.yaml")))
	ordinary := createTestAgentConfig("ordinary")
	require.NoError(t, SaveAgentConfig(ordinary, filepath.Join(agentsDir, "ordinary.yaml")))

	ctx := context.Background()
	require.NoError(t, registry.LoadAgents(ctx))
	limited, err := registry.CreateAgent(ctx, "no-tools")
	require.NoError(t, err)
	assert.Zero(t, limited.ToolCount())
	assert.Same(t, store, limited.graphMemoryStore)
	assert.True(t, limited.graphMemoryConfig.Enabled)
	assert.True(t, limited.enableGraphMemoryExtraction)

	normal, err := registry.CreateAgent(ctx, "ordinary")
	require.NoError(t, err)
	assert.Positive(t, normal.ToolCount())
	assert.Contains(t, normal.ListTools(), "graph_memory")
}
