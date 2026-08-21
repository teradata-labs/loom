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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// stringsField extracts a zap.Strings field value from an observed log entry.
func stringsField(t *testing.T, entry observer.LoggedEntry, key string) []string {
	t.Helper()
	raw, ok := entry.ContextMap()[key]
	require.True(t, ok, "field %s not found on entry %q", key, entry.Message)
	items, ok := raw.([]interface{})
	require.True(t, ok, "field %s must be an array, got %T", key, raw)
	values := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		require.True(t, ok, "field %s must contain strings, got %T", key, item)
		values = append(values, s)
	}
	return values
}

// TestPruneStaleRowsLogsPrunedIDs verifies the prune path records exactly
// which tool IDs and servers it removed, so a tool disappearing from search
// is traceable to the reconciliation decision that deleted it.
func TestPruneStaleRowsLogsPrunedIDs(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)

	serverA := []*loomv1.IndexedTool{mcpTool("alpha", "query"), mcpTool("alpha", "list")}
	serverB := []*loomv1.IndexedTool{mcpTool("beta", "read")}
	indexer := &fakeReconcilingIndexer{
		name:   "mcp",
		source: loomv1.ToolSource_TOOL_SOURCE_MCP,
		outcome: &IndexOutcome{
			Tools:             append(append([]*loomv1.IndexedTool{}, serverA...), serverB...),
			CompleteScopes:    []string{"alpha", "beta"},
			KnownScopes:       []string{"alpha", "beta"},
			PruneOrphanScopes: true,
		},
	}
	reg := newReconcileTestRegistry(t, Config{
		Logger:   zap.New(core),
		Indexers: []Indexer{indexer},
	})
	ctx := context.Background()

	_, err := reg.IndexAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, observed.FilterMessage("Pruned stale tool index entries").All(),
		"a run that prunes nothing must not log a prune entry")

	// Second run: server beta was removed and tool alpha:list disappeared.
	indexer.outcome = &IndexOutcome{
		Tools:             []*loomv1.IndexedTool{mcpTool("alpha", "query")},
		CompleteScopes:    []string{"alpha"},
		KnownScopes:       []string{"alpha"},
		PruneOrphanScopes: true,
	}
	resp, err := reg.IndexAll(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(2), resp.PrunedCount)

	entries := observed.FilterMessage("Pruned stale tool index entries").All()
	require.Len(t, entries, 2, "one entry per prune reason")

	var allIDs, allServers []string
	for _, entry := range entries {
		allIDs = append(allIDs, stringsField(t, entry, "tool_ids")...)
		allServers = append(allServers, stringsField(t, entry, "mcp_servers")...)
	}
	assert.ElementsMatch(t, []string{"mcp:beta:read", "mcp:alpha:list"}, allIDs,
		"every pruned tool ID must be logged")
	assert.ElementsMatch(t, []string{"beta", "alpha"}, allServers,
		"every affected server must be logged")
}

// TestEvictToolLogsEviction verifies EvictTool records which entry it removed
// and stays silent for no-op evictions of unknown IDs.
func TestEvictToolLogsEviction(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	reg := newReconcileTestRegistry(t, Config{Logger: zap.New(core)})
	ctx := context.Background()

	tool := mcpTool("ghost", "stale_tool")
	require.NoError(t, reg.RegisterTool(ctx, tool))
	require.NoError(t, reg.EvictTool(ctx, tool.Id))

	entries := observed.FilterMessage("Evicted stale tool index entry").All()
	require.Len(t, entries, 1)
	assert.Equal(t, tool.Id, entries[0].ContextMap()["tool_id"])

	// Evicting a missing ID deletes nothing and must not log.
	require.NoError(t, reg.EvictTool(ctx, "mcp:ghost:never_existed"))
	assert.Len(t, observed.FilterMessage("Evicted stale tool index entry").All(), 1)
}
