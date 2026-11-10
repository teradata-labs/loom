// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/task"
)

type noToolsDiscoveryRegistry struct {
	tool     *loomv1.IndexedTool
	searches int
}

func (r *noToolsDiscoveryRegistry) Search(context.Context, *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	r.searches++
	return &loomv1.SearchToolsResponse{Results: []*loomv1.ToolSearchResult{{Tool: r.tool}}}, nil
}

type noToolsMCPClient struct{ calls int }

func (c *noToolsMCPClient) CallTool(context.Context, string, map[string]interface{}) (interface{}, error) {
	c.calls++
	return map[string]interface{}{"value": "executed"}, nil
}

type noToolsMCPManager struct {
	client  *noToolsMCPClient
	lookups int
}

func (m *noToolsMCPManager) GetClient(string) (interface{}, error) {
	m.lookups++
	return m.client, nil
}

func TestWithoutToolsBlocksDynamicDiscovery(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "readable.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("dynamic discovery regression"), 0600))
	for _, source := range []loomv1.ToolSource{loomv1.ToolSource_TOOL_SOURCE_BUILTIN, loomv1.ToolSource_TOOL_SOURCE_MCP} {
		for _, disabled := range []bool{false, true} {
			for _, resolveOnly := range []bool{false, true} {
				name := source.String()
				if disabled {
					name += "/disabled"
				} else {
					name += "/enabled"
				}
				if resolveOnly {
					name += "/resolve"
				} else {
					name += "/execute"
				}
				t.Run(name, func(t *testing.T) {
					var opts []Option
					if disabled {
						opts = append(opts, WithoutTools())
					}
					a := NewAgent(nil, nil, opts...)
					toolName := "file_read"
					if source == loomv1.ToolSource_TOOL_SOURCE_MCP {
						toolName = "remote_read"
					}
					registry := &noToolsDiscoveryRegistry{tool: &loomv1.IndexedTool{
						Name: toolName, Source: source, McpServer: "test-server", InputSchema: `{"type":"object"}`,
					}}
					manager := &noToolsMCPManager{client: &noToolsMCPClient{}}
					a.SetToolRegistryForDynamicDiscovery(registry, manager)
					require.False(t, a.tools.IsRegistered(toolName))

					var err error
					if resolveOnly {
						var tool shuttle.Tool
						tool, err = a.executor.ResolveAndRegister(context.Background(), toolName, nil)
						if disabled {
							assert.Nil(t, tool)
						} else {
							require.NotNil(t, tool)
							assert.Equal(t, toolName, tool.Name())
						}
					} else {
						var result *shuttle.Result
						result, err = a.executor.Execute(context.Background(), toolName, map[string]interface{}{"path": filePath})
						if disabled {
							assert.Nil(t, result)
						} else {
							require.NotNil(t, result)
							assert.True(t, result.Success, "%+v", result.Error)
						}
					}
					if disabled {
						require.Error(t, err)
						assert.Zero(t, registry.searches, "disabled agents must not consult the tool catalog")
						assert.Zero(t, manager.lookups)
						assert.Zero(t, manager.client.calls)
						assert.Zero(t, a.ToolCount())
						for _, sessionID := range []string{"current-session", "other-session"} {
							assert.Empty(t, a.advertisedTools(&Session{ID: sessionID}))
						}
					} else {
						require.NoError(t, err)
						assert.Positive(t, registry.searches)
						assert.True(t, a.tools.IsRegistered(toolName))
						assert.Contains(t, advertisedNames(a, &Session{ID: "current-session"}), toolName)
						if source == loomv1.ToolSource_TOOL_SOURCE_MCP && !resolveOnly {
							assert.Equal(t, 1, manager.client.calls)
						}
					}
				})
			}
		}
	}
}

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
	noTools.Tools = &loomv1.ToolsConfig{Disabled: true}
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

func TestWithoutToolsOmitsToolPromptSupplements(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range manualModeFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	orch := skills.NewOrchestrator(skills.NewLibrary(skills.WithSearchPaths(dir)))
	mgr := task.NewManager(nil, nil, observability.NewNoOpTracer(), nil)

	cfg := DefaultConfig()
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: true}
	build := func(extra ...Option) *Agent {
		opts := append([]Option{
			WithConfig(cfg),
			WithSkillOrchestrator(orch),
			WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{Enabled: true}),
		}, extra...)
		return NewAgent(&mockBackend{}, &mockToolCallingLLM{}, opts...)
	}

	enabled := build()
	assert.Contains(t, enabled.taskBoardPromptSupplement(), "task_board")
	assert.Contains(t, enabled.skillMenuPromptSupplement(), "manage_skills")

	disabled := build(WithoutTools())
	assert.Empty(t, disabled.taskBoardPromptSupplement())
	assert.Empty(t, disabled.skillMenuPromptSupplement())
}

func TestAgent_ToolsDisabledAccessor(t *testing.T) {
	var nilAgent *Agent
	assert.False(t, nilAgent.ToolsDisabled(), "nil agent must report ToolsDisabled=false")

	ordinary := NewAgent(nil, nil)
	assert.False(t, ordinary.ToolsDisabled(), "ordinary agent must report ToolsDisabled=false")

	emptyToolList := NewAgent(nil, nil,
		WithoutBuiltinTool("query_tool_result"),
		WithoutBuiltinTool("recall"),
		WithoutBuiltinTool("load_pattern"),
	)
	require.Zero(t, emptyToolList.ToolCount(), "ordinary agent with built-in tools suppressed has zero registered tools")
	assert.False(t, emptyToolList.ToolsDisabled(), "empty tool list on an ordinary agent must not be misclassified as WithoutTools()")

	disabled := NewAgent(nil, nil, WithoutTools())
	assert.True(t, disabled.ToolsDisabled(), "agent constructed with WithoutTools() must report ToolsDisabled=true")
	assert.Zero(t, disabled.ToolCount())
}
