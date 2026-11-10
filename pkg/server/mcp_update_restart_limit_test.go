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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
)

// stubMCPServer answers just enough of the protocol for Manager.AddServer to
// complete a handshake and register a live client, which is what makes a
// server "connected" and so eligible for the restart path.
func stubMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// A notification carries no id and takes no response body.
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		write := func(payload string) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,` + payload + `}`))
		}

		switch req.Method {
		case "initialize":
			write(`"result":{"protocolVersion":"2024-11-05",` +
				`"serverInfo":{"name":"stub","version":"1.0"},"capabilities":{}}`)
		case "tools/list":
			write(`"result":{"tools":[]}`)
		default:
			// server/discover and anything else: unknown, so the client
			// falls back to the initialize handshake.
			write(`"error":{"code":-32601,"message":"method not found"}`)
		}
	}))
}

// An update that restarts the server REPLACES the running config, so a request
// that does not carry max_response_bytes must not drop a configured limit back
// to the 16 MiB default. The persisted file is safe either way (viper layers
// the written map over it), but the RUNNING server is not, and GetMCPServer /
// ListMCPServers both read GetServerConfig.
//
// Two things about the shape of this test, both deliberate:
//
//   - The server is created directly on the Manager with transport
//     "streamable-http", because that is what a looms.yaml entry produces
//     (cmd_serve.go) and it is the only transport max_response_bytes affects.
//     The management RPCs cannot express it: validTransports admits only
//     stdio, http and sse. That gap is pre-existing and reported separately.
//   - The update therefore leaves transport empty, which is the only form the
//     RPC accepts here, and the restart's reconnect consequently fails with
//     "unsupported transport". That failure is issue #529 (an update rebuilds
//     the config from the request and drops fields), not this finding. It is
//     irrelevant to what is asserted: AddServer stores the new config BEFORE
//     starting, so the stored limit is what this test reads, and before the
//     fix it was zero.
func TestUpdateMCPServer_RestartKeepsTheConfiguredResponseBound(t *testing.T) {
	const configured int64 = 32 << 20

	srv := stubMCPServer(t)
	defer srv.Close()

	mgr, err := manager.NewManager(manager.Config{
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	ctx := context.Background()
	require.NoError(t, mgr.AddServer(ctx, "srv", manager.ServerConfig{
		Enabled:          true,
		Transport:        "streamable-http",
		URL:              srv.URL,
		MaxResponseBytes: configured,
		Timeout:          "5s",
	}))

	// The restart path only runs for a server that is actually connected.
	var connected bool
	for _, info := range mgr.ListServers() {
		if info.Name == "srv" {
			connected = info.Connected
		}
	}
	require.True(t, connected, "the stub server must be connected for the restart path to run")

	configPath := filepath.Join(t.TempDir(), "looms.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
	s := &MultiAgentServer{mcpManager: mgr, logger: zap.NewNop(), configPath: configPath}

	// An unrelated update: it changes the tool filter and asks for a restart,
	// and says nothing about the response bound. "sh" is required only because
	// the RPC demands a non-empty command and looks it up for an empty
	// transport; it is never executed, since the restart fails before that.
	_, updateErr := s.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{
		ServerName:       "srv",
		Enabled:          true,
		Command:          "sh",
		RestartIfRunning: true,
		ToolFilter:       &loomv1.ToolFilterConfig{All: true},
	})
	// Not asserted on, deliberately. Today the reconnect fails because the
	// request carries no transport (#529, #560); once either is fixed it will
	// succeed. Either way it is not what this test checks, so asserting on it
	// would make this test fail for an unrelated reason the day that changes.
	t.Logf("update returned err=%v (incidental: #529/#560, not what this test checks)", updateErr)

	// What matters: the stored config kept the configured bound.
	got, err := mgr.GetServerConfig("srv")
	require.NoError(t, err)
	assert.Equal(t, configured, got.MaxResponseBytes,
		"the restart reset the running limit to the default")

	// And the API reports it, since GetMCPServer reads GetServerConfig.
	reported, err := s.GetMCPServer(ctx, &loomv1.GetMCPServerRequest{ServerName: "srv"})
	require.NoError(t, err)
	assert.Equal(t, configured, reported.MaxResponseBytes,
		"GetMCPServer reported the default while the configured limit was higher")
}
