// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package agent

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
)

// TestToolSurface_Only6InContext_Not47 is the economic proof: the full
// enterprise catalog (the 47 tools the live --profile all server exposes) is
// indexed and discoverable, but after the td-data-stats skill activates, the
// tool list actually sent to the LLM (advertisedTools) contains ONLY the 6 the
// skill declared — not all 47. The other 41 stay in the index, out of context.
func TestToolSurface_Only6InContext_Not47(t *testing.T) {
	ctx := context.Background()

	// The real live enterprise catalog (verified against the up ClearScape server).
	all47 := []string{
		"sql_Analyze_Cluster_Stats", "sql_Execute_Full_Pipeline", "sql_Retrieve_Cluster_Queries",
		"plot_line_chart", "plot_pie_chart", "plot_polar_chart", "plot_radar_chart",
		"base_columnMetadata", "base_readQuery", "base_saveDDL",
		"graph_analyseDatabase", "graph_bfsLevels", "graph_connectedComponents", "graph_detectCycles",
		"graph_edgeContractDDL", "graph_findRootObjects", "graph_traceLineage", "rag_Execute_Workflow",
		"base_databaseList", "base_tableList", "base_tableDDL", "base_columnDescription",
		"base_tablePreview", "base_tableAffinity", "base_tableUsage",
		"qlty_missingValues", "qlty_negativeValues", "qlty_distinctCategories", "qlty_standardDeviation",
		"qlty_columnSummary", "qlty_univariateStatistics", "qlty_rowsWithMissingValues",
		"sec_userDbPermissions", "sec_rolePermissions", "sec_userRoles",
		"dba_tableSpace", "dba_tableSqlList", "dba_userSqlList", "dba_databaseSpace", "dba_tableUsageImpact",
		"dba_resusageSummary", "dba_databaseVersion", "dba_flowControl", "dba_featureUsage",
		"dba_userDelay", "dba_sessionInfo", "dba_systemSpace",
	}
	require.Len(t, all47, 47)

	required6 := []string{
		"base_tableDDL", "base_tablePreview", "base_readQuery", "base_tableList",
		"qlty_univariateStatistics", "qlty_columnSummary",
	}

	// Index the ENTIRE catalog — all 47 are available/discoverable.
	reg, err := toolregistry.New(toolregistry.Config{DBPath: ":memory:"})
	require.NoError(t, err)
	defer reg.Close()
	for _, n := range all47 {
		require.NoError(t, reg.RegisterTool(ctx, &loomv1.IndexedTool{
			Id: "mcp:teradata:" + n, Name: n, Source: loomv1.ToolSource_TOOL_SOURCE_MCP,
			McpServer: "teradata", Description: n, InputSchema: `{"type":"object"}`,
		}))
	}

	a := newTestAgentWithSkills(t)
	a.executor = shuttle.NewExecutor(a.tools)
	a.executor.SetToolRegistry(reg)
	a.executor.SetMCPManager(&stubMCPManager{})

	sid := "sess-surface"
	a.skillOrchestrator.ActivateSkill(sid, &skills.Skill{
		Name:  "td-data-stats",
		Tools: skills.SkillToolConfig{RequiredTools: required6, MCPServers: []string{"teradata"}},
	}, "test", "", 1.0)

	a.enforceRequiredSkillTools(sid)

	// What the LLM actually sees this turn.
	adv := a.advertisedTools(&Session{ID: sid})
	var names []string
	for _, tl := range adv {
		names = append(names, tl.Name())
	}
	sort.Strings(names)
	t.Logf("catalog indexed (discoverable): %d", len(all47))
	t.Logf("loaded into LLM context:        %d  %v", len(names), names)

	assert.Len(t, adv, 6, "only the skill's 6 tools may be in context, not 47")
	assert.ElementsMatch(t, required6, names)

	// Explicitly: none of the other 41 leaked into context.
	inReq := map[string]bool{}
	for _, n := range required6 {
		inReq[n] = true
	}
	for _, n := range all47 {
		if !inReq[n] {
			assert.NotContains(t, names, n, "%s must stay out of context (index-only)", n)
		}
	}
}
