// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fabric

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// BackendYAML represents the YAML structure for backend configuration
type BackendYAML struct {
	APIVersion      string                  `yaml:"apiVersion"`
	Kind            string                  `yaml:"kind"`
	Name            string                  `yaml:"name"`
	Description     string                  `yaml:"description"`
	Type            string                  `yaml:"type"`
	Database        *DatabaseConnectionYAML `yaml:"database"`
	Rest            *RestConnectionYAML     `yaml:"rest"`
	GraphQL         *GraphQLConnectionYAML  `yaml:"graphql"`
	GRPC            *GRPCConnectionYAML     `yaml:"grpc"`
	MCP             *MCPConnectionYAML      `yaml:"mcp"`
	SchemaDiscovery *SchemaDiscoveryYAML    `yaml:"schema_discovery"`
	ToolGeneration  *ToolGenerationYAML     `yaml:"tool_generation"`
	HealthCheck     *HealthCheckYAML        `yaml:"health_check"`
}

type DatabaseConnectionYAML struct {
	DSN                      string `yaml:"dsn"`
	MaxConnections           int    `yaml:"max_connections"`
	MaxIdleConnections       int    `yaml:"max_idle_connections"`
	ConnectionTimeoutSeconds int    `yaml:"connection_timeout_seconds"`
	EnableSSL                bool   `yaml:"enable_ssl"`
	SSLCertPath              string `yaml:"ssl_cert_path"`
}

type RestConnectionYAML struct {
	BaseURL        string            `yaml:"base_url"`
	Auth           *AuthConfigYAML   `yaml:"auth"`
	Headers        map[string]string `yaml:"headers"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	MaxRetries     int               `yaml:"max_retries"`
}

type GraphQLConnectionYAML struct {
	Endpoint       string            `yaml:"endpoint"`
	Auth           *AuthConfigYAML   `yaml:"auth"`
	Headers        map[string]string `yaml:"headers"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
}

type GRPCConnectionYAML struct {
	Address        string            `yaml:"address"`
	UseTLS         bool              `yaml:"use_tls"`
	CertPath       string            `yaml:"cert_path"`
	Metadata       map[string]string `yaml:"metadata"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
}

type MCPConnectionYAML struct {
	Command    string            `yaml:"command"`
	Args       []string          `yaml:"args"`
	Env        map[string]string `yaml:"env"`
	Transport  string            `yaml:"transport"`
	URL        string            `yaml:"url"`
	WorkingDir string            `yaml:"working_dir"`
}

type AuthConfigYAML struct {
	Type       string `yaml:"type"`
	Token      string `yaml:"token"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	HeaderName string `yaml:"header_name"`
}

type SchemaDiscoveryYAML struct {
	Enabled         bool     `yaml:"enabled"`
	CacheTTLSeconds int      `yaml:"cache_ttl_seconds"`
	IncludeTables   []string `yaml:"include_tables"`
	ExcludeTables   []string `yaml:"exclude_tables"`
}

type ToolGenerationYAML struct {
	Tools     []string `yaml:"tools"`
	EnableAll bool     `yaml:"enable_all"`
}

