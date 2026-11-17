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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/memory"
)

// TestRegistryWaitDecisionShadows is the guard for short-lived processes:
// a shadow started on an agent's last turn is recorded before the registry
// reports it is safe to exit.
func TestRegistryWaitDecisionShadows(t *testing.T) {
	tmp := t.TempDir()
	reg, err := NewRegistry(RegistryConfig{ConfigDir: tmp, DBPath: filepath.Join(tmp, "r.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	// Empty registry: returns at once.
	reg.WaitDecisionShadows()

	store := &memShadowStore{}
	dec := decisionmock.New().AnswerNoul("c0", 0.9).SetLatency(50 * time.Millisecond)
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("slow"),
		WithDecisionRouter(decision.NewRouter(dec)),
		WithDecisionShadowStore(store))
	reg.mu.Lock()
	reg.agents["slow"] = ag
	reg.mu.Unlock()

	_ = ag.rerankMemories(context.Background(), "q", []*memory.Memory{{ID: "m1", Content: "x"}})

	reg.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the row landed before Wait returned")
}

// Review #410 minor d: with tools.builtin set, the registry's builtin filter
// unregistered every framework tool not on the list, including "decide", so
// decision.expose_tool: true did nothing. expose_tool now counts as asking
// for the tool; without it the filter still removes nothing it did not.
func TestRegistryBuiltinFilterKeepsDecideWhenExposed(t *testing.T) {
	tests := []struct {
		name       string
		exposeTool bool
		want       bool
	}{
		{"expose_tool true", true, true},
		{"expose_tool false", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, _ := createTestRegistry(t)
			config := createTestAgentConfig("decide_filter")
			config.Tools = &loomv1.ToolsConfig{Builtin: []string{"shell_execute"}}
			config.Decision = &loomv1.DecisionConfig{Provider: DecisionProviderMock, ExposeTool: tt.exposeTool}

			ag, err := registry.buildAgent(context.Background(), config)
			require.NoError(t, err)
			tools := ag.ListTools()
			assert.Contains(t, tools, "shell_execute")
			if tt.want {
				assert.Contains(t, tools, DecideToolName)
			} else {
				assert.NotContains(t, tools, DecideToolName)
			}
		})
	}
}
