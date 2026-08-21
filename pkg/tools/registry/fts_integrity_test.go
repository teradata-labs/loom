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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ftsIntegrityCheck runs FTS5's integrity-check command against the tools_fts
// index. A non-nil error means the index disagrees with the tools content
// table (e.g. ghost postings left by INSERT OR REPLACE).
func ftsIntegrityCheck(reg *Registry) error {
	_, err := reg.db.Exec(`INSERT INTO tools_fts(tools_fts) VALUES('integrity-check')`)
	return err
}

// searchToolNames runs a FAST-mode search and returns the names of all tools
// it surfaced.
func searchToolNames(t *testing.T, reg *Registry, query string) []string {
	t.Helper()
	resp, err := reg.Search(context.Background(), &loomv1.SearchToolsRequest{
		Query:      query,
		Mode:       loomv1.SearchMode_SEARCH_MODE_FAST,
		MaxResults: 10,
	})
	require.NoError(t, err)
	names := make([]string, 0, len(resp.Results))
	for _, res := range resp.Results {
		names = append(names, res.Tool.Name)
	}
	return names
}

// TestUpsertReindexEvictReuseKeepsFTSConsistent reproduces the production
// sequence that corrupted the FTS index when upsertTool used INSERT OR
// REPLACE: index tool A, re-index A (the REPLACE path deleted A's row without
// firing tools_ad, leaving ghost postings on the freed rowid), evict A, then
// index a new tool B. SQLite reuses the freed max rowid for B, so B landed on
// A's ghost postings: searching A's name returned B and integrity-check
// reported corruption. With the ON CONFLICT upsert the rowid stays stable and
// the index stays consistent.
func TestUpsertReindexEvictReuseKeepsFTSConsistent(t *testing.T) {
	reg := newReconcileTestRegistry(t, Config{})
	ctx := context.Background()

	toolA := mcpTool("alpha", "alpha_lookup")

	// Index A, then re-index it (the step that used to plant the ghost).
	require.NoError(t, reg.RegisterTool(ctx, toolA))
	require.NoError(t, reg.RegisterTool(ctx, toolA))

	// Evict A, freeing its rowid, then index a brand-new tool B, which
	// reuses it.
	require.NoError(t, reg.EvictTool(ctx, toolA.Id))
	toolB := mcpTool("beta", "beta_report")
	require.NoError(t, reg.RegisterTool(ctx, toolB))

	// Searching A's name must not return B (pre-fix it did, via the ghost
	// postings attached to B's reused rowid) — nothing may match at all.
	namesForA := searchToolNames(t, reg, "alpha_lookup")
	assert.NotContains(t, namesForA, "beta_report",
		"ghost FTS postings must never surface a different tool")
	assert.Empty(t, namesForA, "evicted tool's name must match nothing")

	// B itself is searchable.
	assert.Contains(t, searchToolNames(t, reg, "beta_report"), "beta_report")

	// And the FTS index agrees with the content table.
	require.NoError(t, ftsIntegrityCheck(reg),
		"FTS5 integrity-check must pass after reindex/evict/reuse")
}

// TestNewRepairsLegacyGhostPostings verifies the once-per-open FTS rebuild in
// New: a database written by earlier builds (whose upsert was INSERT OR
// REPLACE) contains ghost postings, and reopening it must repair the index in
// place so the ghosts cannot be inherited by reused rowids.
func TestNewRepairsLegacyGhostPostings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tools.db")
	ctx := context.Background()

	reg, err := New(Config{DBPath: dbPath})
	require.NoError(t, err)

	// Plant the ghost exactly the way the legacy upsert did: INSERT OR
	// REPLACE over an existing row. The implicit DELETE does not fire
	// tools_ad (recursive_triggers is off), so the old rowid's postings
	// survive as ghosts.
	legacyUpsert := `
		INSERT OR REPLACE INTO tools (
			id, name, description, source, mcp_server, input_schema, output_schema,
			capabilities, keywords, examples, indexed_at, version, requires_approval, rate_limit
		) VALUES (?, ?, ?, ?, ?, '', '', '["search"]', '["alpha_lookup"]', '[]', ?, '', 0, '{}')`
	for i := 0; i < 2; i++ {
		_, err = reg.db.Exec(legacyUpsert,
			"mcp:alpha:alpha_lookup", "alpha_lookup", "alpha_lookup tool from alpha",
			int(loomv1.ToolSource_TOOL_SOURCE_MCP), "alpha", time.Now().Format(time.RFC3339))
		require.NoError(t, err)
	}
	require.Error(t, ftsIntegrityCheck(reg),
		"test premise: the legacy REPLACE upsert must have planted ghost postings")
	require.NoError(t, reg.Close())

	// Reopen: New's startup rebuild must repair the index.
	reg, err = New(Config{DBPath: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	require.NoError(t, ftsIntegrityCheck(reg),
		"reopening must rebuild the FTS index and clear legacy ghosts")

	// The real row is still searchable after the rebuild.
	assert.Contains(t, searchToolNames(t, reg, "alpha_lookup"), "alpha_lookup")

	// And the repaired database no longer exhibits the wrong-tool bug:
	// evicting A and indexing B (which reuses A's rowid) must not attach
	// A's tokens to B.
	require.NoError(t, reg.EvictTool(ctx, "mcp:alpha:alpha_lookup"))
	require.NoError(t, reg.RegisterTool(ctx, mcpTool("beta", "beta_report")))
	assert.NotContains(t, searchToolNames(t, reg, "alpha_lookup"), "beta_report")
	require.NoError(t, ftsIntegrityCheck(reg))
}
