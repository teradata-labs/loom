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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// thinkingProvider is a provider that applies the two rules Anthropic applies
// to replayed thinking blocks, so a test through the real loop sees what the
// provider would refuse:
//
//   - Binding (every form): a thinking block is valid only after the messages
//     it was written after. A request that replays a block on a different
//     past is refused with a 400.
//   - Leading block (budgetForm only, the enabled+budget_tokens form of the
//     older models): with thinking on, the newest assistant message that
//     calls tools must carry its thinking block. A request that sends such a
//     message without one is refused with a 400, unless the request is sent
//     with thinking off (llm.ContextSuppressThinking).
//
// It works in steps, each one reasoning and calling a tool, and answers a
// fold ask with a summary.
type thinkingProvider struct {
	mu         sync.Mutex
	budgetForm bool
	// sendsNoThinking makes the fake report the "none" form: an adaptive
	// model name on a client whose request carries no thinking field.
	sendsNoThinking bool
	steps           int

	taken int
	folds int
	// boundTo is, per block signature, the fingerprint of the messages the
	// block was written after.
	boundTo map[string]string
	// thinkingOn and thinkingOff count the step requests sent with thinking on
	// and with thinking suppressed.
	thinkingOn, thinkingOff int
	// offBeforeFold counts suppressed requests sent before the first fold.
	offBeforeFold int
}

func (p *thinkingProvider) Name() string { return "anthropic" }

// Model names the form the fake answers in, as the provider clients decide it
// (llm.IsAdaptiveThinkingModel). Neither name is in the catalog, so the
// window figures stay the configured ones.
func (p *thinkingProvider) Model() string {
	if p.budgetForm {
		return "thinking-model"
	}
	return "thinking-model-opus-5"
}

