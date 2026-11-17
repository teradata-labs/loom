package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	mcpserver "github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestValidateMCPURLPlaceholderRejectsEnvironmentPlaceholders(t *testing.T) {
	err := validateMCPURLPlaceholder("https://example.test/${MCP_TOKEN}")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.NotContains(t, err.Error(), "MCP_TOKEN")
	assert.NoError(t, validateMCPURLPlaceholder("https://example.test/api"))
}

// TestAddMCPServerPersistence tests that all fields are persisted correctly when adding an MCP server.
func TestAddMCPServerPersistence(t *testing.T) {
	tests := []struct {
		name    string
		request *loomv1.AddMCPServerRequest
		verify  func(t *testing.T, configPath string)
	}{
		{
			name: "enabled_true_persisted",
			request: &loomv1.AddMCPServerRequest{
				Name:      "test-server-enabled",
				Enabled:   true,
				Command:   "python3",
				Args:      []string{"-m", "mcp_server"},
				Transport: "stdio",
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				enabled := v.GetBool("mcp.servers.test-server-enabled.enabled")
				assert.True(t, enabled, "enabled field should be true")
			},
		},
		{
			name: "enabled_false_persisted",
			request: &loomv1.AddMCPServerRequest{
				Name:      "test-server-disabled",
				Enabled:   false,
				Command:   "python3",
				Args:      []string{"-m", "mcp_server"},
				Transport: "stdio",
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				enabled := v.GetBool("mcp.servers.test-server-disabled.enabled")
				assert.False(t, enabled, "enabled field should be false")
			},
		},
		{
			name: "all_fields_persisted",
			request: &loomv1.AddMCPServerRequest{
				Name:             "test-server-complete",
				Enabled:          true,
				Command:          "node",
				Args:             []string{"server.js"},
				Transport:        "streamable-http",
				Url:              "http://localhost:8080",
				EnableSessions:   true,
				EnableResumption: true,
				WorkingDir:       "/tmp/mcp",
				MaxResponseBytes: 64 << 20,
				Env: map[string]string{
					"DEBUG": "true",
					"PORT":  "8080",
				},
				ToolFilter: &loomv1.ToolFilterConfig{
					All:     false,
					Include: []string{"tool1", "tool2"},
					Exclude: []string{"tool3"},
				},
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				prefix := "mcp.servers.test-server-complete"
				assert.True(t, v.GetBool(prefix+".enabled"))
				assert.Equal(t, "node", v.GetString(prefix+".command"))
				assert.Equal(t, []interface{}{"server.js"}, v.Get(prefix+".args"))
				assert.Equal(t, "streamable-http", v.GetString(prefix+".transport"))
				assert.Equal(t, "http://localhost:8080", v.GetString(prefix+".url"))
				assert.True(t, v.GetBool(prefix+".enable_sessions"))
				assert.True(t, v.GetBool(prefix+".enable_resumption"))
				assert.Equal(t, "/tmp/mcp", v.GetString(prefix+".working_dir"))
				assert.Equal(t, int64(64<<20), v.GetInt64(prefix+".max_response_bytes"))

				// Verify env map exists (exact structure depends on viper serialization)
				assert.NotNil(t, v.Get(prefix+".env"))
				assert.Equal(t, "true", v.GetString(prefix+".env.DEBUG"))
				assert.Equal(t, "8080", v.GetString(prefix+".env.PORT"))

				assert.False(t, v.GetBool(prefix+".tool_filter.all"))
				include := v.GetStringSlice(prefix + ".tool_filter.include")
				assert.ElementsMatch(t, []string{"tool1", "tool2"}, include)
				exclude := v.GetStringSlice(prefix + ".tool_filter.exclude")
				assert.ElementsMatch(t, []string{"tool3"}, exclude)
			},
		},
		{
			name: "http_transport_with_url",
			request: &loomv1.AddMCPServerRequest{
				Name:      "http-server",
				Enabled:   true,
				Transport: "http",
				Url:       "https://api.example.com",
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				prefix := "mcp.servers.http-server"
				assert.Equal(t, "http", v.GetString(prefix+".transport"))
				assert.Equal(t, "https://api.example.com", v.GetString(prefix+".url"))
				assert.True(t, v.GetBool(prefix+".enabled"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "looms.yaml")

			// Initialize empty config
			require.NoError(t, os.WriteFile(configPath, []byte(""), 0644))

			// Create server instance
			logger := zap.NewNop()
			server := &MultiAgentServer{
				configPath: configPath,
				logger:     logger,
			}

			// Add MCP server
			err := server.addMCPServerToConfig(tt.request)
			require.NoError(t, err)

			// Verify persistence
			tt.verify(t, configPath)
		})
	}
}

// TestUpdateMCPServerPersistence tests that all fields are persisted correctly when updating an MCP server.
func TestUpdateMCPServerPersistence(t *testing.T) {
	tests := []struct {
		name    string
		initial *loomv1.AddMCPServerRequest
		update  *loomv1.UpdateMCPServerRequest
		verify  func(t *testing.T, configPath string)
	}{
		{
			name: "update_enabled_to_false",
			initial: &loomv1.AddMCPServerRequest{
				Name:      "test-server",
				Enabled:   true,
				Command:   "python3",
				Args:      []string{"-m", "mcp_server"},
				Transport: "stdio",
			},
			update: &loomv1.UpdateMCPServerRequest{
				ServerName: "test-server",
				Enabled:    false,
				Command:    "python3",
				Args:       []string{"-m", "mcp_server"},
				Transport:  "stdio",
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				enabled := v.GetBool("mcp.servers.test-server.enabled")
				assert.False(t, enabled, "enabled should be updated to false")
			},
		},
		{
			name: "update_all_fields",
			initial: &loomv1.AddMCPServerRequest{
				Name:      "test-server",
				Enabled:   true,
				Command:   "python3",
				Transport: "stdio",
			},
			update: &loomv1.UpdateMCPServerRequest{
				ServerName:       "test-server",
				Enabled:          true,
				Command:          "node",
				Args:             []string{"server.js"},
				Transport:        "streamable-http",
				Url:              "http://localhost:9000",
				EnableSessions:   true,
				EnableResumption: true,
				WorkingDir:       "/opt/mcp",
				MaxResponseBytes: 32 << 20,
				Env: map[string]string{
					"NODE_ENV": "production",
				},
				ToolFilter: &loomv1.ToolFilterConfig{
					All:     true,
					Exclude: []string{"dangerous-tool"},
				},
			},
			verify: func(t *testing.T, configPath string) {
				v := viper.New()
				v.SetConfigFile(configPath)
				require.NoError(t, v.ReadInConfig())

				prefix := "mcp.servers.test-server"
				assert.True(t, v.GetBool(prefix+".enabled"))
				assert.Equal(t, "node", v.GetString(prefix+".command"))
				assert.Equal(t, "streamable-http", v.GetString(prefix+".transport"))
				assert.Equal(t, "http://localhost:9000", v.GetString(prefix+".url"))
				assert.True(t, v.GetBool(prefix+".enable_sessions"))
				assert.True(t, v.GetBool(prefix+".enable_resumption"))
				assert.Equal(t, "/opt/mcp", v.GetString(prefix+".working_dir"))
				assert.Equal(t, int64(32<<20), v.GetInt64(prefix+".max_response_bytes"))

				// Verify env map exists
				assert.NotNil(t, v.Get(prefix+".env"))
				assert.Equal(t, "production", v.GetString(prefix+".env.NODE_ENV"))

				assert.True(t, v.GetBool(prefix+".tool_filter.all"))
				exclude := v.GetStringSlice(prefix + ".tool_filter.exclude")
				assert.ElementsMatch(t, []string{"dangerous-tool"}, exclude)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "looms.yaml")

			// Initialize empty config
			require.NoError(t, os.WriteFile(configPath, []byte(""), 0644))

			// Create server instance
			logger := zap.NewNop()
			server := &MultiAgentServer{
				configPath: configPath,
				logger:     logger,
			}

			// Add initial MCP server
			err := server.addMCPServerToConfig(tt.initial)
			require.NoError(t, err)

			// Update MCP server
			err = server.updateMCPServerInConfig(tt.update)
			require.NoError(t, err)

			// Verify persistence
			tt.verify(t, configPath)
		})
	}
}

