// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// recordingStopReasonLLM is scriptedStopReasonLLM that also keeps the messages
// of every request, so a test can read what the model was told after a turn.
type recordingStopReasonLLM struct {
	mu        sync.Mutex
	responses []scriptedResponse
	idx       int
	requests  [][]llmtypes.Message
}

func (m *recordingStopReasonLLM) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, append([]llmtypes.Message(nil), msgs...))
	if m.idx >= len(m.responses) {
		return &llmtypes.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return &llmtypes.LLMResponse{
		Content:    r.content,
		ToolCalls:  r.toolCalls,
		StopReason: r.stopReason,
		Usage:      llmtypes.Usage{InputTokens: 10, OutputTokens: 4096},
	}, nil
}

func (m *recordingStopReasonLLM) Name() string  { return "recording-stop-reason" }
func (m *recordingStopReasonLLM) Model() string { return "recording-v1" }

// requestAt returns the messages of the n-th LLM request (0-based).
func (m *recordingStopReasonLLM) requestAt(t *testing.T, n int) []llmtypes.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Greater(t, len(m.requests), n, "LLM was called %d times", len(m.requests))
	return m.requests[n]
}

// requiredCountingTool demands "key" and "value" (like shared_memory_write)
// and counts how many times its body ran.
type requiredCountingTool struct {
	name string
	runs atomic.Int64
}

func (t *requiredCountingTool) Name() string        { return t.name }
func (t *requiredCountingTool) Description() string { return "stores a value under a key" }
func (t *requiredCountingTool) Backend() string     { return "" }
func (t *requiredCountingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"key":   {Type: "string"},
			"value": {Type: "string"},
		},
		Required: []string{"key", "value"},
	}
}
func (t *requiredCountingTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	return &shuttle.Result{Success: true, Data: "stored"}, nil
}

func newTruncationAgent(t *testing.T, llm LLMProvider, tools ...shuttle.Tool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxTurns = 20
	cfg.MaxToolExecutions = 20
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	for _, tl := range tools {
		ag.RegisterTool(tl)
	}
	return ag
}

// toolRowFor returns the content of the tool row answering callID in msgs.
func toolRowFor(msgs []llmtypes.Message, callID string) (string, bool) {
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolUseID == callID {
			return m.Content, true
		}
	}
	return "", false
}

var cutOffWrite = llmtypes.ToolCall{
	ID:    "c-cut",
	Name:  "shared_memory_write",
	Input: map[string]interface{}{"key": "conf-intel-2027-ai-worldwide-discovery"},
}

func TestTruncatedToolCall_NotExecutedAndModelTold(t *testing.T) {
	tool := &requiredCountingTool{name: "shared_memory_write"}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{content: "Storing the full list now.", toolCalls: []llmtypes.ToolCall{cutOffWrite}, stopReason: "max_tokens"},
	}}
	ag := newTruncationAgent(t, llm, tool)

	resp, err := ag.Chat(context.Background(), "s-cut", "research")
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Content)
	assert.Zero(t, tool.runs.Load(), "a call cut off by max_tokens must not run")

	row, ok := toolRowFor(llm.requestAt(t, 1), "c-cut")
	require.True(t, ok, "the next request must answer the cut-off call")
	assert.Contains(t, row, outputTruncatedCode)
	assert.Contains(t, row, "output token limit (4096 tokens)")
	assert.Contains(t, row, "shared_memory_write")
	assert.Contains(t, row, "missing: value")
	assert.Contains(t, row, "smaller pieces")

	require.Len(t, resp.ToolExecutions, 1)
	require.NotNil(t, resp.ToolExecutions[0].Result)
	require.NotNil(t, resp.ToolExecutions[0].Result.Error)
	assert.Equal(t, outputTruncatedCode, resp.ToolExecutions[0].Result.Error.Code)
}

