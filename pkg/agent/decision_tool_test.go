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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/session"
)

func TestDecideToolRegistrationFollowsExposeTool(t *testing.T) {
	t.Parallel()
	llm := rerankReplyLLM{reply: "1"}

	// Layer off: no tool.
	off := NewAgent(nil, llm, WithName("off"))
	off.checkAndRegisterDecideTool()
	assert.False(t, off.tools.IsRegistered(DecideToolName))

	// Layer on, tool surface not requested: no tool.
	quiet := NewAgent(nil, llm, WithName("quiet"),
		WithDecisionRouter(decision.NewRouter(decisionmock.New())),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: DecisionProviderMock}, nil))
	quiet.checkAndRegisterDecideTool()
	assert.False(t, quiet.tools.IsRegistered(DecideToolName))

	// Layer on and expose_tool: registered once, idempotent.
	on := NewAgent(nil, llm, WithName("on"),
		WithDecisionRouter(decision.NewRouter(decisionmock.New())),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: DecisionProviderMock, ExposeTool: true}, nil))
	assert.True(t, on.tools.IsRegistered(DecideToolName), "registered at construction")
	before := len(on.tools.ListTools())
	on.checkAndRegisterDecideTool()
	assert.Len(t, on.tools.ListTools(), before, "idempotent")

	// Suppressed builtin wins.
	sup := NewAgent(nil, llm, WithName("sup"),
		WithDecisionRouter(decision.NewRouter(decisionmock.New())),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: DecisionProviderMock, ExposeTool: true}, nil),
		WithoutBuiltinTool(DecideToolName))
	assert.False(t, sup.tools.IsRegistered(DecideToolName))
}

func TestDecideToolExecuteRecordsAndAnswers(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().SetModel("mock-jev").SetUsage(20, 0, 0.0002).AnswerNoul(sites.QAsk, 0.9)
	store := &memShadowStore{}
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec)),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: DecisionProviderMock, ExposeTool: true}, store))
	require.True(t, ag.tools.IsRegistered(DecideToolName))
	tool := NewDecisionTool(ag)

	ctx := session.WithSessionID(context.Background(), "sess-42")
	res, err := tool.Execute(ctx, map[string]interface{}{
		"question": "Did the process run out of memory?",
		"facts":    "exit status 137, dmesg shows oom-killer",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%v", res.Error)
	ans, ok := res.Data.(*sites.AskAnswer)
	require.True(t, ok)
	assert.Equal(t, sites.AskYesNo, ans.Kind)
	assert.Equal(t, true, ans.Answer)
	assert.InDelta(t, 0.9, *ans.PYes, 1e-9)
	assert.Equal(t, "mock-jev", ans.Model)
	assert.Equal(t, sites.SiteAsk, res.Metadata["decision.site"])

	// The call is recorded under the session, at the ask site, with no reference.
	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteAsk})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "sess-42", rows[0].SessionId)
	assert.Equal(t, sites.QAsk, rows[0].QuestionId)
	assert.Empty(t, rows[0].ReferenceAnswer)
	assert.Equal(t, 1, dec.CallCount())
}

func TestDecideToolExecuteErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		decider  *decisionmock.Decider
		input    map[string]interface{}
		wantCode string
	}{
		{
			name:     "bad input never reaches the decider",
			decider:  decisionmock.New(),
			input:    map[string]interface{}{"question": "q"},
			wantCode: "INVALID_PARAMETER",
		},
		{
			name:     "unknown kind",
			decider:  decisionmock.New(),
			input:    map[string]interface{}{"question": "q", "facts": "f", "kind": "maybe"},
			wantCode: "INVALID_PARAMETER",
		},
		{
			name:     "decider error",
			decider:  decisionmock.New().SetError(errors.New("gateway 503")),
			input:    map[string]interface{}{"question": "q", "facts": "f"},
			wantCode: "DECIDER_ERROR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec"),
				WithDecisionRouter(decision.NewRouter(tt.decider)))
			res, err := NewDecisionTool(ag).Execute(context.Background(), tt.input)
			require.NoError(t, err)
			require.False(t, res.Success)
			require.NotNil(t, res.Error)
			assert.Equal(t, tt.wantCode, res.Error.Code)
		})
	}

	t.Run("layer off", func(t *testing.T) {
		ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("off"))
		res, err := NewDecisionTool(ag).Execute(context.Background(), map[string]interface{}{"question": "q", "facts": "f"})
		require.NoError(t, err)
		require.False(t, res.Success)
		assert.Equal(t, "DECISION_LAYER_OFF", res.Error.Code)
	})
}

func TestDecideToolChoiceAndScale(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().
		AnswerChoice(sites.QAsk, map[string]float64{"transient": 0.8, "auth": 0.1, decision.NoneOption: 0.1})
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec"), WithDecisionRouter(decision.NewRouter(dec)))
	res, err := NewDecisionTool(ag).Execute(context.Background(), map[string]interface{}{
		"question": "what kind of failure?", "kind": "choice", "facts": "connection reset by peer",
		"options": []interface{}{"transient", "auth"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%v", res.Error)
	ans := res.Data.(*sites.AskAnswer)
	assert.Equal(t, "transient", ans.Answer)
	require.Len(t, ans.Probabilities, 3)
	assert.Equal(t, "transient", ans.Probabilities[0].Option)

	dec2 := decisionmock.New().AnswerScore(sites.QAsk, map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7})
	ag2 := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec2"), WithDecisionRouter(decision.NewRouter(dec2)))
	res, err = NewDecisionTool(ag2).Execute(context.Background(), map[string]interface{}{
		"question": "how severe?", "kind": "scale", "facts": "all writes failing",
		"options": []interface{}{"low", "medium", "high"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%v", res.Error)
	ans = res.Data.(*sites.AskAnswer)
	assert.Equal(t, "high", ans.Answer)
	assert.InDelta(t, 1.6, *ans.ExpectedLevel, 1e-9)
}
