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

// Characterization of the sequential tool-batch dispatch (dispatchOneCall /
// commitToolRow in agent.go). These pass on today's code on purpose: they pin
// row order, failure isolation, in-turn dedup, the MaxToolExecutions cut-off and
// the contact_human / AwaitResource paths, so a later refactor of the dispatch
// can prove it changed none of them.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// dispatchProbeTool runs fn for each call and records the "v" input of every
// execution, in execution order.
type dispatchProbeTool struct {
	name string
	fn   func(v string) (*shuttle.Result, error)

	mu   sync.Mutex
	runs []string
}

func (t *dispatchProbeTool) Name() string        { return t.name }
func (t *dispatchProbeTool) Description() string { return "dispatch probe" }
func (t *dispatchProbeTool) Backend() string     { return "" }
func (t *dispatchProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *dispatchProbeTool) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	v, _ := params["v"].(string)
	t.mu.Lock()
	t.runs = append(t.runs, v)
	t.mu.Unlock()
	if t.fn != nil {
		return t.fn(v)
	}
	return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
}

func (t *dispatchProbeTool) executed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.runs...)
}

func probeCall(id, tool, v string) llmtypes.ToolCall {
	return llmtypes.ToolCall{ID: id, Name: tool, Input: map[string]interface{}{"v": v}}
}

// dispatchTestAgent builds an agent over a recording LLM that serves the given
// scripted responses, with the supplied tools registered.
func dispatchTestAgent(t *testing.T, maxToolExec int, responses []mockLLMResponse, tools ...shuttle.Tool) (*Agent, *recordingToolLLM) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	if maxToolExec > 0 {
		cfg.MaxToolExecutions = maxToolExec
	}
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: responses}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	for _, tl := range tools {
		ag.RegisterTool(tl)
	}
	return ag, llm
}

// toolRows returns the session's tool rows in append order.
func toolRows(t *testing.T, ag *Agent, sessionID string) []Message {
	t.Helper()
	sess, ok := ag.GetSession(sessionID)
	require.True(t, ok, "session %q exists", sessionID)
	var rows []Message
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" {
			rows = append(rows, m)
		}
	}
	return rows
}

func toolRowIDs(rows []Message) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ToolUseID
	}
	return ids
}

func TestDispatch_CommitsRowsInCallOrder(t *testing.T) {
	probe := &dispatchProbeTool{name: "probe_a"}
	ag, _ := dispatchTestAgent(t, 0, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			probeCall("id-A", "probe_a", "A"),
			probeCall("id-B", "probe_a", "B"),
			probeCall("id-C", "probe_a", "C"),
		}},
		{content: "done"},
	}, probe)

	resp, err := ag.Chat(context.Background(), "dispatch-order", "go")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)

	require.Equal(t, []string{"A", "B", "C"}, probe.executed(), "tools execute in call order")

	rows := toolRows(t, ag, "dispatch-order")
	require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows), "rows commit in call order, each paired with its ToolUseID")
	for i, want := range []string{"A", "B", "C"} {
		require.Contains(t, rows[i].Content, "ran:"+want, "row %d carries its own call's result", i)
		require.NotNil(t, rows[i].ToolResult)
		require.Equal(t, "ran:"+want, rows[i].ToolResult.Data)
	}

	require.Len(t, resp.ToolExecutions, 3)
	for i, want := range []string{"A", "B", "C"} {
		require.Equal(t, want, resp.ToolExecutions[i].Input["v"], "execution record %d is in call order", i)
	}
}

