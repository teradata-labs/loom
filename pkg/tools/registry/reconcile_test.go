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
package registry

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// fakeReconcilingIndexer implements ReconcilingIndexer with a scripted outcome.
type fakeReconcilingIndexer struct {
	name    string
	source  loomv1.ToolSource
	outcome *IndexOutcome
	err     error
}

func (f *fakeReconcilingIndexer) Name() string              { return f.name }
func (f *fakeReconcilingIndexer) Source() loomv1.ToolSource { return f.source }
func (f *fakeReconcilingIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	outcome, err := f.IndexWithOutcome(ctx)
	if err != nil {
		return nil, err
	}
	return outcome.Tools, nil
}
func (f *fakeReconcilingIndexer) IndexWithOutcome(ctx context.Context) (*IndexOutcome, error) {
	return f.outcome, f.err
}

// fakePlainIndexer implements only the base Indexer (no reconciliation).
type fakePlainIndexer struct {
	source loomv1.ToolSource
	tools  []*loomv1.IndexedTool
}

func (f *fakePlainIndexer) Name() string              { return "plain" }
func (f *fakePlainIndexer) Source() loomv1.ToolSource { return f.source }
func (f *fakePlainIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	return f.tools, nil
}

func mcpTool(server, name string) *loomv1.IndexedTool {
	return &loomv1.IndexedTool{
		Id:          fmt.Sprintf("mcp:%s:%s", server, name),
		Name:        name,
		Description: fmt.Sprintf("%s tool from %s", name, server),
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   server,
		IndexedAt:   time.Now().Format(time.RFC3339),
		Keywords:    []string{name},
	}
}

func newReconcileTestRegistry(t *testing.T, cfg Config) *Registry {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_reconcile_*.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })
	_ = tmpFile.Close()

	cfg.DBPath = tmpFile.Name()
	reg, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func toolIDs(t *testing.T, reg *Registry) map[string]bool {
	t.Helper()
	rows, err := reg.db.Query(`SELECT id FROM tools`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids[id] = true
	}
	require.NoError(t, rows.Err())
	return ids
}