// pastFingerprint is what a thinking block is bound to: every message before
// it, by role, text, tool calls and tool result id.
func pastFingerprint(msgs []Message) string {
	h := sha256.New()
	for i := range msgs {
		m := &msgs[i]
		fmt.Fprintf(h, "%d:%s|%d:%s|%s|", len(m.Role), m.Role, len(m.Content), m.Content, m.ToolUseID)
		for _, c := range m.ToolCalls {
			fmt.Fprintf(h, "%s:%s|", c.ID, c.Name)
		}
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ThinkingOffWhenSuppressed reports what the real clients report
// (types.ThinkingSuppressionReporter): only the enabled+budget form sends a
// marked request with thinking off.
func (p *thinkingProvider) ThinkingOffWhenSuppressed() bool { return p.budgetForm }

// ThinkingForm reports the thinking field the fake sends with thinking on
// (types.ThinkingSuppressionReporter).
func (p *thinkingProvider) ThinkingForm() string {
	if p.sendsNoThinking {
		return llmtypes.ThinkingFormNone
	}
	if p.budgetForm {
		return llmtypes.ThinkingFormBudget
	}
	return llmtypes.ThinkingFormAdaptive
}

// geminiProvider is the fake under another provider's name: a client that
// never reads the suppression mark, as Gemini does not, so it reports
// nothing (no ThinkingSuppressionReporter).
type geminiProvider struct{ fake *thinkingProvider }

func (g geminiProvider) Name() string  { return "gemini" }
func (g geminiProvider) Model() string { return "gemini-2.5-pro" }
func (g geminiProvider) Chat(ctx context.Context, msgs []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	return g.fake.Chat(ctx, msgs, tools)
}

func (p *thinkingProvider) Chat(ctx context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	usage := llmtypes.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	// As the real clients do, only the enabled+budget form turns thinking off
	// on request; the adaptive form keeps it on.
	suppressed := p.budgetForm && llm.ThinkingSuppressed(ctx)

	// Binding: each replayed block must follow the past it was written after.
	for i := range msgs {
		for _, b := range msgs[i].ThinkingBlocks {
			if p.boundTo[b.Signature] != pastFingerprint(msgs[:i]) {
				return nil, fmt.Errorf("API error (status 400): messages.%d.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation", i)
			}
		}
	}
	// Leading block: the newest assistant message that calls tools.
	if p.budgetForm && !suppressed {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role != "assistant" || len(msgs[i].ToolCalls) == 0 {
				continue
			}
			if len(msgs[i].ThinkingBlocks) == 0 {
				return nil, fmt.Errorf("API error (status 400): messages.%d.content.0.type: Expected `thinking` or `redacted_thinking`, but found `tool_use`. When `thinking` is enabled, a final `assistant` message must start with a thinking block", i)
			}
			break
		}
	}

	if strings.Contains(msgs[len(msgs)-1].Content, "Context maintenance: summarise the work so far") {
		p.folds++
		return &LLMResponse{Content: foldSummaryJSON, StopReason: "end_turn", Usage: usage}, nil
	}
	if suppressed {
		p.thinkingOff++
		if p.folds == 0 {
			p.offBeforeFold++
		}
	} else {
		p.thinkingOn++
	}
	if p.taken >= p.steps {
		return &LLMResponse{Content: "final answer", StopReason: "end_turn", Usage: usage}, nil
	}
	p.taken++
	resp := &LLMResponse{
		Content:    strings.Repeat("working the step through carefully. ", 120),
		StopReason: "tool_use",
		Usage:      usage,
		ToolCalls:  []ToolCall{{ID: fmt.Sprintf("s%d", p.taken), Name: "step", Input: map[string]interface{}{}}},
	}
	if !suppressed {
		sig := fmt.Sprintf("sig-%d", p.taken)
		if p.boundTo == nil {
			p.boundTo = make(map[string]string)
		}
		p.boundTo[sig] = pastFingerprint(msgs)
		resp.ThinkingBlocks = []ThinkingBlock{{Type: "thinking", Thinking: "why this step", Signature: sig}}
	}
	return resp, nil
}

// A long one-turn job folds mid-turn. The fold keeps the pending step on a
// past that is now a summary, so it drops that step's thinking block. The
// sends after the fold must be ones the provider accepts, on both thinking
// forms: no block replayed on a changed past, and, on the enabled+budget form,
// no tool-calling message sent without its block while thinking is on.
func TestThinkingBinding_ATurnThatFoldsIsAcceptedByTheProvider(t *testing.T) {
	tests := []struct {
		name            string
		budgetForm      bool
		sendsNoThinking bool
		gemini          bool
		wantForm        string
	}{
		{name: "adaptive form: the binding is checked", budgetForm: false, wantForm: "adaptive"},
		{name: "enabled+budget form: a tool-calling message needs its block", budgetForm: true, wantForm: "budget"},
		{name: "a provider that never reads the mark", gemini: true, wantForm: "unknown"},
		{name: "an adaptive model name whose client sends no thinking", sendsNoThinking: true, wantForm: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &thinkingProvider{budgetForm: tt.budgetForm, sendsNoThinking: tt.sendsNoThinking, steps: 24}
			var provider LLMProvider = model
			if tt.gemini {
				provider = geminiProvider{fake: model}
			}
			store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
			require.NoError(t, err, "the session store must open")
			t.Cleanup(func() { _ = store.Close() })
			cfg := DefaultConfig()
			cfg.PatternConfig = DefaultPatternConfig()
			cfg.PatternConfig.UseLLMClassifier = false
			cfg.MaxContextTokens = 12000
			cfg.ReservedOutputTokens = 2000
			cfg.MaxIterations = 100
			cfg.MaxTurns = 200
			cfg.MaxToolExecutions = 100
			tracer := observability.NewMockTracer()
			ag := NewAgent(&mockBackend{}, provider, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)), WithTracer(tracer))
			ag.RegisterTool(&shuttle.MockTool{
				MockName: "step",
				MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
					return &shuttle.Result{Success: true, Data: "ok"}, nil
				},
			})

			resp, err := ag.Chat(context.Background(), "s-thinking-fold", "do the long job")
			require.NoError(t, err, "the provider refused a send after the fold")
			assert.Equal(t, "final answer", resp.Content, "the turn must reach its answer")

			model.mu.Lock()
			defer model.mu.Unlock()
			require.Positive(t, model.folds, "the turn never folded; the test does not reach the fold")
			assert.Positive(t, model.thinkingOn, "the turn must think before its first fold")
			assert.Zero(t, model.offBeforeFold, "thinking must stay on until a fold drops a block")
			if tt.budgetForm {
				assert.Positive(t, model.thinkingOff, "after the fold the tool-calling message has no block, so thinking must be off for the rest of the turn")
			}

			// Hawk sees why thinking stopped: each send with thinking off
			// records it on the loop span, and only after a fold.
			loops := tracer.GetSpansByName("agent.conversation_loop")
			require.Len(t, loops, 1, "the turn did not record its loop span")
			folded, suppressed := false, 0
			for _, e := range loops[0].Events {
				switch e.Name {
				case "fold.applied":
					folded = true
				case "llm.thinking_suppressed":
					assert.True(t, folded, "thinking was marked off before any fold")
					assert.Equal(t, "fold", e.Attributes["reason"], "the event does not say why thinking is off")
					assert.Equal(t, tt.wantForm, e.Attributes["form"], "the event does not say which thinking form the client sends")
					assert.Equal(t, tt.budgetForm, e.Attributes["thinking_off"], "the event does not say whether thinking was really off")
					suppressed++
				}
			}
			assert.Positive(t, suppressed, "no span event says that thinking was off after the fold")
		})
	}
}
