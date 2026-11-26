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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// The skill body must be persisted under its own role, not "user" — that is
// the whole point of this change. buildSkillsCutoverRig, alphaBodySentinel,
// mockToolCallingLLM/mockLLMResponse (agent_integration_test.go:988-995),
// and loadCall/finalTurn (manage_skills_tool_black_box_test.go:231-241) are
// all already defined in the agent package's existing test files.
func TestSkillBodySidecar_PersistedUnderSkillBodyRole(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		finalTurn(),
	}}
	rig := buildSkillsCutoverRig(t, llm, false)

	ctx := context.Background()
	_, err := rig.agent.Chat(ctx, "sess-role-check", "load alpha")
	require.NoError(t, err)

	sess := rig.agent.memory.GetOrCreateSessionWithAgent(ctx, "sess-role-check", rig.agent.config.Name, "")

	// NOTE: deviates from the brief's literal loop body (see task-6-report.md
	// "Concerns"): the brief's require.NotEqual(t, "user", m.Role) ran
	// unconditionally over every message, which also fails on the genuine
	// human turn ("load alpha", legitimately Role: "user") — making the test
	// unable to pass even after the producer flip. Scoped here to the sidecar
	// message itself (identified by alphaBodySentinel), matching what the
	// docstring above says the test verifies.
	found := false
	for _, m := range sess.GetMessages() {
		if !strings.Contains(m.Content, alphaBodySentinel) {
			continue
		}
		found = true
		require.Equal(t, "skill_body", m.Role, "the skill body must be persisted under role \"skill_body\", not \"user\"")
	}
	require.True(t, found, "the skill body must be persisted under role \"skill_body\"")
}

// The empty-response retry nudge must be persisted under its own role, not
// "user". Fixture mirrors TestAgent_EmptyResponseRetry
// (agent_integration_test.go): a tool call turn, then an empty LLM
// response (which fires the one-shot retry nudge), then a real response.
func TestEmptyResponseRetry_PersistedUnderEmptyResponseRetryRole(t *testing.T) {
	mockLLM := &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{
				content:   "",
				toolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}}},
			},
			{content: ""}, // empty response — should trigger the retry nudge
			{content: "The answer is 2 based on my work"},
		},
	}

	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(&Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		PatternConfig:     patternCfg,
	}))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "empty-retry-role-check", "Calculate 1+1")
	require.NoError(t, err)
	require.Equal(t, "The answer is 2 based on my work", resp.Content)

	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "empty-retry-role-check", ag.config.Name, "")

	const nudgeSentinel = "Your previous response was empty"
	found := false
	for _, m := range sess.GetMessages() {
		if !strings.Contains(m.Content, nudgeSentinel) {
			continue
		}
		found = true
		require.Equal(t, "empty_response_retry", m.Role, "the empty-response retry nudge must be persisted under role \"empty_response_retry\", not \"user\"")
	}
	require.True(t, found, "the empty-response retry nudge must be persisted")
}

// The synthesis prompt must be persisted under its own role, not "user".
// Fixture mirrors TestAgent_MaxTurnsLimit (agent_integration_test.go): an
// LLM that keeps requesting tools indefinitely, exhausting MaxTurns and
// forcing synthesizeFinalResponse to fire the synthesis prompt.
func TestSynthesisPrompt_PersistedUnderSynthesisPromptRole(t *testing.T) {
	mockLLM := &mockToolCallingLLM{
		alwaysCallTools: true,
	}

	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(&Config{
		MaxTurns:          5,
		MaxToolExecutions: 50,
		PatternConfig:     patternCfg,
	}))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	_, err := ag.Chat(ctx, "synthesis-prompt-role-check", "Keep calling tools")
	require.NoError(t, err)

	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "synthesis-prompt-role-check", ag.config.Name, "")

	const synthesisSentinel = "You must provide your final answer NOW"
	found := false
	for _, m := range sess.GetMessages() {
		if !strings.Contains(m.Content, synthesisSentinel) {
			continue
		}
		found = true
		require.Equal(t, "synthesis_prompt", m.Role, "the synthesis prompt must be persisted under role \"synthesis_prompt\", not \"user\"")
	}
	require.True(t, found, "the synthesis prompt must be persisted")
}