func TestDispatch_FailedCallDoesNotStopBatch(t *testing.T) {
	cases := []struct {
		name    string
		failure func(v string) (*shuttle.Result, error)
		// substring the middle row must carry
		wantMid string
	}{
		{
			name: "failed result",
			failure: func(v string) (*shuttle.Result, error) {
				if v == "B" {
					return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "boom_code", Message: "boom message"}}, nil
				}
				return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
			},
			wantMid: "boom message",
		},
		{
			name: "go error",
			failure: func(v string) (*shuttle.Result, error) {
				if v == "B" {
					return nil, context.DeadlineExceeded
				}
				return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
			},
			wantMid: "deadline exceeded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &dispatchProbeTool{name: "probe_f", fn: tc.failure}
			ag, llm := dispatchTestAgent(t, 0, []mockLLMResponse{
				{toolCalls: []llmtypes.ToolCall{
					probeCall("id-A", "probe_f", "A"),
					probeCall("id-B", "probe_f", "B"),
					probeCall("id-C", "probe_f", "C"),
				}},
				{content: "done"},
			}, probe)

			resp, err := ag.Chat(context.Background(), "dispatch-fail", "go")
			require.NoError(t, err)
			require.Equal(t, "done", resp.Content)

			require.Equal(t, []string{"A", "B", "C"}, probe.executed(), "a failed middle call does not stop the batch")

			rows := toolRows(t, ag, "dispatch-fail")
			require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows))
			require.Contains(t, rows[0].Content, "ran:A")
			require.Contains(t, rows[1].Content, tc.wantMid)
			require.NotContains(t, rows[1].Content, "ran:B")
			require.Contains(t, rows[2].Content, "ran:C")

			// The model sees all three results on its next request.
			llm.recMu.Lock()
			defer llm.recMu.Unlock()
			require.Len(t, llm.seen, 2)
			var seenIDs []string
			for _, m := range llm.seen[1] {
				if m.Role == "tool" {
					seenIDs = append(seenIDs, m.ToolUseID)
				}
			}
			require.Equal(t, []string{"id-A", "id-B", "id-C"}, seenIDs, "the follow-up provider call carries all three tool results")
		})
	}
}

func TestDispatch_IdenticalCallReusesResult(t *testing.T) {
	probe := &dispatchProbeTool{name: "probe_d"}
	ag, _ := dispatchTestAgent(t, 0, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			probeCall("id-1", "probe_d", "same"),
			probeCall("id-2", "probe_d", "same"),
			probeCall("id-3", "probe_d", "other"),
		}},
		{content: "done"},
	}, probe)

	resp, err := ag.Chat(context.Background(), "dispatch-dedup", "go")
	require.NoError(t, err)

	require.Equal(t, []string{"same", "other"}, probe.executed(), "the identical second call does not execute the tool again")

	rows := toolRows(t, ag, "dispatch-dedup")
	require.Equal(t, []string{"id-1", "id-2", "id-3"}, toolRowIDs(rows))
	require.Contains(t, rows[0].Content, "ran:same")
	require.NotContains(t, rows[0].Content, "deduplicated")
	require.Contains(t, rows[1].Content, "ran:same", "the duplicate row carries the first call's result")
	require.Contains(t, rows[1].Content, "(deduplicated — reused result from identical call in this turn)")
	// Dedup reuses the cached result of the first call (identity is an
	// implementation detail; the observable contract is the same outcome).
	require.NotNil(t, rows[1].ToolResult)
	require.Equal(t, rows[0].ToolResult.Success, rows[1].ToolResult.Success)
	require.Equal(t, rows[0].ToolResult.Data, rows[1].ToolResult.Data)
	require.Contains(t, rows[2].Content, "ran:other")
	require.NotContains(t, rows[2].Content, "deduplicated")

	// Every call still gets an execution record, the duplicate included.
	require.Len(t, resp.ToolExecutions, 3)
}

func TestDispatch_CapStopsBatchAtMaxToolExecutions(t *testing.T) {
	probe := &dispatchProbeTool{name: "probe_cap"}
	ag, _ := dispatchTestAgent(t, 2, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			probeCall("id-1", "probe_cap", "1"),
			probeCall("id-2", "probe_cap", "2"),
			probeCall("id-3", "probe_cap", "3"),
			probeCall("id-4", "probe_cap", "4"),
		}},
		{content: "synthesized"},
	}, probe)

	resp, err := ag.Chat(context.Background(), "dispatch-cap", "go")
	require.NoError(t, err)
	require.Equal(t, "synthesized", resp.Content, "the exhausted budget ends in the tool-free synthesis call")

	require.Equal(t, []string{"1", "2"}, probe.executed(), "exactly the first two calls execute")
	require.Len(t, resp.ToolExecutions, 2)

	rows := toolRows(t, ag, "dispatch-cap")
	// Pinned deliberately: tool_use ids past the cap get no tool_result row.
	// The prepare/run/settle dispatch refactor left this unchanged; changing it
	// is a separate decision.
	require.Equal(t, []string{"id-1", "id-2"}, toolRowIDs(rows), "calls past the cap get no row at all")
}

