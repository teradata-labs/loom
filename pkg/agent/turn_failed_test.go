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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// failsAfterLLM answers its first call with a tool call that costs something,
// then fails every call after it.
type failsAfterLLM struct {
	mu    sync.Mutex
	calls int
}

var errProviderDown = errors.New("provider down")

func (m *failsAfterLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls > 1 {
		return nil, errProviderDown
	}
	return &llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}}},
		Usage:     llmtypes.Usage{InputTokens: 120, OutputTokens: 30, TotalTokens: 150, CostUSD: 0.004},
	}, nil
}

func (m *failsAfterLLM) Name() string  { return "fails-after" }
func (m *failsAfterLLM) Model() string { return "mock-v1" }

func newTurnFailedAgent(llm LLMProvider) *Agent {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&mockCalculatorTool{})
	return ag
}

// A turn that fails after a call has spent returns the failure together with
// what the turn spent, and the original failure is still reachable.
func TestTurnFailedCarriesWhatTheTurnSpent(t *testing.T) {
	ag := newTurnFailedAgent(&failsAfterLLM{})

	_, err := ag.Chat(context.Background(), "turn-failed", "add one and one")
	if err == nil {
		t.Fatal("the turn succeeded; the provider failed on its second call")
	}
	var tf *TurnFailed
	if !errors.As(err, &tf) {
		t.Fatalf("the failure does not carry the turn's spend: %v", err)
	}
	if tf.Spent.CostUSD <= 0 || tf.Spent.TotalTokens <= 0 {
		t.Fatalf("spent = %+v, want the first call's cost and tokens", tf.Spent)
	}
	if !errors.Is(err, errProviderDown) {
		t.Fatalf("the original failure is not reachable through the wrapper: %v", err)
	}
}

// A turn that fails before anything was spent returns its failure unwrapped.
func TestTurnFailedOnlyWhenSomethingWasSpent(t *testing.T) {
	ag := newTurnFailedAgent(&mockErrorLLM{errorMsg: "refused"})

	_, err := ag.Chat(context.Background(), "turn-failed-free", "hello")
	if err == nil {
		t.Fatal("the turn succeeded; the provider refuses every call")
	}
	var tf *TurnFailed
	if errors.As(err, &tf) {
		t.Fatalf("a turn that spent nothing was wrapped: %+v", tf.Spent)
	}
}

// A turn that lost its lease after a call had cost something still reports
// what it spent: the lease-lost cause leads the chain, context.Canceled is
// kept out of it, and the TurnFailed around the spend survives the wrap.
func TestALostLeaseKeepsWhatTheTurnSpent(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrSessionLeaseLost)
	spent := Usage{CostUSD: 0.5, TotalTokens: 900}
	err := leaseLostError(ctx, fmt.Errorf("conversation loop: %w", &TurnFailed{Err: errors.New("provider: connection reset"), Spent: spent}))

	var failed *TurnFailed
	if !errors.As(err, &failed) {
		t.Fatalf("the spend was dropped with the lease: %v", err)
	}
	if failed.Spent != spent {
		t.Fatalf("spent %+v, want %+v", failed.Spent, spent)
	}
	if !errors.Is(err, ErrSessionLeaseLost) {
		t.Fatalf("the lease-lost cause does not lead the chain: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("context.Canceled outranks the cause: %v", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("the provider's error text was lost: %v", err)
	}
}
