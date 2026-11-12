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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// wireModel is tokModel as a client that reports the max_tokens it sends
// (llmtypes.MaxTokensReporter), as every loom client does. Its refusal line is
// the model's window less that figure.
type wireModel struct {
	*tokModel
	sends int
}

// MaxTokens reports the max_tokens this client puts on each request.
func (m wireModel) MaxTokens() int { return m.sends }

var _ llmtypes.MaxTokensReporter = wireModel{}

// rowsTool returns a tool whose every call returns about resultTokens tokens.
func rowsTool(resultTokens int) *shuttle.MockTool {
	line := "id=12345 name=widget qty=7\n"
	per := GetTokenCounter().CountTokens(strings.Repeat(line, 100)) / 100
	rows := strings.Repeat(line, resultTokens/per)
	return &shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(_ context.Context, in map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: fmt.Sprintf("table %v\n%s", in["i"], rows)}, nil
		},
	}
}

// An agent that names a pool entry runs on the pool entry's client, whose
// max_tokens is the entry's, while the agent's config holds the server's
// llm.max_tokens. The pool client here sends 64,000 on claude-sonnet-4-5, so
// the provider takes a prompt up to 200,000 - 64,000. A 56-result step of
// about 137k tokens is over that line. Fitted against the server's 4,096 it
// was sent, refused without a label, and the turn ended.
func TestAStepIsFittedAgainstWhatAPoolEntrySends(t *testing.T) {
	model := wireModel{
		tokModel: &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 64000, fanOut: 56},
		sends:    64000,
	}
	registry, _ := createTestRegistry(t)
	registry.maxTokens = 4096
	registry.SetProviderPool(map[string]LLMProvider{"big": model})
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:           "pooled",
		SystemPrompt:   "Read the tables.",
		ActiveProvider: "big",
		Behavior: &loomv1.BehaviorConfig{
			MaxToolExecutions: 200,
			MaxIterations:     200,
			Patterns:          &loomv1.PatternConfig{Enabled: false},
		},
	})
	ag, err := registry.CreateAgent(context.Background(), "pooled")
	require.NoError(t, err, "the registry must build the pooled agent")
	require.Equal(t, 4096, ag.config.MaxTokens, "the agent's config holds the server's max_tokens, not the pool entry's")
	ag.RegisterTool(rowsTool(2450))

	resp, err := ag.Chat(context.Background(), "s-pool", "read every table")
	require.NoError(t, err, "a step over the pool client's line was sent and refused")
	assert.Equal(t, "narrowed and done", resp.Content, "the model must be told and re-plan")
	assert.True(t, model.told, "the step must be replaced before the send")
}

// A runtime model switch replaces the agent's client and leaves its config as
// it was built. The step is fitted against what the new client sends, in both
// directions: a larger max_tokens lowers the line, and a smaller one raises it.
func TestAStepIsFittedAgainstWhatTheSwitchedClientSends(t *testing.T) {
	tests := []struct {
		name        string
		builtWith   int32 // the agent's own llm.max_tokens when it was built
		clientSends int   // the max_tokens of the client switched to
		wantAnswer  string
		wantTold    bool
	}{
		{
			name:        "the new client sends more: the step is caught",
			builtWith:   4096,
			clientSends: 64000,
			wantAnswer:  "narrowed and done",
			wantTold:    true,
		},
		{
			name:        "the new client sends less: the step goes whole",
			builtWith:   64000,
			clientSends: 4096,
			wantAnswer:  "read them all",
			wantTold:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - int(tt.builtWith), fanOut: 56}
			ag := realLineAgent(t, first, &loomv1.LLMConfig{MaxTokens: tt.builtWith}, 4096, 2450)
			require.Equal(t, int(tt.builtWith), ag.config.MaxTokens, "the config holds the max_tokens the agent was built with")

			switched := wireModel{
				tokModel: &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - tt.clientSends, fanOut: 56},
				sends:    tt.clientSends,
			}
			ag.SetLLMProvider(switched)

			resp, err := ag.Chat(context.Background(), "s-switch", "read every table")
			require.NoError(t, err, "a step over the switched client's line was sent and refused")
			assert.Equal(t, tt.wantAnswer, resp.Content, "the turn must end as the client's real line decides")
			assert.Equal(t, tt.wantTold, switched.told, "the step is replaced only when it is over the client's real line")
		})
	}
}
