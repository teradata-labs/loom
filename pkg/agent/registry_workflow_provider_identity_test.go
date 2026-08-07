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

package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"go.uber.org/zap"
)

func TestRegistry_LoadWorkflows_BedrockSDKProviderUsesCanonicalIdentity(t *testing.T) {
	provider, err := bedrock.NewSDKClient(bedrock.Config{
		Region:      "us-east-1",
		BearerToken: "test-token",
		ModelID:     "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
	})
	require.NoError(t, err)

	configDir := t.TempDir()
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   configDir,
		DBPath:      filepath.Join(configDir, "registry.db"),
		LLMProvider: provider,
		Logger:      zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, registry.Close())
	})

	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: bedrock-provider-workflow
spec:
  type: pipeline
  stages:
    - agent_id: researcher
      prompt_template: "{{input}}"
`

	workflowPath := filepath.Join(configDir, "workflows", "workflow.yaml")
	require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

	require.NoError(t, registry.LoadWorkflows(context.Background()))

	agentsByName := make(map[string]bool)
	for _, info := range registry.ListAgents() {
		agentsByName[info.Name] = true
	}
	require.Equal(t, map[string]bool{
		"bedrock-provider-workflow":            true,
		"bedrock-provider-workflow:researcher": true,
	}, agentsByName)

	configs := registry.ListConfigs()
	require.Len(t, configs, 2)
	for _, config := range configs {
		require.NotNil(t, config.Llm)
		require.Equal(t, "bedrock", config.Llm.Provider)
	}
}