func TestTruncatedToolCall_CompleteCallInSameBatchStillRuns(t *testing.T) {
	tool := &requiredCountingTool{name: "shared_memory_write"}
	complete := llmtypes.ToolCall{
		ID:    "c-ok",
		Name:  "shared_memory_write",
		Input: map[string]interface{}{"key": "part-1", "value": "NeurIPS 2027"},
	}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{toolCalls: []llmtypes.ToolCall{complete, cutOffWrite}, stopReason: "max_tokens"},
	}}
	ag := newTruncationAgent(t, llm, tool)

	_, err := ag.Chat(context.Background(), "s-mixed", "research")
	require.NoError(t, err)
	assert.Equal(t, int64(1), tool.runs.Load(), "only the complete call runs")

	next := llm.requestAt(t, 1)
	okRow, ok := toolRowFor(next, "c-ok")
	require.True(t, ok)
	assert.NotContains(t, okRow, outputTruncatedCode)
	cutRow, ok := toolRowFor(next, "c-cut")
	require.True(t, ok)
	assert.Contains(t, cutRow, outputTruncatedCode)
}

// A missing required argument without a max_tokens stop is the model's own
// mistake: the call runs and the tool's validation answers it, as before.
func TestTruncatedToolCall_OrdinaryStopStillRunsTheCall(t *testing.T) {
	tool := &requiredCountingTool{name: "shared_memory_write"}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{toolCalls: []llmtypes.ToolCall{cutOffWrite}, stopReason: "tool_use"},
	}}
	ag := newTruncationAgent(t, llm, tool)

	_, err := ag.Chat(context.Background(), "s-ordinary", "research")
	require.NoError(t, err)
	assert.Equal(t, int64(1), tool.runs.Load())
	row, ok := toolRowFor(llm.requestAt(t, 1), "c-cut")
	require.True(t, ok)
	assert.NotContains(t, row, outputTruncatedCode)
}

// The output-token circuit breaker remains the backstop when the model keeps
// sending cut-off calls despite the notice.
func TestTruncatedToolCall_CircuitBreakerStillFires(t *testing.T) {
	tool := &requiredCountingTool{name: "shared_memory_write"}
	cut := scriptedResponse{toolCalls: []llmtypes.ToolCall{cutOffWrite}, stopReason: "max_tokens"}
	llm := &scriptedStopReasonLLM{responses: []scriptedResponse{cut, cut, cut, cut}}
	ag := newOutputCBAgent(t, 3, llm, tool)

	_, err := ag.Chat(context.Background(), "s-cb", "research")
	assert.True(t, isCircuitBreakerErr(err), "expected circuit breaker, got %v", err)
	assert.Zero(t, tool.runs.Load(), "no cut-off call may run on the way to the breaker")
}

// The notice comes from prompts/errors/tool_execution.yaml when a registry is
// loaded, and the shipped prompt renders the same facts as the fallback.
func TestOutputTruncatedMessage_FromPromptRegistry(t *testing.T) {
	registry := prompts.NewFileRegistry(filepath.Join("..", "..", "prompts"))
	require.NoError(t, registry.Reload(context.Background()))

	ag := newTruncationAgent(t, &recordingStopReasonLLM{})
	ag.prompts = registry
	fromRegistry := ag.outputTruncatedMessage(nil, "shared_memory_write", 4096, []string{"value"})

	ag.prompts = nil
	fallback := ag.outputTruncatedMessage(nil, "shared_memory_write", 4096, []string{"value"})

	assert.Equal(t, strings.TrimSpace(fallback), strings.TrimSpace(fromRegistry),
		"registry prompt and fallback must say the same thing")
	assert.Contains(t, fromRegistry, "(missing: value)")

	ag.prompts = registry
	noMissing := ag.outputTruncatedMessage(nil, "shared_memory_write", 4096, nil)
	assert.Contains(t, noMissing, "(missing: not determined)")
}

