// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
)

// indexMCPTool registers an MCP tool into a real fts5 registry exactly the way
// loom indexes one: bare Name (the search key) + server-qualified Id.
func indexMCPTool(t *testing.T, reg *toolregistry.Registry, server, name string) {
	t.Helper()
	require.NoError(t, reg.RegisterTool(context.Background(), &loomv1.IndexedTool{
		Id:          "mcp:" + server + ":" + name,
		Name:        name,
		Description: name + " on " + server,
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   server,
		InputSchema: `{"type":"object"}`,
	}))
}

// agentWithRealRegistry wires a hand-built Agent to a real fts5 tool registry +
// a stub MCP manager (the manager only hands back a client; registerMCPTool
// wraps it, it never calls the server at registration time).
func agentWithRealRegistry(t *testing.T, reg *toolregistry.Registry) *Agent {
	t.Helper()
	a := newTestAgentWithSkills(t)
	a.executor = shuttle.NewExecutor(a.tools)
	a.executor.SetToolRegistry(reg)
	a.executor.SetMCPManager(&stubMCPManager{})
	return a
}

// TestSkillMount_E2E_RealSkill is the end-to-end proof: the SHIPPED
// teradata-sql-analytics.yaml is loaded through the real loader, activated, and
// enforced against a real fts5 registry. Every MCP tool the skill declares in
// required_tools must be resolved server-qualified and mounted directly, with
// no tool_search discovery hop, and advertised into the session.
func TestSkillMount_E2E_RealSkill(t *testing.T) {
	// 1. Load the REAL shipped skill via the REAL loader (proves the publish
	//    path carries required_tools + mcp_servers to Skill.Tools).
	skill, err := skills.LoadSkill("../../skills/teradata-sql-analytics.yaml")
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]string{"execute_sql", "explain_query", "get_syntax_help"},
		skill.Tools.RequiredTools,
		"the shipped skill must declare its MCP tools in required_tools (not preferred_order)")
	require.Contains(t, skill.Tools.MCPServers, "teradata",
		"the shipped skill must declare its MCP server")

	// 2. REAL fts5 registry, indexed with the teradata MCP tools.
	reg, err := toolregistry.New(toolregistry.Config{DBPath: ":memory:"})
	require.NoError(t, err)
	defer reg.Close()
	for _, name := range skill.Tools.RequiredTools {
		indexMCPTool(t, reg, "teradata", name)
	}

	a := agentWithRealRegistry(t, reg)
	sessionID := "sess-e2e"
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	for _, name := range skill.Tools.RequiredTools {
		require.False(t, a.tools.IsRegistered(name), "precondition: %s not pre-registered", name)
	}

	// 3. The path under test.
	a.enforceRequiredSkillTools(sessionID)

	// 4. All three mounted directly AND advertised into this session.
	for _, name := range skill.Tools.RequiredTools {
		assert.True(t, a.tools.IsRegistered(name),
			"%s must be resolved+mounted from the real registry", name)
		assert.True(t, a.sessionToolLedger[sessionID][name],
			"%s must be advertised into the session ledger", name)
	}
}

// TestSkillMount_E2E_ServerFilter proves server binding: a skill declaring
// mcp_servers:[teradata] must NOT mount a same-named tool that lives only on a
// different server.
func TestSkillMount_E2E_ServerFilter(t *testing.T) {
	reg, err := toolregistry.New(toolregistry.Config{DBPath: ":memory:"})
	require.NoError(t, err)
	defer reg.Close()
	indexMCPTool(t, reg, "other", "execute_sql") // exists ONLY on the undeclared server

	a := agentWithRealRegistry(t, reg)
	sessionID := "sess-filter"
	a.skillOrchestrator.ActivateSkill(sessionID, &skills.Skill{
		Name: "td-only",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"execute_sql"},
			MCPServers:    []string{"teradata"},
		},
	}, "test", "", 1.0)

	a.enforceRequiredSkillTools(sessionID)

	assert.False(t, a.tools.IsRegistered("execute_sql"),
		"execute_sql on an undeclared server must not be mounted")
}

// TestSkillMount_E2E_ExactName proves the exact-name guard: a fuzzy FTS hit on a
// near name must be rejected, not silently mounted.
func TestSkillMount_E2E_ExactName(t *testing.T) {
	reg, err := toolregistry.New(toolregistry.Config{DBPath: ":memory:"})
	require.NoError(t, err)
	defer reg.Close()
	indexMCPTool(t, reg, "teradata", "execute_sql_v2") // near name, NOT the requested one

	a := agentWithRealRegistry(t, reg)
	sessionID := "sess-exact"
	a.skillOrchestrator.ActivateSkill(sessionID, &skills.Skill{
		Name: "typo",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"execute_sql"},
			MCPServers:    []string{"teradata"},
		},
	}, "test", "", 1.0)

	a.enforceRequiredSkillTools(sessionID)

	assert.False(t, a.tools.IsRegistered("execute_sql"),
		"a request for execute_sql must not fuzzy-mount execute_sql_v2")
	assert.False(t, a.tools.IsRegistered("execute_sql_v2"),
		"the near-name tool must not be mounted under any name")
}

// TestSkillMount_E2E_HostResolverWins proves the host hook takes precedence over
// the executor's own resolver when installed (the cloud path).
func TestSkillMount_E2E_HostResolverWins(t *testing.T) {
	a := newTestAgentWithSkills(t)
	// No executor registry wired at all — only the host resolver can mount.
	a.executor = shuttle.NewExecutor(a.tools)

	var gotName string
	var gotServers []string
	a.SetSkillMCPResolver(func(_ context.Context, name string, servers []string) error {
		gotName, gotServers = name, servers
		a.tools.Register(&shuttle.MockTool{MockName: name})
		return nil
	})

	sessionID := "sess-hook"
	a.skillOrchestrator.ActivateSkill(sessionID, &skills.Skill{
		Name: "hosted",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"execute_sql"},
			MCPServers:    []string{"teradata"},
		},
	}, "test", "", 1.0)

	a.enforceRequiredSkillTools(sessionID)

	assert.Equal(t, "execute_sql", gotName, "host resolver must receive the tool name")
	assert.Equal(t, []string{"teradata"}, gotServers, "host resolver must receive declared servers")
	assert.True(t, a.tools.IsRegistered("execute_sql"), "host-resolved tool must be mounted")
	assert.True(t, a.sessionToolLedger[sessionID]["execute_sql"], "host-resolved tool must be advertised")
}
