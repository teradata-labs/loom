// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// stubToolRegistry returns a fixed search response, standing in for the
// indexed tool catalog the executor consults during dynamic resolution.
type stubToolRegistry struct{ resp *loomv1.SearchToolsResponse }

func (s *stubToolRegistry) Search(_ context.Context, _ *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	return s.resp, nil
}

// stubMCPManager hands back a dummy client; registerMCPTool only wraps it
// into a tool definition and does not call the server at registration time.
type stubMCPManager struct{}

func (s *stubMCPManager) GetClient(_ string) (interface{}, error) { return struct{}{}, nil }

// TestEnforceRequiredSkillTools_MountsMCPToolByName proves the mcp-npd change:
// a skill that names a non-builtin (MCP) tool has it resolved from the dynamic
// registry and mounted directly — no tool_search discovery hop — and
// advertised into the requesting session. Before the change, builtin.ByName
// returned nil for such a name and the tool was dropped with a warning.
func TestEnforceRequiredSkillTools_MountsMCPToolByName(t *testing.T) {
	a := newTestAgentWithSkills(t)
	sessionID := "sess-mcp"

	const toolName = "td_run_sql" // deliberately not in builtin.ByName's set

	// Executor wired to a registry that knows the tool as an MCP tool.
	a.executor = shuttle.NewExecutor(a.tools)
	a.executor.SetToolRegistry(&stubToolRegistry{resp: &loomv1.SearchToolsResponse{
		Results: []*loomv1.ToolSearchResult{{Tool: &loomv1.IndexedTool{
			Name:        toolName,
			Description: "Run SQL on Teradata",
			Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
			McpServer:   "teradata",
			InputSchema: `{"type":"object"}`,
		}}},
	}})
	a.executor.SetMCPManager(&stubMCPManager{})

	skill := &skills.Skill{
		Name:  "td-analytics",
		Tools: skills.SkillToolConfig{RequiredTools: []string{toolName}},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	require.False(t, a.tools.IsRegistered(toolName),
		"precondition: MCP tool must not be pre-registered")

	a.enforceRequiredSkillTools(sessionID)

	// Mounted directly into the agent's tool set...
	assert.True(t, a.tools.IsRegistered(toolName),
		"skill-required MCP tool must be resolved and mounted via the dynamic registry")
	// ...and advertised into THIS session's ledger.
	assert.True(t, a.sessionToolLedger[sessionID][toolName],
		"mounted tool must be advertised into the requesting session")
}

// TestEnforceRequiredSkillTools_MCPResolutionFailure_SkipsCleanly proves the
// fallback degrades to the prior behavior (warn + skip) when a required name
// resolves nowhere: it never panics and never registers a phantom tool.
func TestEnforceRequiredSkillTools_MCPResolutionFailure_SkipsCleanly(t *testing.T) {
	a := newTestAgentWithSkills(t)
	sessionID := "sess-miss"

	a.executor = shuttle.NewExecutor(a.tools)
	// Empty results → tryDynamicRegistration returns "tool not found".
	a.executor.SetToolRegistry(&stubToolRegistry{resp: &loomv1.SearchToolsResponse{}})

	skill := &skills.Skill{
		Name:  "needs-ghost",
		Tools: skills.SkillToolConfig{RequiredTools: []string{"ghost_tool"}},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	a.enforceRequiredSkillTools(sessionID)
	assert.False(t, a.tools.IsRegistered("ghost_tool"),
		"unresolvable required tool must not be registered")
}