func TestIndexAllReconciliation(t *testing.T) {
	serverA := []*loomv1.IndexedTool{mcpTool("alpha", "query"), mcpTool("alpha", "list")}
	serverB := []*loomv1.IndexedTool{mcpTool("beta", "read")}

	tests := []struct {
		name        string
		firstRun    *IndexOutcome
		secondRun   *IndexOutcome
		wantIDs     []string
		wantGoneIDs []string
		wantPruned  int32
	}{
		{
			name: "removed server rows are pruned",
			firstRun: &IndexOutcome{
				Tools:             append(append([]*loomv1.IndexedTool{}, serverA...), serverB...),
				CompleteScopes:    []string{"alpha", "beta"},
				KnownScopes:       []string{"alpha", "beta"},
				PruneOrphanScopes: true,
			},
			secondRun: &IndexOutcome{
				Tools:             serverA,
				CompleteScopes:    []string{"alpha"},
				KnownScopes:       []string{"alpha"},
				PruneOrphanScopes: true,
			},
			wantIDs:     []string{"mcp:alpha:query", "mcp:alpha:list"},
			wantGoneIDs: []string{"mcp:beta:read"},
			wantPruned:  1,
		},
		{
			name: "removed tool on a live server is pruned",
			firstRun: &IndexOutcome{
				Tools:             serverA,
				CompleteScopes:    []string{"alpha"},
				KnownScopes:       []string{"alpha"},
				PruneOrphanScopes: true,
			},
			secondRun: &IndexOutcome{
				Tools:             []*loomv1.IndexedTool{mcpTool("alpha", "query")},
				CompleteScopes:    []string{"alpha"},
				KnownScopes:       []string{"alpha"},
				PruneOrphanScopes: true,
			},
			wantIDs:     []string{"mcp:alpha:query"},
			wantGoneIDs: []string{"mcp:alpha:list"},
			wantPruned:  1,
		},
		{
			name: "unreachable but configured server keeps its rows",
			firstRun: &IndexOutcome{
				Tools:             append(append([]*loomv1.IndexedTool{}, serverA...), serverB...),
				CompleteScopes:    []string{"alpha", "beta"},
				KnownScopes:       []string{"alpha", "beta"},
				PruneOrphanScopes: true,
			},
			secondRun: &IndexOutcome{
				Tools:             serverA,
				CompleteScopes:    []string{"alpha"},         // beta failed to enumerate
				KnownScopes:       []string{"alpha", "beta"}, // but is still configured
				PruneOrphanScopes: true,
			},
			wantIDs:    []string{"mcp:alpha:query", "mcp:alpha:list", "mcp:beta:read"},
			wantPruned: 0,
		},
		{
			name: "no servers configured prunes every MCP row",
			firstRun: &IndexOutcome{
				Tools:             append(append([]*loomv1.IndexedTool{}, serverA...), serverB...),
				CompleteScopes:    []string{"alpha", "beta"},
				KnownScopes:       []string{"alpha", "beta"},
				PruneOrphanScopes: true,
			},
			secondRun: &IndexOutcome{
				PruneOrphanScopes: true,
			},
			wantGoneIDs: []string{"mcp:alpha:query", "mcp:alpha:list", "mcp:beta:read"},
			wantPruned:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexer := &fakeReconcilingIndexer{
				name:    "mcp",
				source:  loomv1.ToolSource_TOOL_SOURCE_MCP,
				outcome: tt.firstRun,
			}
			reg := newReconcileTestRegistry(t, Config{Indexers: []Indexer{indexer}})
			ctx := context.Background()

			resp, err := reg.IndexAll(ctx)
			require.NoError(t, err)
			require.Empty(t, resp.Errors)

			indexer.outcome = tt.secondRun
			resp, err = reg.IndexAll(ctx)
			require.NoError(t, err)
			require.Empty(t, resp.Errors)
			assert.Equal(t, tt.wantPruned, resp.PrunedCount)

			ids := toolIDs(t, reg)
			for _, id := range tt.wantIDs {
				assert.True(t, ids[id], "expected %s to survive", id)
			}
			for _, id := range tt.wantGoneIDs {
				assert.False(t, ids[id], "expected %s to be pruned", id)
			}
		})
	}
}

// A plain (non-reconciling) indexer must keep the historical upsert-only
// behavior: rows registered out-of-band (RegisterTool) survive IndexAll.
func TestIndexAllPlainIndexerDoesNotPrune(t *testing.T) {
	plain := &fakePlainIndexer{source: loomv1.ToolSource_TOOL_SOURCE_CUSTOM}
	reg := newReconcileTestRegistry(t, Config{Indexers: []Indexer{plain}})
	ctx := context.Background()

	custom := &loomv1.IndexedTool{
		Id:          "custom:my_tool",
		Name:        "my_tool",
		Description: "registered via gRPC, not reported by any indexer",
		Source:      loomv1.ToolSource_TOOL_SOURCE_CUSTOM,
		IndexedAt:   time.Now().Format(time.RFC3339),
	}
	require.NoError(t, reg.RegisterTool(ctx, custom))

	resp, err := reg.IndexAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.PrunedCount)

	ids := toolIDs(t, reg)
	assert.True(t, ids["custom:my_tool"], "out-of-band custom tool must survive IndexAll")
}

