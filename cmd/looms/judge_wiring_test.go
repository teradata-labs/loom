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
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// fakeJudgeLLM is the minimal types.LLMProvider for identity assertions.
type fakeJudgeLLM struct{ name string }

func (f *fakeJudgeLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return nil, nil
}
func (f *fakeJudgeLLM) Name() string  { return f.name }
func (f *fakeJudgeLLM) Model() string { return "fake" }

// TestResolveJudgeFallback is the regression guard for judge wiring on
// single-provider servers: the judge's fallback LLM must resolve without any
// provider pool, and a misconfigured active_provider must fall back to the
// server default instead of handing the judge a nil provider.
func TestResolveJudgeFallback(t *testing.T) {
	def := &fakeJudgeLLM{name: "default"}
	pooled := &fakeJudgeLLM{name: "pooled"}

	t.Run("no pool at all → server default", func(t *testing.T) {
		assert.Same(t, agent.LLMProvider(def), resolveJudgeFallback(nil, "anthropic", def))
	})
	t.Run("pool hit → active pool provider", func(t *testing.T) {
		pool := map[string]agent.LLMProvider{"anthropic": pooled}
		assert.Same(t, agent.LLMProvider(pooled), resolveJudgeFallback(pool, "anthropic", def))
	})
	t.Run("misspelled active_provider → server default, never nil", func(t *testing.T) {
		pool := map[string]agent.LLMProvider{"anthropic": pooled}
		got := resolveJudgeFallback(pool, "antropic", def)
		assert.Same(t, agent.LLMProvider(def), got)
		assert.NotNil(t, got)
	})
	t.Run("pool entry explicitly nil → server default", func(t *testing.T) {
		pool := map[string]agent.LLMProvider{"anthropic": nil}
		assert.Same(t, agent.LLMProvider(def), resolveJudgeFallback(pool, "anthropic", def))
	})
}
