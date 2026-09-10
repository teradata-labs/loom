// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newLivenessTestManager builds an unstarted manager with one enabled and one
// disabled server, mirroring a config where a server was turned off but its
// tool index rows still exist.
func newLivenessTestManager(t *testing.T) *manager.Manager {
	t.Helper()
	mgr, err := manager.NewManager(manager.Config{
		Servers: map[string]manager.ServerConfig{
			"alpha":    {Enabled: true, Transport: "stdio", Command: "echo"},
			"disabled": {Enabled: false, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}, zap.NewNop())
	require.NoError(t, err)
	return mgr
}

// TestEnabledMCPServerNamesSkipsDisabled verifies the liveness callback's
// helper reports only enabled servers: a disabled server's stale index rows
// would pass the search filter, yet its tools can never execute.
func TestEnabledMCPServerNamesSkipsDisabled(t *testing.T) {
	mgr := newLivenessTestManager(t)

	core, observed := observer.New(zapcore.DebugLevel)
	names := enabledMCPServerNames(mgr, zap.New(core))
	assert.ElementsMatch(t, []string{"alpha"}, names,
		"only enabled servers may be reported live")

	// The exclusion is logged so filtered-out tools stay traceable.
	entries := observed.FilterMessage("Excluding disabled MCP servers from tool search").All()
	require.Len(t, entries, 1)
	assert.Equal(t, []interface{}{"disabled"}, entries[0].ContextMap()["disabled_servers"])

	// A nil logger must not panic.
	assert.ElementsMatch(t, []string{"alpha"}, enabledMCPServerNames(mgr, nil))
}

// TestDisabledServerToolsFilteredFromSearch wires the helper into a tool
// registry the way serve does and verifies a disabled server's indexed tools
// never surface in search results, while an enabled server's tools do.
func TestDisabledServerToolsFilteredFromSearch(t *testing.T) {
	mgr := newLivenessTestManager(t)
	ctx := context.Background()

	reg, err := toolregistry.New(toolregistry.Config{
		DBPath: ":memory:",
		LiveMCPServers: func() []string {
			return enabledMCPServerNames(mgr, nil)
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	indexTool := func(server, name string) {
		require.NoError(t, reg.RegisterTool(ctx, &loomv1.IndexedTool{
			Id:          "mcp:" + server + ":" + name,
			Name:        name,
			Description: name + " tool from " + server,
			Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
			McpServer:   server,
			IndexedAt:   time.Now().Format(time.RFC3339),
			Keywords:    []string{"query"},
		}))
	}
	indexTool("alpha", "query_live")
	indexTool("disabled", "query_disabled")

	resp, err := reg.Search(ctx, &loomv1.SearchToolsRequest{
		Query:      "query",
		Mode:       loomv1.SearchMode_SEARCH_MODE_FAST,
		MaxResults: 10,
	})
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, res := range resp.Results {
		names[res.Tool.Name] = true
	}
	assert.True(t, names["query_live"], "enabled server's tool must be searchable")
	assert.False(t, names["query_disabled"],
		"disabled server's tool must be filtered from search: it can never execute")
}
