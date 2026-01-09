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
// Package manager provides multi-server orchestration for MCP clients.
package manager

import (
	"fmt"
)

// Config defines the configuration for the MCP manager.
type Config struct {
	// Servers maps server name to server configuration
	Servers map[string]ServerConfig `yaml:"servers" json:"servers"`

	// DynamicDiscovery enables runtime tool discovery
	DynamicDiscovery DynamicDiscoveryConfig `yaml:"dynamic_discovery" json:"dynamic_discovery"`

	// ClientInfo provides implementation details sent to MCP servers
	ClientInfo ClientInfo `yaml:"client_info" json:"client_info"`
}

// ServerConfig defines the configuration for a single MCP server.
type ServerConfig struct {
	// Enabled indicates whether this server should be started
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Command is the executable to run for stdio transport
	Command string `yaml:"command" json:"command"`

	// Args are the command-line arguments for the command
	Args []string `yaml:"args" json:"args"`

	// Env are environment variables to set for the subprocess
	Env map[string]string `yaml:"env" json:"env"`

	// ToolFilter controls which tools are registered from this server
	ToolFilter ToolFilter `yaml:"tools" json:"tools"`

	// Transport specifies the transport type ("stdio" or "sse")
	Transport string `yaml:"transport" json:"transport"`

	// URL is the server URL (for SSE transport)
	URL string `yaml:"url" json:"url"`

	// Timeout for server operations (e.g., "30s", "1m")
	Timeout string `yaml:"timeout" json:"timeout"`
}

// ToolFilter controls which tools are registered from a server.
type ToolFilter struct {
	// All indicates whether to register all tools (default: false)
	All bool `yaml:"all" json:"all"`

	// Include is a whitelist of tool names (if set, only these are registered)
	Include []string `yaml:"include" json:"include"`

	// Exclude is a blacklist of tool names (applied after include)
	Exclude []string `yaml:"exclude" json:"exclude"`
}

// DynamicDiscoveryConfig configures runtime tool discovery.
type DynamicDiscoveryConfig struct {
	// Enabled enables dynamic tool discovery
	Enabled bool `yaml:"enabled" json:"enabled"`

	// CacheSize is the maximum number of discovered tools to cache
	CacheSize int `yaml:"cache_size" json:"cache_size"`
}

// ClientInfo provides implementation details sent to MCP servers.
type ClientInfo struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return fmt.Errorf("no servers configured")
	}

	for name, server := range c.Servers {
		if err := server.Validate(); err != nil {
			return fmt.Errorf("server %s: %w", name, err)
		}
	}

	if c.DynamicDiscovery.CacheSize < 0 {
		return fmt.Errorf("dynamic_discovery.cache_size must be >= 0")
	}

	return nil
}

// Validate checks the server configuration for errors.
func (s *ServerConfig) Validate() error {
	if !s.Enabled {
		return nil // Disabled servers don't need validation
	}

	// Validate transport
	if s.Transport == "" {
		s.Transport = "stdio" // Default
	}

	switch s.Transport {
	case "stdio":
		if s.Command == "" {
			return fmt.Errorf("command required for stdio transport")
		}
	case "http", "sse":
		if s.URL == "" {
			return fmt.Errorf("url required for http/sse transport")
		}
	default:
		return fmt.Errorf("invalid transport: %s (must be 'stdio', 'http', or 'sse')", s.Transport)
	}

	return nil
}

// ShouldRegisterTool checks if a tool should be registered based on the filter.
func (f *ToolFilter) ShouldRegisterTool(toolName string) bool {
	// If All is true and no exclusions, register everything
	if f.All && len(f.Exclude) == 0 {
		return true
	}

	// If include list is specified, tool must be in it
	if len(f.Include) > 0 {
		if !contains(f.Include, toolName) {
			return false
		}
	}

	// Check exclusion list
	if contains(f.Exclude, toolName) {
		return false
	}

	// If we have an include list and tool passed, register it
	if len(f.Include) > 0 {
		return true
	}

	// If All is true and not excluded, register
	if f.All {
		return true
	}

	// Default: don't register (selective registration by default)
	return false
}

// contains checks if a string is in a slice.
func contains(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		Servers: make(map[string]ServerConfig),
		DynamicDiscovery: DynamicDiscoveryConfig{
			Enabled:   false,
			CacheSize: 100,
		},
		ClientInfo: ClientInfo{
			Name:    "loom",
			Version: "0.1.0",
		},
	}
}
