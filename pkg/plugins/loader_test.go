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

package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/plugins"
)

const minimalPlugin = `
apiVersion: loom/v1
kind: Plugin
metadata:
  name: test-plugin
  description: A test plugin for unit tests
  version: "1.0.0"
trigger:
  slash_commands: [/test]
  keywords: [test, demo]
skills:
  - name: test-skill
    required: true
    description: Core skill for testing
`

const fullPlugin = `
apiVersion: loom/v1
kind: Plugin
metadata:
  name: teradata-perf
  title: Teradata Performance
  description: Analyze and optimize Teradata SQL query performance
  version: "2.1.0"
  author: loom-core
  domains: [teradata, sql, performance]
  labels:
    surface: teradata
  type: domain
  risk_level: medium
  require_approval: true
trigger:
  slash_commands: [/td-perf, /explain-plan]
  keywords: [slow query, explain plan, query optimization]
  min_confidence: 0.75
  description: Analyze and optimize Teradata SQL query performance
workflows:
  - name: query-optimizer
    min_version: "1.0.0"
    required: true
    description: End-to-end query optimization workflow
skills:
  - name: teradata-sql
    min_version: "2.0.0"
    required: true
    description: SQL analysis for Teradata databases
  - name: explain-plan
    required: false
    synthesize: true
    description: Parse and analyze Teradata EXPLAIN output to identify bottlenecks
agents:
  - id: dba-agent
    role: primary
    required: false
    synthesize: true
    description: Database administrator for Teradata optimization tasks
mcp_tools:
  - tool_name: td_explain
    required: true
    description: Execute EXPLAIN on a Teradata query
install:
  auto_register_skills: true
  auto_configure_mcp: true
default_binding_mode: LAZY
resolution:
  on_required_missing: FAIL
  on_optional_missing: SKIP_WARN
  resynthesize_on_activation: true
`

func TestParsePlugin_Minimal(t *testing.T) {
	p, err := plugins.ParsePlugin([]byte(minimalPlugin), "")
	require.NoError(t, err)
	assert.Equal(t, "test-plugin", p.Name)
	assert.Equal(t, "A test plugin for unit tests", p.Description)
	assert.Equal(t, "1.0.0", p.Version)
	assert.Equal(t, []string{"/test"}, p.Trigger.SlashCommands)
	assert.Equal(t, []string{"test", "demo"}, p.Trigger.Keywords)
	assert.Equal(t, 0.7, p.Trigger.MinConfidence) // default
	require.Len(t, p.SkillRefs, 1)
	assert.Equal(t, "test-skill", p.SkillRefs[0].Name)
	assert.True(t, p.SkillRefs[0].Required)
	// Defaults
	assert.Equal(t, "LAZY", p.DefaultBindingMode)
	assert.Equal(t, "FAIL", p.Resolution.OnRequiredMissing)
	assert.Equal(t, "SKIP_WARN", p.Resolution.OnOptionalMissing)
}

func TestParsePlugin_Full(t *testing.T) {
	p, err := plugins.ParsePlugin([]byte(fullPlugin), "")
	require.NoError(t, err)

	assert.Equal(t, "teradata-perf", p.Name)
	assert.Equal(t, "Teradata Performance", p.Title)
	assert.Equal(t, "2.1.0", p.Version)
	assert.Equal(t, []string{"teradata", "sql", "performance"}, p.Domains)

	require.Len(t, p.WorkflowRefs, 1)
	assert.Equal(t, "query-optimizer", p.WorkflowRefs[0].Name)
	assert.True(t, p.WorkflowRefs[0].Required)

	require.Len(t, p.SkillRefs, 2)
	assert.Equal(t, "teradata-sql", p.SkillRefs[0].Name)
	assert.True(t, p.SkillRefs[0].Required)
	assert.False(t, p.SkillRefs[0].Synthesize)
	assert.Equal(t, "explain-plan", p.SkillRefs[1].Name)
	assert.False(t, p.SkillRefs[1].Required)
	assert.True(t, p.SkillRefs[1].Synthesize)
	assert.NotEmpty(t, p.SkillRefs[1].Description)

	require.Len(t, p.AgentRefs, 1)
	assert.Equal(t, "dba-agent", p.AgentRefs[0].ID)
	assert.True(t, p.AgentRefs[0].Synthesize)

	require.Len(t, p.MCPToolRefs, 1)
	assert.Equal(t, "td_explain", p.MCPToolRefs[0].ToolName)

	assert.Equal(t, "LAZY", p.DefaultBindingMode)
	assert.True(t, p.Resolution.ResynthesizeOnActivation)

	assert.Equal(t, "domain", p.Type)
	assert.Equal(t, "medium", p.RiskLevel)
	assert.True(t, p.RequireApproval)
	assert.False(t, p.IsHighRisk()) // "medium" is not high/restricted
}

