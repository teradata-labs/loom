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

// Park no-op proofs. The park pre-scan sits on the hottest path in the product
// (every tool batch of every turn), and the dispatchOneCall extraction moves
// the batch loop body verbatim. These tests pin both claims:
//
//   - golden: an ungoverned two-tool turn produces exactly this message
//     sequence (roles, tool pairing, contents). Written against the
//     pre-extraction loop; the extraction must keep it green unchanged.
//   - differential: enabling park on an ungoverned agent changes nothing —
//     the message sequences of a park-enabled and a park-disabled run of the
//     same script are identical.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// parkEchoTool is a trivial ungoverned tool: echoes its "v" param.
type parkEchoTool struct{ name string }

func (t *parkEchoTool) Name() string        { return t.name }
func (t *parkEchoTool) Description() string { return "echoes v" }
func (t *parkEchoTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *parkEchoTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	v, _ := params["v"].(string)
	return &shuttle.Result{Success: true, Data: "echo:" + v}, nil
}
func (t *parkEchoTool) Backend() string { return "" }

// startParkNoopAgent builds an ungoverned agent (no admission chain) with two
// echo tools and a scripted two-call batch followed by a final text turn.
func startParkNoopAgent(t *testing.T, parkEnabled bool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "call-a", Name: "echo_a", Input: map[string]interface{}{"v": "one"}},
			{ID: "call-b", Name: "echo_b", Input: map[string]interface{}{"v": "two"}},
		}},
		{content: "all done"},
	}}

	opts := []Option{WithConfig(cfg)}
	if parkEnabled {
		opts = append(opts, WithHITLPark(shuttle.NewInMemoryHumanRequestStore(), 0, nil))
	}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	ag.RegisterTool(&parkEchoTool{name: "echo_a"})
	ag.RegisterTool(&parkEchoTool{name: "echo_b"})
	return ag
}

// messageTrace renders the session's conversational rows (system rows excluded)
// as one comparable line per row: role, tool-use id, and content.
func messageTrace(t *testing.T, ag *Agent, ctx context.Context, sessionID string) []string {
	t.Helper()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sessionID, ag.config.Name, "")
	var out []string
	for _, m := range sess.GetMessages() {
		if m.Role == "system" {
			continue
		}
		content := m.Content
		// The compiled view prepends a rendered "[<timestamp>] " prefix to
		// user rows (renderLocked temporal grounding) — normalize it away so
		// the trace pins conversational content, not the wall clock.
		if m.Role == "user" && strings.HasPrefix(content, "[") {
			if end := strings.Index(content, "] "); end > 0 {
				content = content[end+2:]
			}
		}
		out = append(out, fmt.Sprintf("%s|%s|%s", m.Role, m.ToolUseID, content))
	}
	return out
}

func TestParkNoop_GoldenUngovernedBatchSequence(t *testing.T) {
	ag := startParkNoopAgent(t, false)
	ctx := context.Background()
	resp, err := ag.Chat(ctx, "park-noop-golden", "run both")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "all done" {
		t.Fatalf("final content = %q, want %q", resp.Content, "all done")
	}
	if len(resp.ToolExecutions) != 2 {
		t.Fatalf("tool executions = %d, want 2", len(resp.ToolExecutions))
	}

	trace := messageTrace(t, ag, ctx, "park-noop-golden")
	want := []string{
		"user||run both",
		"assistant||",
		"tool|call-a|echo:one",
		"tool|call-b|echo:two",
		"assistant||all done",
	}
	if len(trace) != len(want) {
		t.Fatalf("trace length = %d, want %d\ntrace: %#v", len(trace), len(want), trace)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q\nfull: %#v", i, trace[i], want[i], trace)
		}
	}
}

func TestParkNoop_ParkEnabledUngovernedRunIsIdentical(t *testing.T) {
	ctx := context.Background()

	agOff := startParkNoopAgent(t, false)
	respOff, err := agOff.Chat(ctx, "park-noop-off", "run both")
	if err != nil {
		t.Fatalf("Chat (park off): %v", err)
	}
	agOn := startParkNoopAgent(t, true)
	respOn, err := agOn.Chat(ctx, "park-noop-on", "run both")
	if err != nil {
		t.Fatalf("Chat (park on): %v", err)
	}

	if respOff.Content != respOn.Content {
		t.Fatalf("content diverged: off=%q on=%q", respOff.Content, respOn.Content)
	}
	if len(respOff.ToolExecutions) != len(respOn.ToolExecutions) {
		t.Fatalf("tool executions diverged: off=%d on=%d",
			len(respOff.ToolExecutions), len(respOn.ToolExecutions))
	}

	traceOff := messageTrace(t, agOff, ctx, "park-noop-off")
	traceOn := messageTrace(t, agOn, ctx, "park-noop-on")
	if len(traceOff) != len(traceOn) {
		t.Fatalf("trace lengths diverged: off=%d on=%d\noff: %#v\non: %#v",
			len(traceOff), len(traceOn), traceOff, traceOn)
	}
	for i := range traceOff {
		if traceOff[i] != traceOn[i] {
			t.Fatalf("trace[%d] diverged:\noff: %q\non:  %q", i, traceOff[i], traceOn[i])
		}
	}
}