func TestDispatch_ContactHumanAndAwaitUnchanged(t *testing.T) {
	t.Run("contact_human takes the HITL branch and its row stays in call order", func(t *testing.T) {
		const answer = "use the replica"
		store := shuttle.NewInMemoryHumanRequestStore()
		probe := &dispatchProbeTool{name: "probe_h"}
		ag, _ := dispatchTestAgent(t, 0, []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				probeCall("id-A", "probe_h", "A"),
				{ID: "id-H", Name: "contact_human", Input: map[string]interface{}{"question": "which db?"}},
				probeCall("id-C", "probe_h", "C"),
			}},
			{content: "done"},
		}, probe, shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
			Store:        store,
			Timeout:      30 * time.Second, // returns as soon as answered; slack for one-time setup under -race
			PollInterval: 10 * time.Millisecond,
		}))

		// Local responder with a long deadline: the shared helper's 3s window
		// starts before the first Chat's ~2.4s setup and can expire first.
		responderCtx, stopResponder := context.WithCancel(context.Background())
		t.Cleanup(stopResponder)
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) && responderCtx.Err() == nil {
				if pending, err := store.ListPending(responderCtx); err == nil && len(pending) > 0 {
					_ = store.RespondToRequest(responderCtx, pending[0].ID, "responded", answer, "reviewer@example.com", nil)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()

		var events []ProgressEvent
		var evMu sync.Mutex
		resp, err := ag.ChatWithProgress(context.Background(), "dispatch-hitl", "go", func(e ProgressEvent) {
			evMu.Lock()
			events = append(events, e)
			evMu.Unlock()
		})
		require.NoError(t, err)
		require.Equal(t, "done", resp.Content)

		// The HITL branch emitted its own progress stage for the call.
		evMu.Lock()
		var hitl int
		for _, e := range events {
			if e.Stage == StageHumanInTheLoop {
				hitl++
			}
		}
		evMu.Unlock()
		require.GreaterOrEqual(t, hitl, 1, "contact_human raises a human-in-the-loop progress event")

		// The sibling calls ran; the human's answer is the contact_human result.
		require.Equal(t, []string{"A", "C"}, probe.executed())
		rows := toolRows(t, ag, "dispatch-hitl")
		require.Equal(t, []string{"id-A", "id-H", "id-C"}, toolRowIDs(rows), "the HITL row sits at its call position")
		require.NotNil(t, rows[1].ToolResult)
		require.True(t, rows[1].ToolResult.Success)
		data, ok := rows[1].ToolResult.Data.(map[string]interface{})
		require.True(t, ok, "contact_human returns a map payload")
		require.Equal(t, answer, data["response"])
	})

	t.Run("AwaitResource without a handler passes through as an ordinary row", func(t *testing.T) {
		job := &awaitingTool{name: "start_job", uri: "gdp://jobs/42"}
		sibling := &dispatchProbeTool{name: "probe_s"}

		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "await.db"), observability.NewNoOpTracer())
		require.NoError(t, err)
		t.Cleanup(func() { _ = sessions.Close() })

		llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{
				{ID: "id-J", Name: "start_job", Input: map[string]interface{}{"v": "j"}},
				probeCall("id-S", "probe_s", "S"),
			}},
			{content: "continued"},
		}}}
		// No WithResourceAwait / WithHITLPark: nothing can park.
		ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithMemory(NewMemoryWithStore(sessions)))
		ag.RegisterTool(job)
		ag.RegisterTool(sibling)

		resp, err := ag.Chat(context.Background(), "dispatch-await", "go")
		require.NoError(t, err, "no handler wired: the turn is not parked")
		require.Equal(t, "continued", resp.Content)

		require.EqualValues(t, 1, job.runs.Load())
		rows := toolRows(t, ag, "dispatch-await")
		require.Equal(t, []string{"id-J", "id-S"}, toolRowIDs(rows), "the await result commits a normal row, in order")
		require.NotNil(t, rows[0].ToolResult)
		require.True(t, rows[0].ToolResult.Success)
		require.NotNil(t, rows[0].ToolResult.AwaitResource, "the result is passed through untouched")
		require.Equal(t, "gdp://jobs/42", rows[0].ToolResult.AwaitResource.URI)
		require.Contains(t, rows[0].Content, "working")
	})
}