type HealthCheckYAML struct {
	Enabled         bool   `yaml:"enabled"`
	IntervalSeconds int    `yaml:"interval_seconds"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	Query           string `yaml:"query"`
}

// LoadBackend loads a backend configuration from a YAML file
func LoadBackend(path string) (*loomv1.BackendConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read backend file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := expandEnvVars(string(data))

	var yamlConfig BackendYAML
	if err := yaml.Unmarshal([]byte(dataStr), &yamlConfig); err != nil {
		return nil, fmt.Errorf("failed to parse backend YAML: %w", err)
	}

	// Validate structure
	if err := validateBackendYAML(&yamlConfig); err != nil {
		return nil, fmt.Errorf("invalid backend config: %w", err)
	}

	// Convert to proto
	backend := yamlToProtoBackend(&yamlConfig)

	// Resolve file paths (SSL certs, etc.)
	backendDir := filepath.Dir(path)
	if err := resolveBackendFilePaths(backend, backendDir); err != nil {
		return nil, fmt.Errorf("failed to resolve file paths: %w", err)
	}

	return backend, nil
}

// validateBackendYAML validates the YAML structure
func validateBackendYAML(yaml *BackendYAML) error {
	if yaml.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if yaml.APIVersion != "loom/v1" {
		return fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", yaml.APIVersion)
	}
	if yaml.Kind != "Backend" {
		return fmt.Errorf("kind must be 'Backend', got: %s", yaml.Kind)
	}
	if yaml.Name == "" {
		return fmt.Errorf("name is required")
	}
	if yaml.Type == "" {
		return fmt.Errorf("type is required")
	}

	// Validate backend type
	validTypes := map[string]bool{
		"postgres": true,
		"mysql":    true,
		"sqlite":   true,
		"file":     true,
		"rest":     true,
		"graphql":  true,
		"grpc":     true,
		"mcp":      true,
	}
	if !validTypes[strings.ToLower(yaml.Type)] {
		return fmt.Errorf("invalid backend type: %s (must be: postgres, mysql, sqlite, file, rest, graphql, grpc, mcp)", yaml.Type)
	}

	// Validate connection config is provided for the specified type
	switch strings.ToLower(yaml.Type) {
	case "postgres", "mysql", "sqlite":
		if yaml.Database == nil {
			return fmt.Errorf("database connection config is required for type: %s", yaml.Type)
		}
		if yaml.Database.DSN == "" {
			return fmt.Errorf("database.dsn is required")
		}
	case "file":
		if yaml.Database == nil {
			return fmt.Errorf("database connection config is required for file backend (use dsn to specify base directory)")
		}
		if yaml.Database.DSN == "" {
			return fmt.Errorf("database.dsn is required (base directory path for file backend)")
		}
	case "mcp":
		if yaml.MCP == nil {
			return fmt.Errorf("mcp connection config is required for type: mcp")
		}
		// Validate transport type
		if yaml.MCP.Transport != "" && yaml.MCP.Transport != "stdio" && yaml.MCP.Transport != "http" && yaml.MCP.Transport != "sse" {
			return fmt.Errorf("mcp.transport must be 'stdio', 'http', or 'sse'")
		}
		// For stdio transport, command is required
		if (yaml.MCP.Transport == "" || yaml.MCP.Transport == "stdio") && yaml.MCP.Command == "" {
			return fmt.Errorf("mcp.command is required for stdio transport")
		}
		// For http/sse transport, url is required
		if (yaml.MCP.Transport == "http" || yaml.MCP.Transport == "sse") && yaml.MCP.URL == "" {
			return fmt.Errorf("mcp.url is required for http/sse transport")
		}
	case "rest":
		if yaml.Rest == nil {
			return fmt.Errorf("rest connection config is required for type: rest")
		}
		if yaml.Rest.BaseURL == "" {
			return fmt.Errorf("rest.base_url is required")
		}
	case "graphql":
		if yaml.GraphQL == nil {
			return fmt.Errorf("graphql connection config is required for type: graphql")
		}
		if yaml.GraphQL.Endpoint == "" {
			return fmt.Errorf("graphql.endpoint is required")
		}
	case "grpc":
		if yaml.GRPC == nil {
			return fmt.Errorf("grpc connection config is required for type: grpc")
		}
		if yaml.GRPC.Address == "" {
			return fmt.Errorf("grpc.address is required")
		}
	}

	// Validate auth config if provided
	if yaml.Rest != nil && yaml.Rest.Auth != nil {
		if err := validateAuthConfig(yaml.Rest.Auth); err != nil {
			return fmt.Errorf("rest.auth: %w", err)
		}
	}
	if yaml.GraphQL != nil && yaml.GraphQL.Auth != nil {
		if err := validateAuthConfig(yaml.GraphQL.Auth); err != nil {
			return fmt.Errorf("graphql.auth: %w", err)
		}
	}

	return nil
}

// validateAuthConfig validates authentication configuration
func validateAuthConfig(auth *AuthConfigYAML) error {
	if auth.Type == "" {
		return fmt.Errorf("type is required")
	}

	validAuthTypes := map[string]bool{
		"bearer": true,
		"basic":  true,
		"apikey": true,
		"oauth2": true,
	}
	if !validAuthTypes[strings.ToLower(auth.Type)] {
		return fmt.Errorf("invalid auth type: %s (must be: bearer, basic, apikey, oauth2)", auth.Type)
	}

	switch strings.ToLower(auth.Type) {
	case "bearer", "apikey":
		if auth.Token == "" {
			return fmt.Errorf("token is required for auth type: %s", auth.Type)
		}
	case "basic":
		if auth.Username == "" || auth.Password == "" {
			return fmt.Errorf("username and password are required for basic auth")
		}
	}

	return nil
}

// yamlToProtoBackend converts YAML to proto
func yamlToProtoBackend(yaml *BackendYAML) *loomv1.BackendConfig {
	backend := &loomv1.BackendConfig{
		Name:        yaml.Name,
		Description: yaml.Description,
		Type:        strings.ToLower(yaml.Type),
	}

	// Set connection config based on type
	if yaml.Database != nil {
		backend.Connection = &loomv1.BackendConfig_Database{
			Database: &loomv1.DatabaseConnection{
				Dsn:                      yaml.Database.DSN,
				MaxConnections:           int32(yaml.Database.MaxConnections),
				MaxIdleConnections:       int32(yaml.Database.MaxIdleConnections),
				ConnectionTimeoutSeconds: int32(yaml.Database.ConnectionTimeoutSeconds),
				EnableSsl:                yaml.Database.EnableSSL,
				SslCertPath:              yaml.Database.SSLCertPath,
			},
		}
	}

	if yaml.Rest != nil {
		backend.Connection = &loomv1.BackendConfig_Rest{
			Rest: &loomv1.RestConnection{
				BaseUrl:        yaml.Rest.BaseURL,
				Auth:           convertAuthConfig(yaml.Rest.Auth),
				Headers:        yaml.Rest.Headers,
				TimeoutSeconds: int32(yaml.Rest.TimeoutSeconds),
				MaxRetries:     int32(yaml.Rest.MaxRetries),
			},
		}
	}

	if yaml.GraphQL != nil {
		backend.Connection = &loomv1.BackendConfig_Graphql{
			Graphql: &loomv1.GraphQLConnection{
				Endpoint:       yaml.GraphQL.Endpoint,
				Auth:           convertAuthConfig(yaml.GraphQL.Auth),
				Headers:        yaml.GraphQL.Headers,
				TimeoutSeconds: int32(yaml.GraphQL.TimeoutSeconds),
			},
		}
	}

	if yaml.GRPC != nil {
		backend.Connection = &loomv1.BackendConfig_Grpc{
			Grpc: &loomv1.GRPCConnection{
				Address:        yaml.GRPC.Address,
				UseTls:         yaml.GRPC.UseTLS,
				CertPath:       yaml.GRPC.CertPath,
				Metadata:       yaml.GRPC.Metadata,
				TimeoutSeconds: int32(yaml.GRPC.TimeoutSeconds),
			},
		}
	}

	if yaml.MCP != nil {
		backend.Connection = &loomv1.BackendConfig_Mcp{
			Mcp: &loomv1.MCPConnection{
				Command:    yaml.MCP.Command,
				Args:       yaml.MCP.Args,
				Env:        yaml.MCP.Env,
				Transport:  yaml.MCP.Transport,
				Url:        yaml.MCP.URL,
				WorkingDir: yaml.MCP.WorkingDir,
			},
		}
	}

	// Schema discovery
	if yaml.SchemaDiscovery != nil {
		backend.SchemaDiscovery = &loomv1.SchemaDiscoveryConfig{
			Enabled:         yaml.SchemaDiscovery.Enabled,
			CacheTtlSeconds: int32(yaml.SchemaDiscovery.CacheTTLSeconds),
			IncludeTables:   yaml.SchemaDiscovery.IncludeTables,
			ExcludeTables:   yaml.SchemaDiscovery.ExcludeTables,
		}
	}

	// Tool generation
	if yaml.ToolGeneration != nil {
		backend.ToolGeneration = &loomv1.ToolGenerationConfig{
			Tools:     yaml.ToolGeneration.Tools,
			EnableAll: yaml.ToolGeneration.EnableAll,
		}
	}

	// Health check
	if yaml.HealthCheck != nil {
		backend.HealthCheck = &loomv1.HealthCheckConfig{
			Enabled:         yaml.HealthCheck.Enabled,
			IntervalSeconds: int32(yaml.HealthCheck.IntervalSeconds),
			TimeoutSeconds:  int32(yaml.HealthCheck.TimeoutSeconds),
			Query:           yaml.HealthCheck.Query,
		}
	}

	// Set defaults
	if backend.GetDatabase() != nil && backend.GetDatabase().MaxConnections == 0 {
		backend.GetDatabase().MaxConnections = 10
	}
	if backend.GetDatabase() != nil && backend.GetDatabase().ConnectionTimeoutSeconds == 0 {
		backend.GetDatabase().ConnectionTimeoutSeconds = 30
	}
	if backend.GetRest() != nil && backend.GetRest().TimeoutSeconds == 0 {
		backend.GetRest().TimeoutSeconds = 30
	}
	if backend.GetGraphql() != nil && backend.GetGraphql().TimeoutSeconds == 0 {
		backend.GetGraphql().TimeoutSeconds = 30
	}
	if backend.GetGrpc() != nil && backend.GetGrpc().TimeoutSeconds == 0 {
		backend.GetGrpc().TimeoutSeconds = 30
	}

	return backend
}

// convertAuthConfig converts auth YAML to proto
func convertAuthConfig(yaml *AuthConfigYAML) *loomv1.AuthConfig {
	if yaml == nil {
		return nil
	}

	return &loomv1.AuthConfig{
		Type:       strings.ToLower(yaml.Type),
		Token:      yaml.Token,
		Username:   yaml.Username,
		Password:   yaml.Password,
		HeaderName: yaml.HeaderName,
	}
}

// resolveBackendFilePaths makes file paths absolute
func resolveBackendFilePaths(backend *loomv1.BackendConfig, backendDir string) error {
	// Resolve SSL cert path for database
	if db := backend.GetDatabase(); db != nil && db.SslCertPath != "" {
		db.SslCertPath = resolveRelativePath(backendDir, db.SslCertPath)
	}

	// Resolve cert path for gRPC
	if grpc := backend.GetGrpc(); grpc != nil && grpc.CertPath != "" {
		grpc.CertPath = resolveRelativePath(backendDir, grpc.CertPath)
	}

	return nil
}

// resolveRelativePath resolves a relative path to absolute
func resolveRelativePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// expandEnvVars expands environment variables in YAML content
func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}
