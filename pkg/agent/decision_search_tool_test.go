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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/shuttle"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"github.com/teradata-labs/loom/pkg/types"
)

// toolRerankLLM answers the tool registry's rerank prompt keeping 1 and 2.
type toolRerankLLM struct{}

func (toolRerankLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: `[{"index": 1, "score": 0.9, "reason": "m"}, {"index": 2, "score": 0.6, "reason": "p"}]`}, nil
}
func (toolRerankLLM) Name() string  { return "rerank-fake" }
func (toolRerankLLM) Model() string { return "fake-1" }

// Two agents on one shared tool registry, built the way looms serve and
// Registry.buildAgent build them: each agent's tool_search carries that
// agent's decision layer and nothing else (review #409 F2).
func TestAgentsShareToolRegistryButNotDecisionRouters(t *testing.T) {
	treg, err := toolregistry.New(toolregistry.Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: toolRerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = treg.Close() })
	ctx := context.Background()
	for _, name := range []string{"send_slack", "send_email", "send_webhook"} {
		require.NoError(t, treg.RegisterTool(ctx, &loomv1.IndexedTool{
			Id: "custom:" + name, Name: name, Description: "send a message via " + name,
			Source: loomv1.ToolSource_TOOL_SOURCE_CUSTOM,
		}))
	}

	agentA := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("a")) // no decision layer
	assert.Nil(t, agentA.SearchToolOptions(), "an agent with no decision layer gets no router")

	decB := decisionmock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.9).AnswerNoul("c2", 0.1)
	storeB := &memShadowStore{}
	agentB := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("b"),
		WithDecisionRouter(decision.NewRouter(decB)), WithDecisionShadowStore(storeB))
	require.Len(t, agentB.SearchToolOptions(), 1)

	stA := toolregistry.NewSearchTool(treg, agentA.SearchToolOptions()...)
	stB := toolregistry.NewSearchTool(treg, agentB.SearchToolOptions()...)

	run := func(st *toolregistry.SearchTool, sessionID string) {
		res, err := st.Execute(decision.WithSessionID(ctx, sessionID), map[string]interface{}{"query": "send", "mode": "balanced"})
		require.NoError(t, err)
		require.True(t, res.Success)
	}

	run(stA, "sess-a")
	agentA.WaitDecisionShadows()
	agentB.WaitDecisionShadows()
	assert.Equal(t, 0, decB.CallCount(), "agent A's tool_search reached agent B's decider")

	run(stB, "sess-b")
	agentB.WaitDecisionShadows() // the binding's Track runs on B's WaitGroup
	assert.Equal(t, 1, decB.CallCount())
	rows, err := storeB.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteToolSearchRerank})
	require.NoError(t, err)
	require.Len(t, rows, 3, "B's shadow rows are in B's store once B's wait returns")
	for _, r := range rows {
		assert.Equal(t, "sess-b", r.SessionId)
	}

	// A later agent C does not rewire B.
	decC := decisionmock.New().AnswerNoul("c0", 0.1).AnswerNoul("c1", 0.1).AnswerNoul("c2", 0.1)
	agentC := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("c"), WithDecisionRouter(decision.NewRouter(decC)))
	_ = toolregistry.NewSearchTool(treg, agentC.SearchToolOptions()...)
	run(stB, "sess-b")
	agentB.WaitDecisionShadows()
	assert.Equal(t, 2, decB.CallCount())
	assert.Equal(t, 0, decC.CallCount(), "B's tool_search reached C's decider")
}