func TestDispatch_AdmissionDeniedMiddleCallStaysInOrder(t *testing.T) {
	allowed := &dispatchProbeTool{name: "probe_ok"}
	denied := &dispatchProbeTool{name: "probe_denied"}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			probeCall("id-A", "probe_ok", "A"),
			probeCall("id-B", "probe_denied", "B"),
			probeCall("id-C", "probe_ok", "C"),
		}},
		{content: "done"},
	}}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedDenyHook{tool: "probe_denied"}}, nil, nil)))
	ag.RegisterTool(allowed)
	ag.RegisterTool(denied)

	resp, err := ag.Chat(context.Background(), "dispatch-deny", "go")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)

	require.Equal(t, []string{"A", "C"}, allowed.executed(), "the calls around a denied call still run")
	require.Empty(t, denied.executed(), "a denied call's body never runs")

	rows := toolRows(t, ag, "dispatch-deny")
	require.Equal(t, []string{"id-A", "id-B", "id-C"}, toolRowIDs(rows), "the denial row sits at its call position")
	require.Contains(t, rows[0].Content, "ran:A")
	require.Contains(t, rows[1].Content, "denied by policy")
	require.NotNil(t, rows[1].ToolResult)
	require.False(t, rows[1].ToolResult.Success)
	require.Equal(t, "permission_denied", rows[1].ToolResult.Error.Code)
	require.Contains(t, rows[2].Content, "ran:C")
}

// A call whose tool breaker is already open never ran, so the recovered row
// comes from tier-1 recovery and the executed-call bookkeeping (guardrails
// error tracking) must not fire for it.
func TestDispatch_BreakerOpenSkipsLeaseAndGuardrails(t *testing.T) {
	flaky := &dispatchProbeTool{name: "probe_flaky"}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.EnableSelfHealing = true
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{probeCall("id-A", "probe_flaky", "A")}},
		{content: "done"},
	}}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(flaky)

	// Trip the tool's breaker the way production does.
	breaker := ag.circuitBreakers.GetBreaker(ag.executor.CanonicalToolName("probe_flaky"))
	for i := 0; i < fabric.DefaultCircuitBreakerConfig().FailureThreshold; i++ {
		_ = breaker.Execute(func() error { return context.DeadlineExceeded })
	}

	// Seed a guardrails record: an executed failed call would bump AttemptCount.
	seed := &fabric.ErrorAnalysisInfo{ErrorType: "seed", Summary: "seed"}
	ag.guardrails.HandleErrorWithAnalysis(context.Background(), "dispatch-brk", "", seed)
	require.Equal(t, 1, ag.guardrails.GetErrorRecord("dispatch-brk").AttemptCount)

	resp, err := ag.Chat(context.Background(), "dispatch-brk", "go")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)

	require.Empty(t, flaky.executed(), "an open breaker rejects the call before the body runs")

	rows := toolRows(t, ag, "dispatch-brk")
	require.Equal(t, []string{"id-A"}, toolRowIDs(rows))
	require.NotNil(t, rows[0].ToolResult)
	require.False(t, rows[0].ToolResult.Success)
	require.Equal(t, "tool_disabled", rows[0].ToolResult.Error.Code, "tier-1 recovery supplies the row")

	rec := ag.guardrails.GetErrorRecord("dispatch-brk")
	require.NotNil(t, rec, "guardrails record not cleared")
	require.Equal(t, 1, rec.AttemptCount, "guardrails error tracking is skipped for a call that never ran")
}
