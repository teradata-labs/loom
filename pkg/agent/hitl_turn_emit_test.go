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

// M1's driven-turn half: the pending-human signal is observed on the
// ProgressCallback handed to a real conversation turn — the same seat cloud's
// relay occupies — rather than on a callback the test installs on a context it
// hands straight to a tool. The turn is driven by the scripted mockToolCallingLLM
// over the real executor and admission chain (no model, no network): the run's
// callback enters ctx inside chat(), so a pass shows it survives the agent loop
// into executor.admit(), where the ask resolver runs with no callback of its own.
// Both origins are driven — a hook-held ask (config-built "ask" binding) and a
// contact_human question — and each hold is resolved mid-turn by a background
// actor through the store, so the turn's Decision/Result is read from what the
// loop recorded.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// turnProgressRecorder captures every ProgressEvent the driven turn delivers to
// its ProgressCallback. The callback is invoked from the loop goroutine and from
// the resolver's notify inside executor.admit(), so access is guarded.
type turnProgressRecorder struct {
	mu     sync.Mutex
	events []ProgressEvent
}

// callback returns the ProgressCallback to hand to ChatWithProgress.
func (r *turnProgressRecorder) callback() ProgressCallback {
	return func(e ProgressEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, e)
	}
}

// idBearingHITLEvents returns the recorded human-in-the-loop events that carry a
// request id. The id is what separates the authoritative pending card — emitted
// by the Notifier→ProgressEvent bridge once the row is stored — from the loop's
// id-less pre-creation ping for a contact_human call, which a stream consumer
// ignores.
func (r *turnProgressRecorder) idBearingHITLEvents() []ProgressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ProgressEvent
	for _, e := range r.events {
		if e.Stage == StageHumanInTheLoop && e.HITLRequest != nil && e.HITLRequest.RequestID != "" {
			out = append(out, e)
		}
	}
	return out
}

// buildAskChainForExecuteSQL builds, purely from config, a chain whose sole
// binding holds every execute_sql call for a human, deferring to the concrete
// HITL ask resolver over store with the progress bridge as its pending-emit
// collaborator. timeout bounds the resolver's in-turn block; poll is its store
// poll interval.
func buildAskChainForExecuteSQL(t *testing.T, store shuttle.HumanRequestStore, timeout, poll time.Duration) *shuttle.Chain {
	t.Helper()
	cfg := shuttle.HooksConfig{Bindings: []shuttle.HookBinding{
		{Kind: "ask", Scope: admExecTool},
	}}
	chain, err := shuttle.BuildChainFromConfig(cfg, shuttle.ChainDeps{
		Ask: shuttle.NewHITLAskResolver(store, timeout, poll, NewProgressNotifier()),
	})
	if err != nil {
		t.Fatalf("BuildChainFromConfig: %v", err)
	}
	return chain
}

