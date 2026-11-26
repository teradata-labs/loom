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
package server

// A parked approval card is the only thing a gRPC client (the TUI, any
// StreamWeave consumer) sees of a parked turn before it answers. The card must
// name every asking hook's reason, because that is what the approval's grants
// cover. These tests park a turn behind two asking hooks on one call and read
// the card off both StreamWeave implementations.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	parkedCardReasonProgram     = "review-approve-off-list-program"
	parkedCardReasonDestination = "review-approve-external-destination"
	parkedCardJoinedReason      = parkedCardReasonProgram + "; " + parkedCardReasonDestination
	parkedCardCallID            = "c-export"
)

// parkedCardAskHook asks on one tool with a fixed reason and grant. It is
// deterministic for the same call, as the admission chain requires.
type parkedCardAskHook struct {
	tool   string
	reason string
	grant  string
}

func (h parkedCardAskHook) Matches(r shuttle.AdmissionRequest) bool { return r.ToolName == h.tool }
func (h parkedCardAskHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Ask, Reason: h.reason, Grant: h.grant}
}

// exportOnceLLM calls export_csv on its first turn and answers in text after.
type exportOnceLLM struct {
	mu     sync.Mutex
	called bool
}

func (m *exportOnceLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	first := !m.called
	m.called = true
	m.mu.Unlock()
	if first {
		return &llmtypes.LLMResponse{
			ToolCalls: []llmtypes.ToolCall{{
				ID:    parkedCardCallID,
				Name:  "export_csv",
				Input: map[string]interface{}{"v": "w"},
			}},
			Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	}
	return &llmtypes.LLMResponse{Content: "exported", Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (m *exportOnceLLM) Name() string  { return "export-once" }
func (m *exportOnceLLM) Model() string { return "export-once-v1" }

// exportCSVTool is the held tool. Its body must not run before approval.
type exportCSVTool struct {
	mu   sync.Mutex
	runs int
}

func (t *exportCSVTool) Name() string        { return "export_csv" }
func (t *exportCSVTool) Description() string { return "exports a CSV" }
func (t *exportCSVTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *exportCSVTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	t.mu.Lock()
	t.runs++
	t.mu.Unlock()
	return &shuttle.Result{Success: true, Data: "exported"}, nil
}
func (t *exportCSVTool) Backend() string { return "" }

func (t *exportCSVTool) runCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runs
}

// newParkingAgent builds an agent that parks export_csv behind the review
// repro's two asking hooks and emits the parked card on the progress stream.
// Park needs a persistent session store (the parked turn's assistant message
// must be durable), so the agent gets a SQLite one in the test's temp dir.
func newParkingAgent(t *testing.T) (*agent.Agent, *exportCSVTool, shuttle.HumanRequestStore) {
	t.Helper()
	sessions, err := agent.NewSessionStore(filepath.Join(t.TempDir(), "park.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	store := shuttle.NewInMemoryHumanRequestStore()
	chain := shuttle.NewChain([]shuttle.Hook{
		parkedCardAskHook{tool: "export_csv", reason: parkedCardReasonProgram, grant: "program-exception"},
		parkedCardAskHook{tool: "export_csv", reason: parkedCardReasonDestination, grant: "destination-exception"},
	}, nil, nil)
	ag := agent.NewAgent(&mockBackend{}, &exportOnceLLM{},
		agent.WithMemory(agent.NewMemoryWithStore(sessions)),
		agent.WithHITLPark(store, 0, agent.NewProgressNotifier()),
		agent.WithAdmissionHooks(chain),
	)
	tool := &exportCSVTool{}
	ag.RegisterTool(tool)
	return ag, tool, store
}

// TestStreamWeave_ParkedApprovalCardCarriesJoinedReasons drives a real
// StreamWeave on both servers into a park and reads the card a gRPC client
// receives: params_json must hold the held call's descriptor with both
// hooks' reasons, and kind/summary must describe the approval.
func TestStreamWeave_ParkedApprovalCardCarriesJoinedReasons(t *testing.T) {
	tests := []struct {
		name   string
		stream func(ag *agent.Agent, s *mockStreamWeaveServer) error
	}{
		{
			name: "single-agent server",
			stream: func(ag *agent.Agent, s *mockStreamWeaveServer) error {
				return NewServer(ag, nil).StreamWeave(&loomv1.WeaveRequest{Query: "export it", SessionId: "s-parked-card"}, s)
			},
		},
		{
			name: "multi-agent server",
			stream: func(ag *agent.Agent, s *mockStreamWeaveServer) error {
				srv := NewMultiAgentServer(map[string]*agent.Agent{"agent-1": ag}, nil)
				return srv.StreamWeave(&loomv1.WeaveRequest{Query: "export it", SessionId: "s-parked-card"}, s)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag, tool, store := newParkingAgent(t)
			stream := &mockStreamWeaveServer{ctx: context.Background()}

			// The turn parks; how each server reports a parked turn to the
			// caller is not what this test pins, only the card it sent first.
			_ = tt.stream(ag, stream)

			stream.mu.Lock()
			var cards []*loomv1.HITLRequestInfo
			for _, msg := range stream.messages {
				if msg.GetHitlRequest().GetRequestId() != "" {
					cards = append(cards, msg.GetHitlRequest())
				}
			}
			stream.mu.Unlock()
			require.Len(t, cards, 1, "the park surfaces exactly one card on the gRPC stream")
			card := cards[0]

			// The card is the stored parked row, not some other request.
			hr, err := store.Get(context.Background(), card.RequestId)
			require.NoError(t, err)
			require.Equal(t, "parked", hr.RequestType)

			assert.Equal(t, "parked", card.RequestType)
			assert.Equal(t, "approval", card.Kind)
			assert.Equal(t, hr.Summary, card.Summary)
			assert.NotEmpty(t, card.Summary)
			assert.False(t, card.ParamsTruncated)

			require.NotEmpty(t, card.ParamsJson,
				"the parked card must carry the per-item descriptors over gRPC")
			var params map[string]map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(card.ParamsJson), &params))
			desc, ok := params[parkedCardCallID]
			require.True(t, ok, "params_json has no descriptor for the held call: %s", card.ParamsJson)
			assert.Equal(t, parkedCardJoinedReason, desc["reason"],
				"the parked card a gRPC client receives must name every asking hook's reason")

			assert.Zero(t, tool.runCount(), "the held call must not run before approval")
		})
	}
}
