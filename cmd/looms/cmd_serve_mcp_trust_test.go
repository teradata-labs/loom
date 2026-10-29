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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// trust_annotations written in the `mcp.servers` YAML must reach the manager's
// server config that agent registration reads: true only where set, false by
// default.
func TestInitializeMCPManager_ThreadsTrustAnnotations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp := protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID}
		if req.Method == "server/discover" {
			res := &protocol.DiscoverResult{
				ResultType:        protocol.ResultTypeComplete,
				SupportedVersions: []string{protocol.Version20260728},
				CacheScope:        "public",
			}
			require.NoError(t, res.SetServerInfo(protocol.Implementation{Name: "fake", Version: "1"}))
			resp.Result, _ = json.Marshal(res)
		} else {
			resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
mcp:
  servers:
    trusted:
      enabled: true
      transport: streamable-http
      url: `+srv.URL+`
      trust_annotations: true
    plain:
      enabled: true
      transport: streamable-http
      url: `+srv.URL+`
`)))
	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))

	m, err := initializeMCPManager(&cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.GetManager().Stop() })

	trusted, err := m.GetManager().GetServerConfig("trusted")
	require.NoError(t, err)
	plain, err := m.GetManager().GetServerConfig("plain")
	require.NoError(t, err)
	assert.True(t, trusted.TrustAnnotations)
	assert.False(t, plain.TrustAnnotations)
}
