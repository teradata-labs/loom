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

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// wireRoleRecorderLLM captures the Role of every message it receives, so the
// test can assert on exactly what reached the "provider."
type wireRoleRecorderLLM struct {
	lastRoles []string
}

func (w *wireRoleRecorderLLM) Chat(_ context.Context, messages []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	w.lastRoles = nil
	for _, m := range messages {
		w.lastRoles = append(w.lastRoles, m.Role)
	}
	return &LLMResponse{Content: "ok", Usage: llmtypes.Usage{TotalTokens: 1}}, nil
}
func (w *wireRoleRecorderLLM) Name() string  { return "wire-role-recorder" }
func (w *wireRoleRecorderLLM) Model() string { return "wire-role-recorder-model" }

// The provider must see skill_body, hygiene_injection, empty_response_retry,
// and synthesis_prompt as "user" — the LLM-attention optimization this whole
// design exists to preserve.
func TestChatWithRetry_FoldsSyntheticRolesToUserOnWire(t *testing.T) {
	llm := &wireRoleRecorderLLM{}
	a := &Agent{id: "wire-role-test", llm: llm, config: &Config{}}
	ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

	input := []Message{
		{Role: "system", Content: "rom"},
		{Role: "skill_body", Content: "## Skill: SQL Optimization"},
		{Role: "hygiene_injection", Content: "fix your last response"},
		{Role: "empty_response_retry", Content: "your previous response was empty"},
		{Role: "synthesis_prompt", Content: "you must provide your final answer now"},
		{Role: "assistant", Content: "ok"},
	}

	_, err := a.chatWithRetry(ctx, input, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"system", "user", "user", "user", "user", "assistant"}, llm.lastRoles)

	// The caller's slice must be untouched — the fold builds a copy.
	require.Equal(t, "skill_body", input[1].Role, "fold must not mutate the caller's slice")
	require.Equal(t, "hygiene_injection", input[2].Role, "fold must not mutate the caller's slice")
	require.Equal(t, "empty_response_retry", input[3].Role, "fold must not mutate the caller's slice")
	require.Equal(t, "synthesis_prompt", input[4].Role, "fold must not mutate the caller's slice")
}

// The common case — no synthetic roles present — must not allocate a
// throwaway copy on every turn. normalizeWireRoles returns the input slice
// itself (same backing array) when there is nothing to fold.
func TestNormalizeWireRoles_NoOpReturnsSameSliceWhenNothingToFold(t *testing.T) {
	input := []Message{
		{Role: "system", Content: "rom"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok"},
	}

	out := normalizeWireRoles(input)

	require.Len(t, out, len(input))
	// require.Same checks pointer identity (==), not require.Equal's
	// reflect.DeepEqual — DeepEqual would report two *different* arrays with
	// equal contents as "equal" too, which would defeat this test's purpose.
	require.Same(t, &input[0], &out[0], "no fold needed: must return the same backing array, not a copy")
}
