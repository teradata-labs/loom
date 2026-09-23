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

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryBuiltAgentKeepsSystemPrompt: an agent built from its YAML by
// the registry (the path `looms workflow run` and dynamic gRPC creation use)
// runs with the system prompt the YAML declares. On the rig every such agent
// had lost it, and debaters and voters configured to take a side answered on
// the merits instead.
func TestRegistryBuiltAgentKeepsSystemPrompt(t *testing.T) {
	registry, tmpDir := createTestRegistry(t)
	t.Cleanup(func() { _ = registry.Close() })
	setupAgentConfig(t, registry, tmpDir, "stance-agent")

	ctx := context.Background()
	_, err := registry.CreateAgent(ctx, "stance-agent")
	require.NoError(t, err)
	ag, err := registry.GetAgent(ctx, "stance-agent")
	require.NoError(t, err)
	require.NotNil(t, ag.GetConfig())
	assert.Equal(t, createTestAgentConfig("stance-agent").SystemPrompt, ag.GetConfig().SystemPrompt)
	assert.Equal(t, "stance-agent", ag.GetName())
	assert.Equal(t, "Test agent", ag.GetConfig().Description)
}
