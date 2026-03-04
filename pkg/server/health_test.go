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

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// countingLLM counts how many times Chat() is called.
type countingLLM struct {
	name  string
	model string
	calls atomic.Int64
	err   error // if non-nil, Chat returns this error
}

func (m *countingLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return &llmtypes.LLMResponse{Content: "pong"}, nil
}

func (m *countingLLM) Name() string  { return m.name }
func (m *countingLLM) Model() string { return m.model }

func TestValidateProviders_NoProviders(t *testing.T) {
	err := ValidateProviders(context.Background(), map[string]*agent.Agent{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no LLM providers configured")
}

func TestValidateProviders_AllHealthy(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "test-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)

	err := ValidateProviders(context.Background(), map[string]*agent.Agent{
		"agent1": ag,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), llm.calls.Load(), "should ping exactly once")
}

func TestValidateProviders_DeduplicatesSharedProvider(t *testing.T) {
	// 50 agents all sharing one LLM provider — should result in exactly 1 ping.
	sharedLLM := &countingLLM{name: "bedrock", model: "claude-3"}
	agents := make(map[string]*agent.Agent, 50)
	for i := range 50 {
		ag := agent.NewAgent(&mockBackend{}, sharedLLM)
		agents[string(rune('a'+i))] = ag
	}

	err := ValidateProviders(context.Background(), agents)
	require.NoError(t, err)
	assert.Equal(t, int64(1), sharedLLM.calls.Load(),
		"50 agents sharing one provider should produce exactly 1 ping")
}

func TestValidateProviders_DistinctProvidersAllPinged(t *testing.T) {
	llm1 := &countingLLM{name: "bedrock", model: "claude-3"}
	llm2 := &countingLLM{name: "bedrock", model: "claude-3-haiku"} // different model
	llm3 := &countingLLM{name: "ollama", model: "llama3"}

	agents := map[string]*agent.Agent{
		"a1": agent.NewAgent(&mockBackend{}, llm1),
		"a2": agent.NewAgent(&mockBackend{}, llm2),
		"a3": agent.NewAgent(&mockBackend{}, llm3),
	}

	err := ValidateProviders(context.Background(), agents)
	require.NoError(t, err)
	assert.Equal(t, int64(1), llm1.calls.Load())
	assert.Equal(t, int64(1), llm2.calls.Load())
	assert.Equal(t, int64(1), llm3.calls.Load())
}

func TestValidateProviders_ReportsFailures(t *testing.T) {
	llmOK := &countingLLM{name: "bedrock", model: "claude-3"}
	llmBad := &countingLLM{name: "ollama", model: "llama3", err: errors.New("connection refused")}

	agents := map[string]*agent.Agent{
		"ok":  agent.NewAgent(&mockBackend{}, llmOK),
		"bad": agent.NewAgent(&mockBackend{}, llmBad),
	}

	err := ValidateProviders(context.Background(), agents)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ollama/llama3")
	assert.Contains(t, err.Error(), "connection refused")
	// Healthy provider should still have been checked
	assert.Equal(t, int64(1), llmOK.calls.Load())
}