func TestPlugin_IsHighRisk(t *testing.T) {
	cases := []struct {
		riskLevel string
		want      bool
	}{
		{"high", true},
		{"HIGH", true},
		{"restricted", true},
		{"RESTRICTED", true},
		{"medium", false},
		{"low", false},
		{"", false},
	}
	for _, tc := range cases {
		p := &plugins.Plugin{
			Name:        "x",
			Description: "d",
			RiskLevel:   tc.riskLevel,
		}
		assert.Equal(t, tc.want, p.IsHighRisk(), "risk_level=%q", tc.riskLevel)
	}
}

func TestParsePlugin_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "wrong apiVersion",
			yaml:    "apiVersion: loom/v2\nkind: Plugin\nmetadata:\n  name: x\n  description: d\nskills:\n  - name: s\n",
			wantErr: "apiVersion",
		},
		{
			name:    "wrong kind",
			yaml:    "apiVersion: loom/v1\nkind: Skill\nmetadata:\n  name: x\n  description: d\nskills:\n  - name: s\n",
			wantErr: "kind",
		},
		{
			name:    "missing name",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  description: d\nskills:\n  - name: s\n",
			wantErr: "name is required",
		},
		{
			name:    "invalid name",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: My_Plugin\n  description: d\nskills:\n  - name: s\n",
			wantErr: "kebab-case",
		},
		{
			name:    "missing description",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: my-plugin\nskills:\n  - name: s\n",
			wantErr: "description is required",
		},
		{
			name:    "no refs",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: my-plugin\n  description: d\n",
			wantErr: "at least one",
		},
		{
			name:    "synthesize without description",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: my-plugin\n  description: d\nskills:\n  - name: s\n    synthesize: true\n",
			wantErr: "synthesize=true requires description",
		},
		{
			name:    "invalid binding mode",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: my-plugin\n  description: d\nskills:\n  - name: s\ndefault_binding_mode: SOMETIMES\n",
			wantErr: "default_binding_mode",
		},
		{
			name:    "invalid type",
			yaml:    "apiVersion: loom/v1\nkind: Plugin\nmetadata:\n  name: my-plugin\n  description: d\n  type: widget\nskills:\n  - name: s\n",
			wantErr: "metadata.type",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plugins.ParsePlugin([]byte(tc.yaml), "test.yaml")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadPlugin_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-plugin.yaml")
	require.NoError(t, os.WriteFile(path, []byte(minimalPlugin), 0o644))

	p, err := plugins.LoadPlugin(path)
	require.NoError(t, err)
	assert.Equal(t, "test-plugin", p.Name)
	assert.Equal(t, path, p.SourcePath)
}

func TestPluginToYAML_RoundTrip(t *testing.T) {
	original, err := plugins.ParsePlugin([]byte(fullPlugin), "")
	require.NoError(t, err)

	data, err := plugins.PluginToYAML(original)
	require.NoError(t, err)

	loaded, err := plugins.ParsePlugin(data, "")
	require.NoError(t, err)

	assert.Equal(t, original.Name, loaded.Name)
	assert.Equal(t, original.Version, loaded.Version)
	assert.Equal(t, original.DefaultBindingMode, loaded.DefaultBindingMode)
	assert.Equal(t, original.Resolution.OnRequiredMissing, loaded.Resolution.OnRequiredMissing)
	require.Len(t, loaded.SkillRefs, len(original.SkillRefs))
	assert.Equal(t, original.SkillRefs[1].Synthesize, loaded.SkillRefs[1].Synthesize)
	assert.Equal(t, original.SkillRefs[1].Description, loaded.SkillRefs[1].Description)
	assert.Equal(t, original.Type, loaded.Type)
	assert.Equal(t, original.RiskLevel, loaded.RiskLevel)
	assert.Equal(t, original.RequireApproval, loaded.RequireApproval)
}

func TestLoadPluginDir(t *testing.T) {
	dir := t.TempDir()

	// Write two valid plugins and one non-plugin file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(minimalPlugin), 0o644))

	plugin2 := strings.Replace(minimalPlugin, "name: test-plugin", "name: test-plugin-two", 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(plugin2), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore"), 0o644))

	ps, err := plugins.LoadPluginDir(dir)
	require.NoError(t, err)
	assert.Len(t, ps, 2)
}
