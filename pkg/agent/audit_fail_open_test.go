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
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/builder"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// errAuditSinkDown is the injected failure returned by the audit write sink.
var errAuditSinkDown = errors.New("audit sink unavailable: injected write failure")

// failingToolExecStore wraps a working SessionStorage but fails every
// SaveToolExecution — the audit write sink is down. All other persistence
// (sessions, messages) delegates to the embedded store so the conversation still
// runs to the point of the audit write.
type failingToolExecStore struct {
	agent.SessionStorage
	mu    sync.Mutex
	calls int
}

func (f *failingToolExecStore) SaveToolExecution(ctx context.Context, sessionID string, exec agent.ToolExecution) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return errAuditSinkDown
}

func (f *failingToolExecStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestAudit_FailOpen_WriteFailureLeavesDecisionUnchanged drives a governed,
// audited tool call whose audit write to storage fails, and asserts the fail-open
// contract (ac5): the failed audit write does not alter the call's admission
// decision and does not fail the call — audit is observability, not enforcement —
// while the miss is traced on the tool-execution span.
func TestAudit_FailOpen_WriteFailureLeavesDecisionUnchanged(t *testing.T) {
	tracer := newTestTracer()
	backend := &mockBackend{}

	// Two-turn mock LLM: first requests the audited tool, then answers.
	llmProvider := &mockLLMProvider{
		name:  "test-llm",
		model: "test-model",
		responses: []agent.LLMResponse{
			{
				StopReason: "tool_use",
				ToolCalls: []llmtypes.ToolCall{
					{
						ID:    "call_1",
						Name:  "audited_tool",
						Input: map[string]interface{}{"q": "x"},
					},
				},
			},
			{
				Content:    "done",
				StopReason: "end_turn",
			},
		},
	}

	// A governed call: a real audit binding produces the decision and the
	// executor stamps it onto the result metadata ("admission.decision" is a
	// reserved key — a tool-forged value would be stripped); the agent reads it
	// into ToolExecution.AdmissionDecision.
	auditedTool := &shuttle.MockTool{
		MockName:        "audited_tool",
		MockDescription: "a governed, audited tool",
		MockBackend:     "test",
		MockExecute: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: "ok"}, nil
		},
	}
	auditChain, err := shuttle.BuildChainFromConfig(shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
		{Kind: "audit", Scope: "audited_tool"},
	}}, shuttle.ChainDeps{})
	require.NoError(t, err)

	// Working base store so sessions/messages persist; SaveToolExecution fails.
	base, err := agent.NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	sink := &failingToolExecStore{SessionStorage: base}

	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := builder.NewInstrumentedAgent(backend, llmProvider, tracer,
		agent.WithConfig(cfg),
		agent.WithMemory(agent.NewMemoryWithStore(sink)),
		agent.WithAdmissionHooks(auditChain),
	)
	ag.RegisterTool(auditedTool)

	response, err := ag.Chat(context.Background(), "audit-session", "run it")

	// Fail-open: the failed audit write does not fail the call.
	require.NoError(t, err, "a failed audit write must not fail the call (fail-open, not enforcement)")
	require.NotNil(t, response)

	// The audit write was attempted and failed.
	assert.GreaterOrEqual(t, sink.callCount(), 1, "the audit write path must have been exercised")

	// Decision unchanged: the call still carries its admission decision despite the
	// failed write.
	require.Len(t, response.ToolExecutions, 1)
	assert.Equal(t, "allow", response.ToolExecutions[0].AdmissionDecision,
		"the failed audit write must leave the call's admission decision unchanged")

	// The miss is traced: the tool-execution span records the audit write error.
	tracer.mu.Lock()
	spans := tracer.spans
	tracer.mu.Unlock()

	var traced bool
	for _, span := range spans {
		if span.Name != "agent.tool_execution" {
			continue
		}
		if span.Status.Code == observability.StatusError &&
			span.Attributes[observability.AttrErrorMessage] == errAuditSinkDown.Error() {
			traced = true
		}
	}
	assert.True(t, traced,
		"the audit write miss must be recorded on the tool-execution span")
}