// resolvePendingInBackground starts an actor that resolves the first pending
// request it sees with the given status and response, mirroring what the human
// review interface does. It runs off the test goroutine because the hold blocks
// the turn synchronously; the model never self-resolves.
func resolvePendingInBackground(store *shuttle.InMemoryHumanRequestStore, status, response string) {
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if pending, err := store.ListPending(ctx); err == nil && len(pending) > 0 {
				_ = store.RespondToRequest(ctx, pending[0].ID, status, response, "reviewer@example.com", nil)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// turnExecutionFor returns the first ToolExecution the loop recorded for
// toolName, or nil if the turn ran no such call.
func turnExecutionFor(resp *Response, toolName string) *ToolExecution {
	for i := range resp.ToolExecutions {
		if resp.ToolExecutions[i].ToolName == toolName {
			return &resp.ToolExecutions[i]
		}
	}
	return nil
}

// TestPendingEmit_AgentTurn_AskHold_SurfacesApprovalOnRunStream drives one
// scripted turn whose execute_sql call an "ask" binding holds, and reads the
// hold off the ProgressCallback the turn was started with. The resolver raises
// the request inside executor.admit(), which installs no callback of its own, so
// the recorded event shows the run's callback reached it through ctx. The event
// carries the four fields cloud's relay maps — RequestID, Kind "approval",
// Summary, ExpiresAt — mirroring the stored row; on approve the executor decision
// admits, so the held body runs and the loop records a successful call.
func TestPendingEmit_AgentTurn_AskHold_SurfacesApprovalOnRunStream(t *testing.T) {
	const sessionID = "hitl-turn-ask-hold"

	store := shuttle.NewInMemoryHumanRequestStore()
	chain := buildAskChainForExecuteSQL(t, store, 2*time.Second, 10*time.Millisecond)

	mockLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{execToolCall("call_grant", admGrantReadSQL)}},
		{content: "granted read access"},
	}}
	ag, exec := startAdmissionAgent(t, mockLLM, chain)

	rec := &turnProgressRecorder{}
	resolvePendingInBackground(store, "approved", "")

	resp, err := ag.ChatWithProgress(context.Background(), sessionID, "grant read access to orders", rec.callback())
	require.NoError(t, err)

	events := rec.idBearingHITLEvents()
	require.Len(t, events, 1, "the held call surfaces exactly one pending card on the run's stream")
	ev := events[0]

	hr, err := store.Get(context.Background(), ev.HITLRequest.RequestID)
	require.NoError(t, err, "the emitted request id addresses the stored hold")
	require.Equal(t, "approval", ev.HITLRequest.Kind)
	require.Equal(t, hr.Summary, ev.HITLRequest.Summary)
	require.NotEmpty(t, ev.HITLRequest.Summary, "the card names the held call")
	require.False(t, ev.HITLRequest.ExpiresAt.IsZero(), "the card carries the hold's expiry")
	require.Equal(t, hr.ExpiresAt, ev.HITLRequest.ExpiresAt)

	// The approval reaches the loop as an admit: the held body ran and the turn
	// recorded a successful execute_sql.
	te := execExecutionFor(resp, admGrantReadSQL)
	require.NotNil(t, te, "the turn recorded the held execute_sql call")
	require.NotNil(t, te.Result)
	require.True(t, te.Result.Success, "an approved hold admits the call")
	require.True(t, exec.ran(admGrantReadSQL), "an approved hold runs the held statement")
}

// TestPendingEmit_AgentTurn_ContactHumanHold_SurfacesQuestionOnRunStream drives
// one scripted turn whose contact_human call blocks on a human answer, and reads
// the hold off the ProgressCallback the turn was started with. The tool stores
// the request and notifies through the same bridge, so the id-bearing event
// carries RequestID, Kind "question", Summary = the question text, and ExpiresAt
// — mirroring the stored row — distinct from the loop's id-less pre-creation
// ping. The typed answer returns into the loop byte-for-byte.
func TestPendingEmit_AgentTurn_ContactHumanHold_SurfacesQuestionOnRunStream(t *testing.T) {
	const sessionID = "hitl-turn-contact-human-hold"
	const question = "Which database should I query?"
	const answer = "Use the staging replica, not prod."

	store := shuttle.NewInMemoryHumanRequestStore()

	mockLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{
			ID:    "call_ask_human",
			Name:  "contact_human",
			Input: map[string]interface{}{"question": question},
		}}},
		{content: "querying the staging replica"},
	}}
	ag, _ := startAdmissionAgent(t, mockLLM, nil)
	ag.RegisterTool(shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
		Store:        store,
		Notifier:     NewProgressNotifier(),
		Timeout:      2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	}))

	rec := &turnProgressRecorder{}
	resolvePendingInBackground(store, "responded", answer)

	resp, err := ag.ChatWithProgress(context.Background(), sessionID, "which database holds the orders?", rec.callback())
	require.NoError(t, err)

	events := rec.idBearingHITLEvents()
	require.Len(t, events, 1, "the question surfaces exactly one pending card on the run's stream")
	ev := events[0]

	hr, err := store.Get(context.Background(), ev.HITLRequest.RequestID)
	require.NoError(t, err, "the emitted request id addresses the stored hold")
	require.Equal(t, "question", ev.HITLRequest.Kind)
	require.Equal(t, question, ev.HITLRequest.Summary, "the card shows the question the model asked")
	require.Equal(t, hr.Summary, ev.HITLRequest.Summary)
	require.False(t, ev.HITLRequest.ExpiresAt.IsZero(), "the card carries the hold's expiry")
	require.Equal(t, hr.ExpiresAt, ev.HITLRequest.ExpiresAt)

	// The answer reaches the loop verbatim as the tool's result.
	te := turnExecutionFor(resp, "contact_human")
	require.NotNil(t, te, "the turn recorded the contact_human call")
	require.NotNil(t, te.Result)
	require.True(t, te.Result.Success)
	data, ok := te.Result.Data.(map[string]interface{})
	require.True(t, ok, "contact_human returns its answer in a map Data payload")
	require.Equal(t, answer, data["response"], "the answer reaches the loop byte-for-byte")
}
