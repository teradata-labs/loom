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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/tools/registry"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// recordingMCPClient counts CallTool invocations so a test can assert a tool
// was NOT executed.
type recordingMCPClient struct {
	mu    sync.Mutex
	calls []string
}

func (c *recordingMCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	return map[string]interface{}{"ok": true}, nil
}

func (c *recordingMCPClient) callNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// TestDynamicRegistration_FuzzyMatchNameMismatchIsRejected verifies the
// executor never executes a search hit whose name differs from the requested
// tool: the registry lookup is a fuzzy full-text search, so a request for a
// dead tool whose tokens overlap a different tool's name used to silently
// register and execute that different tool.
func TestDynamicRegistration_FuzzyMatchNameMismatchIsRejected(t *testing.T) {
	ctx := context.Background()
	exec := shuttle.NewExecutor(shuttle.NewRegistry())

	client := &recordingMCPClient{}
	mcpManager := newMockMCPManager()
	mcpManager.addClient("test-server", client)

	// Only alpha_beta_gamma is indexed. A request for "alpha_beta" (e.g. an
	// evicted tool the agent still remembers) fuzzy-matches it.
	indexed := &loomv1.IndexedTool{
		Id:          "mcp:test-server:alpha_beta_gamma",
		Name:        "alpha_beta_gamma",
		Description: "alpha beta gamma tool",
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   "test-server",
		InputSchema: `{"type":"object"}`,
	}
	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{newMockMCPIndexer([]*loomv1.IndexedTool{indexed})},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = toolReg.Close() })
	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)
	exec.SetMCPManager(mcpManager)

	// The mismatching fuzzy hit must be rejected, not executed.
	_, err = exec.Execute(ctx, "alpha_beta", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool not found")
	assert.Empty(t, client.callNames(),
		"a fuzzy match with a different name must never reach execution")

	// Control: the exact name still registers and executes.
	result, err := exec.Execute(ctx, "alpha_beta_gamma", map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"alpha_beta_gamma"}, client.callNames())
}

// failingEvictRegistry delegates search to a real registry but fails every
// eviction, to exercise the evict-failure path.
type failingEvictRegistry struct {
	inner    *registry.Registry
	evictErr error
}

func (f *failingEvictRegistry) Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	return f.inner.Search(ctx, req)
}

func (f *failingEvictRegistry) EvictTool(ctx context.Context, toolID string) error {
	return f.evictErr
}

// TestDynamicRegistration_EvictFailureIsLogged verifies a failed eviction of
// a stale index entry is logged (with the tool's identity) instead of being
// silently swallowed: a failed eviction means the dead entry will be served
// again.
func TestDynamicRegistration_EvictFailureIsLogged(t *testing.T) {
	ctx := context.Background()
	core, observed := observer.New(zapcore.WarnLevel)

	exec := shuttle.NewExecutor(shuttle.NewRegistry())
	exec.SetLogger(zap.New(core))
	exec.SetToolRegistry(&failingEvictRegistry{
		inner:    newStaleToolRegistry(t),
		evictErr: assert.AnError,
	})
	exec.SetMCPManager(&notFoundMCPManager{})

	_, err := exec.Execute(ctx, "stale_tool", map[string]interface{}{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "evicted",
		"a failed eviction must not be reported as evicted")

	entries := observed.FilterMessage("Failed to evict stale tool index entry").All()
	require.Len(t, entries, 1, "the failed eviction must be logged")
	fields := entries[0].ContextMap()
	assert.Equal(t, "mcp:ghost:stale_tool", fields["tool_id"])
	assert.Equal(t, "stale_tool", fields["tool_name"])
	assert.Equal(t, "ghost", fields["mcp_server"])
	assert.Equal(t, assert.AnError.Error(), fields["error"])
}
