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
package shuttle_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/tools/registry"
)

// notFoundMCPManager reports every server as not existing, wrapping the
// shuttle.ErrMCPServerNotFound sentinel like the production adapters do for
// servers that were removed from configuration.
type notFoundMCPManager struct{}

func (m *notFoundMCPManager) GetClient(serverName string) (interface{}, error) {
	return nil, fmt.Errorf("%w: %s", shuttle.ErrMCPServerNotFound, serverName)
}

// transientErrMCPManager fails without the sentinel — a temporarily
// unreachable server, not a removed one.
type transientErrMCPManager struct{}

func (m *transientErrMCPManager) GetClient(serverName string) (interface{}, error) {
	return nil, fmt.Errorf("connection refused: %s", serverName)
}

func newStaleToolRegistry(t *testing.T) *registry.Registry {
	t.Helper()

	staleTool := &loomv1.IndexedTool{
		Id:          "mcp:ghost:stale_tool",
		Name:        "stale_tool",
		Description: "tool from a server that no longer exists",
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   "ghost",
		InputSchema: `{"type":"object"}`,
	}

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{newMockMCPIndexer([]*loomv1.IndexedTool{staleTool})},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = toolReg.Close() })

	_, err = toolReg.IndexAll(context.Background())
	require.NoError(t, err)
	return toolReg
}

// TestDynamicRegistration_EvictsStaleTool verifies that a tool whose MCP
// server no longer exists is removed from the index on first failed use, so
// the same stale entry is never served twice (issue #334).
func TestDynamicRegistration_EvictsStaleTool(t *testing.T) {
	ctx := context.Background()
	exec := shuttle.NewExecutor(shuttle.NewRegistry())
	toolReg := newStaleToolRegistry(t)

	exec.SetToolRegistry(toolReg)
	exec.SetMCPManager(&notFoundMCPManager{})

	_, err := exec.Execute(ctx, "stale_tool", map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP server not found")
	require.Contains(t, err.Error(), "evicted")

	// The stale entry is gone from the index.
	_, err = toolReg.GetTool(ctx, "mcp:ghost:stale_tool")
	require.Error(t, err, "stale tool must be evicted from the index")
}

// TestDynamicRegistration_TransientErrorKeepsTool verifies that a failure
// without the not-found sentinel (e.g. a server that is configured but
// temporarily unreachable) does NOT evict the index entry.
func TestDynamicRegistration_TransientErrorKeepsTool(t *testing.T) {
	ctx := context.Background()
	exec := shuttle.NewExecutor(shuttle.NewRegistry())
	toolReg := newStaleToolRegistry(t)

	exec.SetToolRegistry(toolReg)
	exec.SetMCPManager(&transientErrMCPManager{})

	_, err := exec.Execute(ctx, "stale_tool", map[string]interface{}{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "evicted")

	// The entry survives: the server may come back.
	_, err = toolReg.GetTool(ctx, "mcp:ghost:stale_tool")
	require.NoError(t, err, "tool must survive a transient failure")
}
