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
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// refuseThenToolCallLLM refuses its first call with ErrContextTooLong, then
// answers with a tool call: the resend after a relief pass is the second place
// the loop records an llm_call_result.
type refuseThenToolCallLLM struct {
	mu    sync.Mutex
	calls int
}

func (l *refuseThenToolCallLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls == 1 {
		return nil, fmt.Errorf("prompt is too long: %w", llm.ErrContextTooLong)
	}
	return &llmtypes.LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "counter", Input: map[string]interface{}{}}}}, nil
}
func (l *refuseThenToolCallLLM) Name() string  { return "mock-refuse-then-tool" }
func (l *refuseThenToolCallLLM) Model() string { return "mock-v1" }

// The resend after a relief pass obeys the same rule as the first send: its tool
// calls do not run until its llm_call_result is durable.
func TestRunConversationLoop_LLMResultWriteFailureAfterReliefStopsBeforeTools(t *testing.T) {
	log := &failingLLMResultLog{recordingEventLog: &recordingEventLog{}}
	tool := &execCountTool{}
	l := &refuseThenToolCallLLM{}
	ag := reliefSeamAgent(t, l, "s-llm-result-relief", NewSegmentedMemory("ROM", 12000, 1000), WithEventLog(log))
	ag.tools.Register(tool)

	_, err := ag.Chat(context.Background(), "s-llm-result-relief", strings.Repeat("lorem ipsum ", 5300))
	require.Error(t, err, "Chat succeeded although the llm_call_result could not be recorded")
	assert.Equal(t, 2, l.calls, "one refusal, one resend")
	assert.Zero(t, tool.runs.Load(), "the tool must not run before its response is recorded")
}

// A registry hands its unknown-outcome policy to every agent it builds, next to
// the event log; without it a configured "fail" would silently become "stop".
func TestRegistry_BuiltAgentCarriesUnknownOutcomePolicy(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(RegistryConfig{
		ConfigDir:            dir,
		DBPath:               dir + "/registry.db",
		LLMProvider:          &mockLLMProvider{},
		Logger:               zap.NewNop(),
		EventLogStore:        &recordingEventLog{},
		UnknownOutcomePolicy: UnknownOutcomeFail,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	ag, err := reg.buildAgent(context.Background(), createTestAgentConfig("outcome-policy"))
	require.NoError(t, err)
	assert.Equal(t, UnknownOutcomeFail, ag.unknownOutcome)
}