// A cut-off call is never put in front of a human: the park pre-scan skips
// it, and approving the parked batch runs only the complete call.
func TestTruncatedToolCall_NotParkedNorRunOnResume(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "park.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })
	store := shuttle.NewInMemoryHumanRequestStore()

	complete := llmtypes.ToolCall{
		ID:    "c-ok",
		Name:  "shared_memory_write",
		Input: map[string]interface{}{"key": "part-1", "value": "NeurIPS 2027"},
	}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{toolCalls: []llmtypes.ToolCall{complete, cutOffWrite}, stopReason: "max_tokens"},
	}}
	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "shared_memory_write"}}, nil, nil)),
	)
	tool := &requiredCountingTool{name: "shared_memory_write"}
	ag.RegisterTool(tool)

	_, err = ag.Chat(context.Background(), "s-park-cut", "research")
	var parked *TurnParkedError
	require.True(t, errors.As(err, &parked), "expected park, got %v", err)

	reqs, err := store.ListBySession(context.Background(), "s-park-cut")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"c-ok"}, paramKeys(reqs[0]), "only the complete call is offered for approval")

	_, err = ag.ResumeChat(context.Background(), "s-park-cut", ParkDecision{
		RequestID: reqs[0].ID,
		ItemIDs:   paramKeys(reqs[0]),
		Approved:  true,
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), tool.runs.Load(), "approval runs the complete call only")
}

// actionCountingTool requires only "action" in its schema, but its "write"
// action needs "filename" and "content" (like the workspace tool), declared
// through shuttle.ConditionalArgsTool.
type actionCountingTool struct {
	runs atomic.Int64
}

func (t *actionCountingTool) Name() string        { return "workspace" }
func (t *actionCountingTool) Description() string { return "reads and writes files" }
func (t *actionCountingTool) Backend() string     { return "" }
func (t *actionCountingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action":   {Type: "string"},
			"filename": {Type: "string"},
			"content":  {Type: "string"},
		},
		Required: []string{"action"},
	}
}
func (t *actionCountingTool) RequiredArgsFor(params map[string]interface{}) []string {
	if params["action"] == "write" {
		return []string{"filename", "content"}
	}
	return nil
}
func (t *actionCountingTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	return &shuttle.Result{Success: true, Data: "written"}, nil
}

// TestTruncatedToolCall_ActionDependentArgsCount covers a write cut off
// before its content: the schema's own required list is satisfied, so only
// the tool's call-dependent requirements show the call is incomplete.
func TestTruncatedToolCall_ActionDependentArgsCount(t *testing.T) {
	tool := &actionCountingTool{}
	cut := llmtypes.ToolCall{ID: "c-ws", Name: "workspace", Input: map[string]interface{}{"action": "write", "filename": "conferences.json"}}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{content: "Writing the file.", toolCalls: []llmtypes.ToolCall{cut}, stopReason: "max_tokens"},
	}}
	ag := newTruncationAgent(t, llm, tool)

	_, err := ag.Chat(context.Background(), "s-ws", "research")
	require.NoError(t, err)
	assert.Zero(t, tool.runs.Load(), "a write cut off before its content must not run")

	row, ok := toolRowFor(llm.requestAt(t, 1), "c-ws")
	require.True(t, ok)
	assert.Contains(t, row, outputTruncatedCode)
	assert.Contains(t, row, "missing: content")
}

// TestTruncatedToolCall_ActionDependentArgsCompleteCallRuns checks that a
// complete write in a max_tokens response still runs.
func TestTruncatedToolCall_ActionDependentArgsCompleteCallRuns(t *testing.T) {
	tool := &actionCountingTool{}
	whole := llmtypes.ToolCall{ID: "c-ok", Name: "workspace", Input: map[string]interface{}{"action": "write", "filename": "a.txt", "content": "x"}}
	llm := &recordingStopReasonLLM{responses: []scriptedResponse{
		{content: "Writing the file.", toolCalls: []llmtypes.ToolCall{whole}, stopReason: "max_tokens"},
	}}
	ag := newTruncationAgent(t, llm, tool)

	_, err := ag.Chat(context.Background(), "s-ok", "research")
	require.NoError(t, err)
	assert.Equal(t, int64(1), tool.runs.Load(), "a complete call must still run")
}
