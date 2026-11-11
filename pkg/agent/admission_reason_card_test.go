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

// The approval card must name everything the grants cover. When several
// asking hooks match one call, the chain joins their reasons into the Ask's
// Reason and an approval delivers every hook's grant, in hook order. These
// tests read the card off the two standard carriers — the stored parked
// request ResumeChat binds against, and the HITL card NewProgressNotifier puts
// on the run's progress stream — and check both reasons are on it, then that
// approving still hands the tool body both grants in order.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	cardReasonProgram     = "review-approve-off-list-program"
	cardReasonDestination = "review-approve-external-destination"
	cardGrantProgram      = "program-exception"
	cardGrantDestination  = "destination-exception"
	cardJoinedReason      = cardReasonProgram + "; " + cardReasonDestination
)

// reasonGrantAskHook asks on one tool with a fixed reason and grant. It is
// deterministic for the same call, as the chain's contract requires.
type reasonGrantAskHook struct {
	tool   string
	reason string
	grant  string
}

func (h reasonGrantAskHook) Matches(r shuttle.AdmissionRequest) bool { return r.ToolName == h.tool }
func (h reasonGrantAskHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Ask, Reason: h.reason, Grant: h.grant}
}

// twoReasonHooks returns the two asking hooks from the review repro, in order.
func twoReasonHooks(tool string) []shuttle.Hook {
	return []shuttle.Hook{
		reasonGrantAskHook{tool: tool, reason: cardReasonProgram, grant: cardGrantProgram},
		reasonGrantAskHook{tool: tool, reason: cardReasonDestination, grant: cardGrantDestination},
	}
}

// grantRecordingTool records the admission grants each run of its body saw.
type grantRecordingTool struct {
	name string
	mu   sync.Mutex
	runs [][]any
}

func (t *grantRecordingTool) Name() string        { return t.name }
func (t *grantRecordingTool) Description() string { return "records admission grants" }
func (t *grantRecordingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *grantRecordingTool) Execute(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	t.mu.Lock()
	t.runs = append(t.runs, shuttle.AdmissionGrantsFromContext(ctx))
	t.mu.Unlock()
	return &shuttle.Result{Success: true, Data: "exported"}, nil
}
func (t *grantRecordingTool) Backend() string { return "" }

func (t *grantRecordingTool) seen() [][]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]any(nil), t.runs...)
}

// exportCall is the repro's single held call.
func exportCall() llmtypes.ToolCall {
	return llmtypes.ToolCall{ID: "c-export", Name: "export_csv", Input: map[string]interface{}{"v": "w"}}
}

// TestPark_CardCarriesJoinedAskReasons drives Chat → stored parked request →
// ResumeChat. The stored row's descriptor for the held call (what ResumeChat
// reads and what a card renders) must carry both hooks' reasons, the parked
// card emitted on the progress stream must carry them too, and approving must
// deliver both grants to the tool body in hook order.
func TestPark_CardCarriesJoinedAskReasons(t *testing.T) {
	responses := []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{exportCall()}},
		{content: "exported"},
	}
	f := newParkFixture(t, twoReasonHooks("export_csv"), responses)
	tool := &grantRecordingTool{name: "export_csv"}
	f.ag.RegisterTool(tool)
	ctx := context.Background()

	rec := &turnProgressRecorder{}
	_, err := f.ag.ChatWithProgress(ctx, "s-reasons", "export it", rec.callback())
	var parked *TurnParkedError
	require.True(t, errors.As(err, &parked), "expected the turn to park, got %v", err)

	// The row as the store holds it — the same row ResumeChat binds against.
	hr, err := f.store.Get(ctx, parked.RequestID)
	require.NoError(t, err)
	desc, ok := hr.Params["c-export"].(map[string]interface{})
	require.True(t, ok, "stored parked request has no descriptor for the held call: %#v", hr.Params)
	require.Equal(t, cardJoinedReason, desc["reason"],
		"the stored parked descriptor must name every asking hook's reason")

	// The parked card on the progress stream carries the same descriptor.
	events := rec.idBearingHITLEvents()
	require.Len(t, events, 1, "the park surfaces exactly one card")
	evDesc, ok := events[0].HITLRequest.Params["c-export"].(map[string]interface{})
	require.True(t, ok, "emitted parked card has no descriptor for the held call: %#v", events[0].HITLRequest.Params)
	require.Equal(t, cardJoinedReason, evDesc["reason"],
		"the emitted parked card must name every asking hook's reason")

	resp, err := f.ag.ResumeChat(ctx, "s-reasons", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "exported", resp.Content)
	require.Equal(t, [][]any{{cardGrantProgram, cardGrantDestination}}, tool.seen(),
		"approval delivers both grants to the tool body, in hook order")
}

