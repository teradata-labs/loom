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

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
)

// The response bound is settable through the management API, so it has to be
// readable back through it: otherwise an operator can set a limit and has no
// way to confirm what is in force short of reading the YAML by hand.
//
// The reported value is the CONFIGURED one, so an unset bound stays
// distinguishable from one explicitly set to the default.
func TestMCPServerInfo_ReportsTheConfiguredResponseBound(t *testing.T) {
	mgr, err := manager.NewManager(manager.Config{
		Servers: map[string]manager.ServerConfig{
			"bounded": {
				Enabled:          true,
				Transport:        "streamable-http",
				URL:              "http://127.0.0.1:1/mcp",
				MaxResponseBytes: 64 << 20,
			},
			"unset": {
				Enabled:   true,
				Transport: "streamable-http",
				URL:       "http://127.0.0.1:1/mcp",
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}, zap.NewNop())
	require.NoError(t, err)

	s := &MultiAgentServer{mcpManager: mgr, logger: zap.NewNop()}
	ctx := context.Background()

	t.Run("GetMCPServer", func(t *testing.T) {
		info, err := s.GetMCPServer(ctx, &loomv1.GetMCPServerRequest{ServerName: "bounded"})
		require.NoError(t, err)
		assert.Equal(t, int64(64<<20), info.MaxResponseBytes)

		info, err = s.GetMCPServer(ctx, &loomv1.GetMCPServerRequest{ServerName: "unset"})
		require.NoError(t, err)
		// 0 is how "no limit configured, the default applies" is reported.
		assert.Equal(t, int64(0), info.MaxResponseBytes)
	})

	t.Run("ListMCPServers", func(t *testing.T) {
		resp, err := s.ListMCPServers(ctx, &loomv1.ListMCPServersRequest{})
		require.NoError(t, err)

		got := map[string]int64{}
		for _, srv := range resp.Servers {
			got[srv.Name] = srv.MaxResponseBytes
		}
		assert.Equal(t, int64(64<<20), got["bounded"])
		assert.Equal(t, int64(0), got["unset"])
	})
}