// Reconciliation is scoped per source: pruning MCP rows never touches
// builtin rows, and vice versa.
func TestIndexAllReconciliationScopedToSource(t *testing.T) {
	builtin := &fakeReconcilingIndexer{
		name:   "builtin",
		source: loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
		outcome: &IndexOutcome{
			Tools: []*loomv1.IndexedTool{{
				Id:          "builtin:echo",
				Name:        "echo",
				Description: "echo tool",
				Source:      loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
				IndexedAt:   time.Now().Format(time.RFC3339),
			}},
			CompleteScopes:    []string{""},
			KnownScopes:       []string{""},
			PruneOrphanScopes: true,
		},
	}
	mcp := &fakeReconcilingIndexer{
		name:   "mcp",
		source: loomv1.ToolSource_TOOL_SOURCE_MCP,
		outcome: &IndexOutcome{
			// No MCP servers exist: prunes all MCP rows, must not touch builtin.
			PruneOrphanScopes: true,
		},
	}
	reg := newReconcileTestRegistry(t, Config{Indexers: []Indexer{builtin, mcp}})
	ctx := context.Background()

	// Seed a stale MCP row from a long-gone server.
	require.NoError(t, reg.RegisterTool(ctx, mcpTool("ghost", "old_query")))

	resp, err := reg.IndexAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), resp.PrunedCount)

	ids := toolIDs(t, reg)
	assert.True(t, ids["builtin:echo"], "builtin row must survive MCP pruning")
	assert.False(t, ids["mcp:ghost:old_query"], "stale MCP row must be pruned")
}

func TestSearchFiltersDeadMCPServers(t *testing.T) {
	live := []string{"alpha"}
	reg := newReconcileTestRegistry(t, Config{
		LiveMCPServers: func() []string { return live },
	})
	ctx := context.Background()

	require.NoError(t, reg.RegisterTool(ctx, mcpTool("alpha", "query_database")))
	require.NoError(t, reg.RegisterTool(ctx, mcpTool("ghost", "query_tables")))
	require.NoError(t, reg.RegisterTool(ctx, &loomv1.IndexedTool{
		Id:          "builtin:query_helper",
		Name:        "query_helper",
		Description: "builtin query helper",
		Source:      loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
		IndexedAt:   time.Now().Format(time.RFC3339),
		Keywords:    []string{"query"},
	}))

	search := func() map[string]bool {
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
		return names
	}

	names := search()
	assert.True(t, names["query_database"], "tool on live server must be returned")
	assert.True(t, names["query_helper"], "builtin tool must be unaffected by MCP liveness")
	assert.False(t, names["query_tables"], "tool on dead server must be filtered out")

	// With no live servers at all, every MCP tool disappears from search
	// while builtin tools remain.
	live = nil
	names = search()
	assert.False(t, names["query_database"])
	assert.False(t, names["query_tables"])
	assert.True(t, names["query_helper"])

	// GetToolsByCapability honors the same filter.
	tools, err := reg.GetToolsByCapability(ctx, "database", nil, 10)
	require.NoError(t, err)
	for _, tool := range tools {
		assert.NotEqual(t, loomv1.ToolSource_TOOL_SOURCE_MCP, tool.Source,
			"no MCP tool may be returned when no server is live")
	}
}

func TestSearchWithoutLivenessCallbackIsUnfiltered(t *testing.T) {
	reg := newReconcileTestRegistry(t, Config{}) // LiveMCPServers nil
	ctx := context.Background()

	require.NoError(t, reg.RegisterTool(ctx, mcpTool("ghost", "query_tables")))

	resp, err := reg.Search(ctx, &loomv1.SearchToolsRequest{
		Query:      "query",
		Mode:       loomv1.SearchMode_SEARCH_MODE_FAST,
		MaxResults: 10,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "query_tables", resp.Results[0].Tool.Name)
}

func TestEvictTool(t *testing.T) {
	reg := newReconcileTestRegistry(t, Config{})
	ctx := context.Background()

	tool := mcpTool("ghost", "stale_tool")
	require.NoError(t, reg.RegisterTool(ctx, tool))

	_, err := reg.GetTool(ctx, tool.Id)
	require.NoError(t, err)

	require.NoError(t, reg.EvictTool(ctx, tool.Id))

	_, err = reg.GetTool(ctx, tool.Id)
	assert.Error(t, err, "evicted tool must no longer resolve")

	// Evicting a missing ID is a no-op, not an error.
	assert.NoError(t, reg.EvictTool(ctx, "mcp:ghost:never_existed"))
}