// TestMCPServerPersistenceAcrossRestart is the critical integration test that verifies
// MCP servers with enabled=true actually persist correctly across restarts.
func TestMCPServerPersistenceAcrossRestart(t *testing.T) {
	// Skip if in short mode (this is an integration test)
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "looms.yaml")
	initialConfig := `
mcp:
  servers: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	// Step 1: Create first server instance and add MCP server
	logger := zap.NewNop()
	server1 := &MultiAgentServer{
		configPath: configPath,
		logger:     logger,
	}

	addReq := &loomv1.AddMCPServerRequest{
		Name:             "persistent-server",
		Enabled:          true,
		Command:          "python3",
		Args:             []string{"-m", "mcp_server"},
		Transport:        "stdio",
		Url:              "http://localhost:8080",
		EnableSessions:   true,
		EnableResumption: true,
	}

	err := server1.addMCPServerToConfig(addReq)
	require.NoError(t, err, "Failed to add MCP server to config")

	// Step 2: Verify enabled=true was written to config file
	v := viper.New()
	v.SetConfigFile(configPath)
	require.NoError(t, v.ReadInConfig())

	enabled := v.GetBool("mcp.servers.persistent-server.enabled")
	assert.True(t, enabled, "enabled field should be true in config file")

	// Step 3: Verify all other fields were also persisted
	assert.Equal(t, "python3", v.GetString("mcp.servers.persistent-server.command"))
	assert.Equal(t, "stdio", v.GetString("mcp.servers.persistent-server.transport"))
	assert.Equal(t, "http://localhost:8080", v.GetString("mcp.servers.persistent-server.url"))
	assert.True(t, v.GetBool("mcp.servers.persistent-server.enable_sessions"))
	assert.True(t, v.GetBool("mcp.servers.persistent-server.enable_resumption"))

	// Step 4: Simulate restart by creating a new viper instance and re-reading config
	v2 := viper.New()
	v2.SetConfigFile(configPath)
	require.NoError(t, v2.ReadInConfig())

	// Verify config was loaded correctly with enabled=true (doesn't default to false)
	enabled2 := v2.GetBool("mcp.servers.persistent-server.enabled")
	assert.True(t, enabled2, "enabled should still be true after config reload")

	// Verify all fields are still present
	assert.Equal(t, "python3", v2.GetString("mcp.servers.persistent-server.command"))
	assert.Equal(t, "stdio", v2.GetString("mcp.servers.persistent-server.transport"))
	assert.Equal(t, "http://localhost:8080", v2.GetString("mcp.servers.persistent-server.url"))
	assert.True(t, v2.GetBool("mcp.servers.persistent-server.enable_sessions"))
	assert.True(t, v2.GetBool("mcp.servers.persistent-server.enable_resumption"))
}

// TestConcurrentMCPOperations tests that sequential add operations preserve all fields correctly.
// Note: True concurrent writes to the same config file can result in lost updates due to
// read-modify-write conflicts in Viper. This is a known limitation. In production, MCP server
// operations are serialized through the gRPC server's request handling.
func TestConcurrentMCPOperations(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "looms.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0644))

	logger := zap.NewNop()
	server := &MultiAgentServer{
		configPath: configPath,
		logger:     logger,
	}

	// Add servers sequentially (simulating serialized gRPC requests)
	const numServers = 10
	for i := 0; i < numServers; i++ {
		req := &loomv1.AddMCPServerRequest{
			Name:      fmt.Sprintf("server-%d", i),
			Enabled:   i%2 == 0, // Alternate enabled/disabled
			Command:   "python3",
			Args:      []string{"-m", "mcp_server"},
			Transport: "stdio",
		}
		err := server.addMCPServerToConfig(req)
		require.NoError(t, err)
	}

	// Verify all servers were added correctly
	v := viper.New()
	v.SetConfigFile(configPath)
	require.NoError(t, v.ReadInConfig())

	for i := 0; i < numServers; i++ {
		serverName := fmt.Sprintf("server-%d", i)
		expectedEnabled := i%2 == 0

		enabled := v.GetBool(fmt.Sprintf("mcp.servers.%s.enabled", serverName))
		assert.Equal(t, expectedEnabled, enabled,
			"Server %s enabled field should match expected value", serverName)
	}
}

// TestMCPServerMaxResponseBytesPersistence covers the response bound across both
// config helpers. A limit set through the management API has to survive a restart
// the way `enabled` does, and because both helpers replace the whole server entry,
// an update that leaves the field at zero must not erase a limit set by hand in
// YAML.
func TestMCPServerMaxResponseBytesPersistence(t *testing.T) {
	newServer := func(t *testing.T, initial string) (*MultiAgentServer, string) {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "looms.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))
		return &MultiAgentServer{configPath: configPath, logger: zap.NewNop()}, configPath
	}
	reload := func(t *testing.T, configPath string) *viper.Viper {
		t.Helper()
		v := viper.New()
		v.SetConfigFile(configPath)
		require.NoError(t, v.ReadInConfig())
		return v
	}

	const handSet = `mcp:
  servers:
    srv:
      enabled: true
      command: python3
      transport: stdio
      max_response_bytes: 33554432
