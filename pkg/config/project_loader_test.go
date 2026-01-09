// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProject(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantErr  bool
		errMsg   string
		validate func(t *testing.T, project interface{})
	}{
		{
			name: "minimal valid project",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: test-project
  version: 1.0.0
spec:
  agents:
    - config_file: ./agents/test.yaml
`,
			wantErr: false,
		},
		{
			name: "full project with all fields",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: full-project
  version: 2.0.0
  description: Complete test project
  labels:
    team: platform
    env: dev
spec:
  observability:
    enabled: true
    hawk_endpoint: http://localhost:8080
    export_traces: true
    export_metrics: true
    tags:
      service: loom

  prompts:
    provider: promptio
    endpoint: http://localhost:9000
    cache_enabled: true
    cache_ttl_seconds: 3600

  backends:
    - config_file: ./backends/postgres.yaml

  agents:
    - config_file: ./agents/sql_expert.yaml

  workflows:
    - config_file: ./workflows/review.yaml

  evals:
    - config_file: ./evals/quality.yaml

  patterns:
    - config_file: ./patterns/sql.yaml

  settings:
    default_timeout_seconds: 600
    max_concurrent_agents: 20
    debug_mode: true
    log_level: debug
`,
			wantErr: false,
		},
		{
			name: "missing apiVersion",
			yaml: `kind: Project
metadata:
  name: test
spec: {}
`,
			wantErr: true,
			errMsg:  "apiVersion is required",
		},
		{
			name: "wrong apiVersion",
			yaml: `apiVersion: loom/v2
kind: Project
metadata:
  name: test
spec: {}
`,
			wantErr: true,
			errMsg:  "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec: {}
`,
			wantErr: true,
			errMsg:  "kind must be 'Project'",
		},
		{
			name: "missing name",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  version: 1.0.0
spec: {}
`,
			wantErr: true,
			errMsg:  "metadata.name is required",
		},
		{
			name: "env var expansion",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: test
spec:
  observability:
    hawk_endpoint: ${TEST_HAWK_ENDPOINT}
  agents:
    - config_file: ./agents/test.yaml
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			projectFile := filepath.Join(tmpDir, "loom.yaml")

			// Create dummy referenced files to avoid validation errors
			agentsDir := filepath.Join(tmpDir, "agents")
			_ = os.MkdirAll(agentsDir, 0755)
			_ = os.WriteFile(filepath.Join(agentsDir, "test.yaml"), []byte("# test"), 0644)
			_ = os.WriteFile(filepath.Join(agentsDir, "sql_expert.yaml"), []byte("# test"), 0644)

			backendsDir := filepath.Join(tmpDir, "backends")
			_ = os.MkdirAll(backendsDir, 0755)
			_ = os.WriteFile(filepath.Join(backendsDir, "postgres.yaml"), []byte("# test"), 0644)

			workflowsDir := filepath.Join(tmpDir, "workflows")
			_ = os.MkdirAll(workflowsDir, 0755)
			_ = os.WriteFile(filepath.Join(workflowsDir, "review.yaml"), []byte("# test"), 0644)

			evalsDir := filepath.Join(tmpDir, "evals")
			_ = os.MkdirAll(evalsDir, 0755)
			_ = os.WriteFile(filepath.Join(evalsDir, "quality.yaml"), []byte("# test"), 0644)

			patternsDir := filepath.Join(tmpDir, "patterns")
			_ = os.MkdirAll(patternsDir, 0755)
			_ = os.WriteFile(filepath.Join(patternsDir, "sql.yaml"), []byte("# test"), 0644)

			// Set env var for test
			os.Setenv("TEST_HAWK_ENDPOINT", "http://test:8080")
			defer os.Unsetenv("TEST_HAWK_ENDPOINT")

			// Write project file
			err := os.WriteFile(projectFile, []byte(tt.yaml), 0644)
			require.NoError(t, err)

			// Load project
			project, err := LoadProject(projectFile)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, project)

			// Basic validations
			assert.NotNil(t, project.Metadata)
			assert.NotNil(t, project.Spec)

			if tt.validate != nil {
				tt.validate(t, project)
			}
		})
	}
}

func TestValidateProject(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid project",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: test
spec:
  agents:
    - config_file: ./agents/test.yaml
`,
			wantErr: false,
		},
		{
			name: "invalid log level",
			yaml: `apiVersion: loom/v1
kind: Project
metadata:
  name: test
spec:
  settings:
    log_level: invalid
  agents:
    - config_file: ./agents/test.yaml
`,
			wantErr: true,
			errMsg:  "invalid log level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			projectFile := filepath.Join(tmpDir, "loom.yaml")

			// Create dummy referenced files
			agentsDir := filepath.Join(tmpDir, "agents")
			_ = os.MkdirAll(agentsDir, 0755)
			_ = os.WriteFile(filepath.Join(agentsDir, "test.yaml"), []byte("# test"), 0644)

			err := os.WriteFile(projectFile, []byte(tt.yaml), 0644)
			require.NoError(t, err)

			project, err := LoadProject(projectFile)
			require.NoError(t, err)

			err = ValidateProject(project)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestEnvVarExpansion(t *testing.T) {
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	input := "value is ${TEST_VAR}"
	result := expandEnvVars(input)
	assert.Equal(t, "value is test_value", result)
}

func TestResolveRelativePath(t *testing.T) {
	tests := []struct {
		name    string
		baseDir string
		path    string
		want    string
	}{
		{
			name:    "relative path",
			baseDir: "/project",
			path:    "./agents/test.yaml",
			want:    "/project/agents/test.yaml",
		},
		{
			name:    "absolute path unchanged",
			baseDir: "/project",
			path:    "/absolute/path/test.yaml",
			want:    "/absolute/path/test.yaml",
		},
		{
			name:    "parent directory",
			baseDir: "/project/subdir",
			path:    "../agents/test.yaml",
			want:    "/project/agents/test.yaml", // filepath.Join cleans paths
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveRelativePath(tt.baseDir, tt.path)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestMCPConfigConversion(t *testing.T) {
	yaml := &MCPServersConfigYAML{
		Servers: map[string]MCPServerConfigYAML{
			"filesystem": {
				Enabled:        true,
				Transport:      "stdio",
				Command:        "npx",
				Args:           []string{"-y", "@modelcontextprotocol/server-filesystem"},
				TimeoutSeconds: 30,
				Tools: MCPToolSelectionYAML{
					Include: []string{"read_file", "write_file"},
				},
				Env: map[string]string{
					"NODE_ENV": "production",
				},
			},
			"memory": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-memory"},
				Tools: MCPToolSelectionYAML{
					All: true,
				},
			},
		},
	}

	proto := convertMCPConfig(yaml)

	require.NotNil(t, proto)
	assert.Len(t, proto.Servers, 2)

	// Check filesystem server
	fs := proto.Servers["filesystem"]
	require.NotNil(t, fs)
	assert.True(t, fs.Enabled)
	assert.Equal(t, "stdio", fs.Transport)
	assert.Equal(t, "npx", fs.Command)
	assert.Len(t, fs.Args, 2)
	assert.Equal(t, int32(30), fs.TimeoutSeconds)
	assert.Equal(t, "production", fs.Env["NODE_ENV"])

	// Check tool selection (include)
	includeTools := fs.Tools.GetInclude()
	require.NotNil(t, includeTools)
	assert.Len(t, includeTools.Tools, 2)

	// Check memory server
	mem := proto.Servers["memory"]
	require.NotNil(t, mem)
	assert.True(t, mem.Tools.GetAll())
}
