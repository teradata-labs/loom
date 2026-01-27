package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

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