`

	t.Run("add_persists_the_requested_limit", func(t *testing.T) {
		server, configPath := newServer(t, "")
		require.NoError(t, server.addMCPServerToConfig(&loomv1.AddMCPServerRequest{
			Name: "srv", Enabled: true, Command: "python3", Transport: "stdio",
			MaxResponseBytes: 64 << 20,
		}))
		assert.Equal(t, int64(64<<20), reload(t, configPath).GetInt64("mcp.servers.srv.max_response_bytes"))
	})

	t.Run("add_omits_the_key_when_the_request_leaves_it_unset", func(t *testing.T) {
		server, configPath := newServer(t, "")
		require.NoError(t, server.addMCPServerToConfig(&loomv1.AddMCPServerRequest{
			Name: "srv", Enabled: true, Command: "python3", Transport: "stdio",
		}))
		// Absent, not zero: the key's absence is how the default is requested.
		assert.False(t, reload(t, configPath).IsSet("mcp.servers.srv.max_response_bytes"))
	})

	t.Run("update_persists_the_requested_limit", func(t *testing.T) {
		server, configPath := newServer(t, "")
		require.NoError(t, server.addMCPServerToConfig(&loomv1.AddMCPServerRequest{
			Name: "srv", Enabled: true, Command: "python3", Transport: "stdio",
		}))
		require.NoError(t, server.updateMCPServerInConfig(&loomv1.UpdateMCPServerRequest{
			ServerName: "srv", Enabled: true, Command: "python3", Transport: "stdio",
			MaxResponseBytes: 8 << 20,
		}))
		assert.Equal(t, int64(8<<20), reload(t, configPath).GetInt64("mcp.servers.srv.max_response_bytes"))
	})

	t.Run("update_keeps_a_hand_set_yaml_value_when_the_request_omits_it", func(t *testing.T) {
		server, configPath := newServer(t, handSet)
		require.NoError(t, server.updateMCPServerInConfig(&loomv1.UpdateMCPServerRequest{
			ServerName: "srv", Enabled: true, Command: "node", Transport: "stdio",
		}))
		v := reload(t, configPath)
		// This pins the config layer, not the helper: the written map is an
		// override over the file, so a key the request leaves unset keeps the
		// file's value. Writing a zero through would shadow it instead.
		assert.Equal(t, int64(33554432), v.GetInt64("mcp.servers.srv.max_response_bytes"),
			"an unrelated update must not erase a limit set by hand")
		assert.Equal(t, "node", v.GetString("mcp.servers.srv.command"), "the update itself must still apply")
	})

	t.Run("update_overrides_a_hand_set_yaml_value_when_the_request_sets_it", func(t *testing.T) {
		server, configPath := newServer(t, handSet)
		require.NoError(t, server.updateMCPServerInConfig(&loomv1.UpdateMCPServerRequest{
			ServerName: "srv", Enabled: true, Command: "python3", Transport: "stdio",
			MaxResponseBytes: 1 << 20,
		}))
		assert.Equal(t, int64(1<<20), reload(t, configPath).GetInt64("mcp.servers.srv.max_response_bytes"))
	})
}

// streamableFixtureTools is a static ToolProvider for the loopback fixture.
type streamableFixtureTools []string

func (f streamableFixtureTools) ListTools(context.Context) ([]protocol.Tool, error) {
	tools := make([]protocol.Tool, len(f))
	for i, name := range f {
		tools[i] = protocol.Tool{Name: name, Description: name}
	}
	return tools, nil
}

func (f streamableFixtureTools) CallTool(context.Context, string, map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{}, nil
}

// newStreamableMCPFixture starts a loopback streamable-http MCP server and
// returns its URL plus a counter of HTTP requests it has received.
func newStreamableMCPFixture(t *testing.T, tools ...string) (string, *atomic.Int64) {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer("mgmt-fixture", "1.0.0", nil, mcpserver.WithToolProvider(streamableFixtureTools(tools)))
	httpSrv, err := transport.NewStreamableHTTPServer(transport.StreamableHTTPServerConfig{
		Handler:       mcpSrv.HandleMessage,
		StreamHandler: mcpSrv,
	})
	require.NoError(t, err)
	var requests atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		httpSrv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, &requests
}

func newManagementTestServer(t *testing.T) (*MultiAgentServer, *manager.Manager, string) {
	t.Helper()
	mgr, err := manager.NewManager(manager.Config{
		ClientInfo: manager.ClientInfo{Name: "mgmt-test", Version: "1.0.0"},
	}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() {
		// Manager.Stop is a no-op before Start, so close AddServer-started clients directly.
		for _, name := range mgr.ServerNames() {
			_ = mgr.StopServer(name)
		}
	})

	configPath := filepath.Join(t.TempDir(), "looms.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))

	srv := &MultiAgentServer{}
	srv.SetMCPManager(mgr, configPath, zap.NewNop())
	return srv, mgr, configPath
}

func readMCPServerConfig(t *testing.T, configPath string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigFile(configPath)
	require.NoError(t, v.ReadInConfig())
	return v
}

func listedToolNames(t *testing.T, srv *MultiAgentServer, name string) []string {
	t.Helper()
	resp, err := srv.ListMCPServerTools(context.Background(), &loomv1.ListMCPServerToolsRequest{ServerName: name})
	require.NoError(t, err)
	names := make([]string, len(resp.Tools))
	for i, tool := range resp.Tools {
		names[i] = tool.Name
	}
	return names
}

func TestAddMCPServerEnabledRemoteMissingURLDoesNotPersist(t *testing.T) {
	for _, transportName := range []string{"http", "sse", "streamable-http"} {
		t.Run(transportName, func(t *testing.T) {
			srv, _, configPath := newManagementTestServer(t)
			resp, err := srv.AddMCPServer(context.Background(), &loomv1.AddMCPServerRequest{
				Name: "healthy", Transport: "stdio", Command: "go", Enabled: true,
			})
			require.NoError(t, err)
			require.True(t, resp.Success)
			before, err := os.ReadFile(configPath)
			require.NoError(t, err)

			_, err = srv.AddMCPServer(context.Background(), &loomv1.AddMCPServerRequest{
				Name: "invalid", Transport: transportName, Enabled: true, AutoStart: false,
			})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			after, readErr := os.ReadFile(configPath)
			require.NoError(t, readErr)
			assert.Equal(t, before, after)
			v := readMCPServerConfig(t, configPath)
			assert.False(t, v.IsSet("mcp.servers.invalid"))
			var cfg manager.Config
			require.NoError(t, v.UnmarshalKey("mcp", &cfg))
			require.Len(t, cfg.Servers, 1)
			assert.True(t, cfg.Servers["healthy"].Enabled)
			_, err = manager.NewManager(cfg, zap.NewNop())
			require.NoError(t, err, "persisted healthy server must remain startup-valid")
		})
	}
}

func TestMCPServerDisabledValidationUsesManagerSemantics(t *testing.T) {
	srv, mgr, configPath := newManagementTestServer(t)
	for _, transportName := range []string{"stdio", "http", "sse", "streamable-http"} {
		resp, err := srv.AddMCPServer(context.Background(), &loomv1.AddMCPServerRequest{
			Name: transportName, Transport: transportName, Enabled: false,
		})
		require.NoError(t, err)
		require.True(t, resp.Success)
		require.NoError(t, mgr.AddServer(context.Background(), transportName, manager.ServerConfig{
			Transport: transportName, Enabled: false,
		}))
		_, err = srv.UpdateMCPServer(context.Background(), &loomv1.UpdateMCPServerRequest{
			ServerName: transportName, Transport: transportName, Enabled: false,
		})
		require.NoError(t, err)
	}
	v := readMCPServerConfig(t, configPath)
	var cfg manager.Config
	require.NoError(t, v.UnmarshalKey("mcp", &cfg))
	_, err := manager.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)
}

func TestAddMCPServerStreamableHTTP(t *testing.T) {
	ctx := context.Background()

	t.Run("remote_without_command_succeeds", func(t *testing.T) {
		url, requests := newStreamableMCPFixture(t, "alpha", "beta")
		srv, mgr, configPath := newManagementTestServer(t)

		resp, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
			Name:             "remote",
			Transport:        "streamable-http",
			Url:              url,
			EnableSessions:   true,
			EnableResumption: true,
			Enabled:          true,
			AutoStart:        true,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Message)
		assert.Equal(t, int32(2), resp.Server.ToolCount)
		assert.Equal(t, "streamable-http", resp.Server.Transport)
		assert.Positive(t, requests.Load())
		assert.ElementsMatch(t, []string{"alpha", "beta"}, listedToolNames(t, srv, "remote"))

		cfg, err := mgr.GetServerConfig("remote")
		require.NoError(t, err)
		assert.Equal(t, url, cfg.URL)
		assert.True(t, cfg.EnableSessions)
		assert.True(t, cfg.EnableResumption)

		v := readMCPServerConfig(t, configPath)
		assert.Equal(t, "streamable-http", v.GetString("mcp.servers.remote.transport"))
		assert.Equal(t, url, v.GetString("mcp.servers.remote.url"))
		assert.True(t, v.GetBool("mcp.servers.remote.enable_sessions"))
		assert.True(t, v.GetBool("mcp.servers.remote.enable_resumption"))
	})

	t.Run("missing_url_rejected_locally", func(t *testing.T) {
		srv, _, configPath := newManagementTestServer(t)

		_, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
			Name:      "nourl",
			Transport: "streamable-http",
			Enabled:   true,
			AutoStart: true,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.False(t, readMCPServerConfig(t, configPath).IsSet("mcp.servers.nourl"))
	})

	t.Run("stdio_validation_preserved", func(t *testing.T) {
		srv, _, _ := newManagementTestServer(t)
		for _, transportName := range []string{"", "stdio"} {
			_, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{Name: "s", Transport: transportName, Enabled: true})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Contains(t, err.Error(), "command is required")
		}
		_, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
			Name: "s", Transport: "stdio", Command: "definitely-not-a-real-command-560", Enabled: true,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "command not found")
	})

	t.Run("invalid_transport_lists_streamable_http", func(t *testing.T) {
		srv, _, _ := newManagementTestServer(t)
		_, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{Name: "s", Transport: "carrier-pigeon"})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "streamable-http")
	})
}

func TestUpdateMCPServerStreamableHTTP(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit_transport_update_restarts_against_new_url", func(t *testing.T) {
		urlA, _ := newStreamableMCPFixture(t, "alpha")
		urlB, requestsB := newStreamableMCPFixture(t, "beta", "gamma")
		srv, mgr, configPath := newManagementTestServer(t)

		resp, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
			Name: "remote", Transport: "streamable-http", Url: urlA,
			EnableSessions: false, EnableResumption: true, Enabled: true, AutoStart: true,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Message)
		assert.Equal(t, []string{"alpha"}, listedToolNames(t, srv, "remote"))

		info, err := srv.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{
			ServerName:       "remote",
			Transport:        "streamable-http",
			Url:              urlB,
			EnableSessions:   true,
			EnableResumption: false,
			Enabled:          true,
			RestartIfRunning: true,
		})
		require.NoError(t, err)
		assert.True(t, info.Connected)
		assert.Positive(t, requestsB.Load())
		assert.ElementsMatch(t, []string{"beta", "gamma"}, listedToolNames(t, srv, "remote"))

		cfg, err := mgr.GetServerConfig("remote")
		require.NoError(t, err)
		assert.Equal(t, urlB, cfg.URL)
		assert.True(t, cfg.EnableSessions)
		assert.False(t, cfg.EnableResumption)

		v := readMCPServerConfig(t, configPath)
		assert.Equal(t, urlB, v.GetString("mcp.servers.remote.url"))
		assert.True(t, v.GetBool("mcp.servers.remote.enable_sessions"))
		assert.False(t, v.GetBool("mcp.servers.remote.enable_resumption"))
	})

	t.Run("missing_url_restart_rejected_locally", func(t *testing.T) {
		for _, transportName := range []string{"http", "sse", "streamable-http"} {
			t.Run(transportName, func(t *testing.T) {
				urlA, _ := newStreamableMCPFixture(t, "alpha")
				srv, mgr, configPath := newManagementTestServer(t)

				resp, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
					Name: "remote", Transport: "streamable-http", Url: urlA, Enabled: true, AutoStart: true,
				})
				require.NoError(t, err)
				require.True(t, resp.Success, resp.Message)
				before, err := os.ReadFile(configPath)
				require.NoError(t, err)

				_, err = srv.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{
					ServerName: "remote", Transport: transportName, Enabled: true, RestartIfRunning: true,
				})
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				after, readErr := os.ReadFile(configPath)
				require.NoError(t, readErr)
				assert.Equal(t, before, after)
				client, err := mgr.GetClient("remote")
				require.NoError(t, err)
				tools, err := client.ListTools(ctx)
				require.NoError(t, err)
				require.Len(t, tools, 1)
				assert.Equal(t, "alpha", tools[0].Name)
			})
		}
	})

	t.Run("stdio_validation_preserved", func(t *testing.T) {
		urlA, _ := newStreamableMCPFixture(t, "alpha")
		srv, _, _ := newManagementTestServer(t)
		resp, err := srv.AddMCPServer(ctx, &loomv1.AddMCPServerRequest{
			Name: "remote", Transport: "streamable-http", Url: urlA, Enabled: true,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Message)

		for _, transportName := range []string{"", "stdio"} {
			_, err := srv.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{ServerName: "remote", Transport: transportName, Enabled: true})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Contains(t, err.Error(), "command is required")
		}
		_, err = srv.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{
			ServerName: "remote", Transport: "stdio", Command: "definitely-not-a-real-command-560", Enabled: true,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "command not found")

		_, err = srv.UpdateMCPServer(ctx, &loomv1.UpdateMCPServerRequest{ServerName: "remote", Transport: "carrier-pigeon"})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Contains(t, err.Error(), "streamable-http")
	})
}

func TestTestMCPServerConnectionStreamableHTTP(t *testing.T) {
	ctx := context.Background()

	t.Run("remote_without_command_succeeds", func(t *testing.T) {
		url, requests := newStreamableMCPFixture(t, "alpha", "beta")
		srv, mgr, configPath := newManagementTestServer(t)

		resp, err := srv.TestMCPServerConnection(ctx, &loomv1.TestMCPServerConnectionRequest{
			Transport:        "streamable-http",
			Url:              url,
			EnableSessions:   true,
			EnableResumption: true,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
		assert.Equal(t, int32(2), resp.ToolCount)
		assert.Positive(t, requests.Load())

		var tested []manager.ServerInfo
		for _, info := range mgr.ListServers() {
			if strings.HasPrefix(info.Name, "__test__") {
				tested = append(tested, info)
			}
		}
		require.Len(t, tested, 1)
		cfg, err := mgr.GetServerConfig(tested[0].Name)
		require.NoError(t, err)
		assert.Equal(t, url, cfg.URL)
		assert.True(t, cfg.EnableSessions)
		assert.True(t, cfg.EnableResumption)
		assert.False(t, tested[0].Connected, "test server must be stopped after the probe")

		raw, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "__test__", "connection test must not persist config")
	})

	t.Run("missing_url_rejected_locally", func(t *testing.T) {
		srv, _, _ := newManagementTestServer(t)

		resp, err := srv.TestMCPServerConnection(ctx, &loomv1.TestMCPServerConnectionRequest{Transport: "streamable-http"})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.NotContains(t, resp.Error, "command is required")
	})

	t.Run("url_placeholder_rejected_before_probe", func(t *testing.T) {
		for _, transportName := range []string{"http", "sse", "streamable-http"} {
			t.Run(transportName, func(t *testing.T) {
				srv, mgr, _ := newManagementTestServer(t)
				resp, err := srv.TestMCPServerConnection(ctx, &loomv1.TestMCPServerConnectionRequest{
					Transport: transportName, Url: "http://127.0.0.1/${MCP_TOKEN}",
				})
				require.NoError(t, err)
				assert.False(t, resp.Success)
				assert.Contains(t, resp.Error, "environment placeholders are not allowed")
				assert.NotContains(t, resp.Error, "MCP_TOKEN")
				assert.Empty(t, mgr.ServerNames())
			})
		}
	})

	t.Run("stdio_validation_preserved", func(t *testing.T) {
		srv, _, _ := newManagementTestServer(t)
		for _, transportName := range []string{"", "stdio"} {
			resp, err := srv.TestMCPServerConnection(ctx, &loomv1.TestMCPServerConnectionRequest{Transport: transportName})
			require.NoError(t, err)
			assert.False(t, resp.Success)
			assert.Contains(t, resp.Error, "command is required")
		}
		resp, err := srv.TestMCPServerConnection(ctx, &loomv1.TestMCPServerConnectionRequest{
			Transport: "stdio", Command: "definitely-not-a-real-command-560",
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "command not found")
	})
}
