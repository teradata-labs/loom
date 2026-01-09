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
package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid stdio config",
			config: Config{
				Servers: map[string]ServerConfig{
					"filesystem": {
						Enabled:   true,
						Transport: "stdio",
						Command:   "npx",
						Args:      []string{"-y", "@modelcontextprotocol/server-filesystem"},
					},
				},
				ClientInfo: ClientInfo{
					Name:    "test",
					Version: "0.1.0",
				},
			},
			wantErr: false,
		},
		{
			name: "valid http config",
			config: Config{
				Servers: map[string]ServerConfig{
					"remote": {
						Enabled:   true,
						Transport: "http",
						URL:       "http://localhost:8080",
					},
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: false,
		},
		{
			name: "no servers",
			config: Config{
				Servers:    map[string]ServerConfig{},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: true,
			errMsg:  "no servers configured",
		},
		{
			name: "stdio missing command",
			config: Config{
				Servers: map[string]ServerConfig{
					"bad": {
						Enabled:   true,
						Transport: "stdio",
					},
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: true,
			errMsg:  "command required",
		},
		{
			name: "http missing url",
			config: Config{
				Servers: map[string]ServerConfig{
					"bad": {
						Enabled:   true,
						Transport: "http",
					},
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: true,
			errMsg:  "url required",
		},
		{
			name: "invalid transport",
			config: Config{
				Servers: map[string]ServerConfig{
					"bad": {
						Enabled:   true,
						Transport: "grpc",
					},
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: true,
			errMsg:  "invalid transport",
		},
		{
			name: "negative cache size",
			config: Config{
				Servers: map[string]ServerConfig{
					"test": {
						Enabled:   true,
						Transport: "stdio",
						Command:   "npx",
					},
				},
				DynamicDiscovery: DynamicDiscoveryConfig{
					CacheSize: -1,
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: true,
			errMsg:  "cache_size must be >= 0",
		},
		{
			name: "disabled server - no validation",
			config: Config{
				Servers: map[string]ServerConfig{
					"disabled": {
						Enabled:   false,
						Transport: "invalid",
					},
					"enabled": {
						Enabled:   true,
						Transport: "stdio",
						Command:   "npx",
					},
				},
				ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
			},
			wantErr: false, // Disabled servers don't get validated
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestToolFilter_ShouldRegisterTool(t *testing.T) {
	tests := []struct {
		name     string
		filter   ToolFilter
		toolName string
		want     bool
	}{
		{
			name: "all true - register everything",
			filter: ToolFilter{
				All: true,
			},
			toolName: "read_file",
			want:     true,
		},
		{
			name: "include list - tool in list",
			filter: ToolFilter{
				Include: []string{"read_file", "write_file"},
			},
			toolName: "read_file",
			want:     true,
		},
		{
			name: "include list - tool not in list",
			filter: ToolFilter{
				Include: []string{"read_file", "write_file"},
			},
			toolName: "delete_file",
			want:     false,
		},
		{
			name: "exclude list - tool excluded",
			filter: ToolFilter{
				All:     true,
				Exclude: []string{"delete_database"},
			},
			toolName: "delete_database",
			want:     false,
		},
		{
			name: "exclude list - tool not excluded",
			filter: ToolFilter{
				All:     true,
				Exclude: []string{"delete_database"},
			},
			toolName: "read_file",
			want:     true,
		},
		{
			name: "include and exclude - included but excluded",
			filter: ToolFilter{
				Include: []string{"read_file", "delete_file"},
				Exclude: []string{"delete_file"},
			},
			toolName: "delete_file",
			want:     false,
		},
		{
			name: "include and exclude - included and not excluded",
			filter: ToolFilter{
				Include: []string{"read_file", "write_file"},
				Exclude: []string{"delete_file"},
			},
			toolName: "read_file",
			want:     true,
		},
		{
			name:     "empty filter - default selective (don't register)",
			filter:   ToolFilter{},
			toolName: "read_file",
			want:     false,
		},
		{
			name: "all false with no include - default behavior",
			filter: ToolFilter{
				All: false,
			},
			toolName: "read_file",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filter.ShouldRegisterTool(tt.toolName)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToolFilter_MultipleTools(t *testing.T) {
	// Test filtering a realistic set of tools
	filter := ToolFilter{
		All:     true,
		Exclude: []string{"drop_database", "drop_table", "delete_all"},
	}

	tools := []string{
		"read_file",
		"write_file",
		"list_tables",
		"execute_query",
		"drop_database", // Should be excluded
		"drop_table",    // Should be excluded
		"create_table",
	}

	var registered []string
	for _, tool := range tools {
		if filter.ShouldRegisterTool(tool) {
			registered = append(registered, tool)
		}
	}

	assert.Len(t, registered, 5)
	assert.NotContains(t, registered, "drop_database")
	assert.NotContains(t, registered, "drop_table")
	assert.Contains(t, registered, "read_file")
	assert.Contains(t, registered, "execute_query")
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.NotNil(t, config.Servers)
	assert.Equal(t, "loom", config.ClientInfo.Name)
	assert.Equal(t, "0.1.0", config.ClientInfo.Version)
	assert.False(t, config.DynamicDiscovery.Enabled)
	assert.Equal(t, 100, config.DynamicDiscovery.CacheSize)
}

func TestServerConfig_Validate_DefaultTransport(t *testing.T) {
	config := ServerConfig{
		Enabled: true,
		Command: "npx",
		Args:    []string{"-y", "test"},
		// Transport not specified - should default to stdio
	}

	err := config.Validate()
	require.NoError(t, err)
	assert.Equal(t, "stdio", config.Transport)
}

func TestToolFilter_CombinedScenarios(t *testing.T) {
	tests := []struct {
		name         string
		filter       ToolFilter
		expectedPass []string
		expectedFail []string
	}{
		{
			name: "selective whitelist",
			filter: ToolFilter{
				Include: []string{"read_file", "write_file", "list_directory"},
			},
			expectedPass: []string{"read_file", "write_file", "list_directory"},
			expectedFail: []string{"delete_file", "execute_query", "drop_table"},
		},
		{
			name: "all except dangerous",
			filter: ToolFilter{
				All:     true,
				Exclude: []string{"drop_database", "delete_all", "format_disk"},
			},
			expectedPass: []string{"read_file", "execute_query", "create_table"},
			expectedFail: []string{"drop_database", "delete_all", "format_disk"},
		},
		{
			name: "whitelist with additional exclusions",
			filter: ToolFilter{
				Include: []string{"read_file", "write_file", "delete_file"},
				Exclude: []string{"delete_file"},
			},
			expectedPass: []string{"read_file", "write_file"},
			expectedFail: []string{"delete_file", "execute_query"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, tool := range tt.expectedPass {
				assert.True(t, tt.filter.ShouldRegisterTool(tool),
					"expected %s to pass filter", tool)
			}
			for _, tool := range tt.expectedFail {
				assert.False(t, tt.filter.ShouldRegisterTool(tool),
					"expected %s to fail filter", tool)
			}
		})
	}
}

func TestContains(t *testing.T) {
	slice := []string{"read_file", "write_file", "list_directory"}

	assert.True(t, contains(slice, "read_file"))
	assert.True(t, contains(slice, "write_file"))
	assert.False(t, contains(slice, "delete_file"))
	assert.False(t, contains([]string{}, "anything"))
}
