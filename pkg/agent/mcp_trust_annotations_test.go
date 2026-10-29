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

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// annotatedMCPServer serves one tool, "lookup", annotated readOnlyHint=true
// over stateless (2026-07-28) streamable HTTP.
func annotatedMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	yes := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp := protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID}
		switch req.Method {
		case "server/discover":
			res := &protocol.DiscoverResult{
				ResultType:        protocol.ResultTypeComplete,
				SupportedVersions: []string{protocol.Version20260728},
				CacheScope:        "public",
			}
			require.NoError(t, res.SetServerInfo(protocol.Implementation{Name: "annotated", Version: "1"}))
			resp.Result, _ = json.Marshal(res)
		case "tools/list":
			resp.Result, _ = json.Marshal(protocol.ToolListResult{Tools: []protocol.Tool{{
				Name:        "lookup",
				Description: "look something up",
				InputSchema: map[string]interface{}{"type": "object"},
				Annotations: &protocol.ToolAnnotations{ReadOnlyHint: &yes},
			}}})
		default:
			resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The trust_annotations flag must survive every construction path from the
// manager's server config to the registered tool: the same readOnlyHint tool
// is concurrency-safe on the trusted server and not on the default one.
func TestMCPTrustAnnotations_ThreadedThroughEveryRegistrationPath(t *testing.T) {
	srv := annotatedMCPServer(t)
	cfg := func(trust bool) manager.ServerConfig {
		return manager.ServerConfig{
			Enabled: true, Transport: "streamable-http", URL: srv.URL,
			Timeout: "10s", ToolFilter: manager.ToolFilter{All: true}, TrustAnnotations: trust,
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := func(servers map[string]manager.ServerConfig) *manager.Manager {
		mgr, err := manager.NewManager(manager.Config{
			Servers:    servers,
			ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
		}, zap.NewNop())
		require.NoError(t, err)
		require.NoError(t, mgr.Start(ctx))
		t.Cleanup(func() { _ = mgr.Stop() })
		return mgr
	}
	mgr := start(map[string]manager.ServerConfig{"trusted": cfg(true), "plain": cfg(false)})

	safe := func(a *Agent, name string) bool {
		tool, ok := a.tools.Get(name)
		require.True(t, ok, "tool %s registered", name)
		return shuttle.IsConcurrencySafe(tool)
	}

	t.Run("RegisterMCPServer", func(t *testing.T) {
		a := parallelAgent(t, nil, nil)
		require.NoError(t, a.RegisterMCPServer(ctx, mgr, "trusted"))
		require.NoError(t, a.RegisterMCPServer(ctx, mgr, "plain"))
		assert.True(t, safe(a, "trusted:lookup"))
		assert.False(t, safe(a, "plain:lookup"))
	})

	t.Run("RegisterMCPTool", func(t *testing.T) {
		a := parallelAgent(t, nil, nil)
		require.NoError(t, a.RegisterMCPTool(ctx, mgr, "trusted", "lookup"))
		require.NoError(t, a.RegisterMCPTool(ctx, mgr, "plain", "lookup"))
		assert.True(t, safe(a, "trusted:lookup"))
		assert.False(t, safe(a, "plain:lookup"))
	})

	t.Run("RegisterMCPTools", func(t *testing.T) {
		a := parallelAgent(t, nil, nil)
		for name, trust := range map[string]bool{"trusted": true, "plain": false} {
			c, err := mgr.GetClient(name)
			require.NoError(t, err)
			require.NoError(t, a.RegisterMCPTools(ctx, MCPServerConfig{Name: name, Client: c, TrustAnnotations: trust}))
		}
		assert.True(t, safe(a, "trusted:lookup"))
		assert.False(t, safe(a, "plain:lookup"))
	})

	t.Run("dynamic discovery", func(t *testing.T) {
		d := NewDynamicToolDiscovery(mgr, zap.NewNop())
		tools, err := d.SearchMultiple(ctx, "lookup")
		require.NoError(t, err)
		require.Len(t, tools, 2)
		for _, tool := range tools {
			assert.Equal(t, tool.Backend() == "mcp:trusted", shuttle.IsConcurrencySafe(tool), tool.Backend())
		}
		// Search returns the first match, so pin it with one server per manager.
		for name, trust := range map[string]bool{"trusted": true, "plain": false} {
			single, err := NewDynamicToolDiscovery(start(map[string]manager.ServerConfig{name: cfg(trust)}), zap.NewNop()).Search(ctx, "lookup")
			require.NoError(t, err)
			assert.Equal(t, trust, shuttle.IsConcurrencySafe(single), name)
		}
	})
}