// TestPendingEmit_InTurnCardCarriesJoinedAskReasons drives the default in-turn
// path: executor → HITL ask resolver → NewProgressNotifier. The card the run's
// progress stream receives must carry the stored request's context, including
// the joined reason, and approving must deliver both grants in hook order.
func TestPendingEmit_InTurnCardCarriesJoinedAskReasons(t *testing.T) {
	store := shuttle.NewInMemoryHumanRequestStore()
	chain := shuttle.NewChain(twoReasonHooks("export_csv"), nil,
		shuttle.NewHITLAskResolver(store, 2*time.Second, 10*time.Millisecond, NewProgressNotifier()))

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{exportCall()}},
		{content: "exported"},
	}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithAdmissionHooks(chain))
	tool := &grantRecordingTool{name: "export_csv"}
	ag.RegisterTool(tool)

	rec := &turnProgressRecorder{}
	resolvePendingInBackground(store, "approved", "")
	_, err := ag.ChatWithProgress(context.Background(), "s-inturn-reasons", "export it", rec.callback())
	require.NoError(t, err)

	events := rec.idBearingHITLEvents()
	require.Len(t, events, 1, "the held call surfaces exactly one card")
	card := events[0].HITLRequest

	hr, err := store.Get(context.Background(), card.RequestID)
	require.NoError(t, err)
	require.Equal(t, cardJoinedReason, hr.Context["reason"], "the stored request carries the joined reason")
	require.NotNil(t, card.Context, "the emitted card must carry the stored request's context")
	require.Equal(t, cardJoinedReason, card.Context["reason"],
		"the emitted in-turn card must name every asking hook's reason")
	require.Equal(t, map[string]interface{}{"reason": cardJoinedReason}, card.Context,
		"the card forwards only the ask reason, not stored bookkeeping such as user_id")

	require.Equal(t, [][]any{{cardGrantProgram, cardGrantDestination}}, tool.seen(),
		"approval delivers both grants to the tool body, in hook order")
}

// TestClassifyParkItems_ApprovalCarriesJoinedAskReasons covers the
// ResumeSession re-park path: classifyParkItems builds the approval item from
// the same Preflight, so its descriptor must name every asking hook's reason
// like maybeParkBatch's does.
func TestClassifyParkItems_ApprovalCarriesJoinedAskReasons(t *testing.T) {
	store := shuttle.NewInMemoryHumanRequestStore()
	chain := shuttle.NewChain(twoReasonHooks("export_csv"), nil,
		shuttle.NewHITLAskResolver(store, 2*time.Second, 10*time.Millisecond, NewProgressNotifier()))
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithAdmissionHooks(chain))
	ag.RegisterTool(&grantRecordingTool{name: "export_csv"})

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-classify-reasons", ag.config.Name, "")
	agentCtx := &agentContext{Context: ctx, session: sess, tracer: ag.tracer}
	items := ag.classifyParkItems(agentCtx, []llmtypes.ToolCall{exportCall()}, []int{3})
	require.Len(t, items, 1)
	require.Equal(t, "approval", items[0].kind)
	require.Equal(t, 3, items[0].seq)
	require.Equal(t, cardJoinedReason, items[0].reason)

	params, truncated := buildParkParams(items)
	require.False(t, truncated)
	desc, ok := params["c-export"].(map[string]interface{})
	require.True(t, ok, "no descriptor for the held call: %#v", params)
	require.Equal(t, cardJoinedReason, desc["reason"])
}
