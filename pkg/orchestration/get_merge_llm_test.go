// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

func TestGetMergeLLM_ExplicitProviderTakesPriority(t *testing.T) {
	// When an explicit LLM provider is set on the orchestrator, it should be returned
	explicitLLM := newMockLLMProvider("explicit response")

	o := NewOrchestrator(Config{
		LLMProvider: explicitLLM,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Also register an agent with its own LLM
	agentLLM := newMockLLMProvider("agent response")
	ag := createMockAgent(t, "test-agent", agentLLM)
	o.RegisterAgent("test-agent", ag)

	// Act
	result := o.GetMergeLLM()

	// Assert: explicit provider takes priority over agent's LLM
	require.NotNil(t, result)
	assert.Equal(t, explicitLLM, result)
}

func TestGetMergeLLM_FallsBackToAgentOrchestratorLLM(t *testing.T) {
	// When no explicit LLM provider, it should fall back to agents' orchestrator role LLM
	o := NewOrchestrator(Config{
		LLMProvider: nil, // No explicit provider
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Register an agent -- its GetLLMForRole(ORCHESTRATOR) will return its main LLM
	// since no dedicated orchestrator LLM is set
	agentLLM := newMockLLMProvider("agent response")
	ag := createMockAgent(t, "test-agent", agentLLM)
	o.RegisterAgent("test-agent", ag)

	// Act
	result := o.GetMergeLLM()

	// Assert: should fall back to agent's LLM (since orchestrator role returns main LLM as fallback)
	require.NotNil(t, result)
	assert.Equal(t, "mock-model", result.Model())
}

func TestGetMergeLLM_WithDedicatedOrchestratorLLM(t *testing.T) {
	// When an agent has a dedicated orchestrator LLM, it should be used
	o := NewOrchestrator(Config{
		LLMProvider: nil, // No explicit provider
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Create agent with a dedicated orchestrator LLM
	agentLLM := newMockLLMProvider("agent response")
	orchestratorLLM := newMockLLMProvider("orchestrator response")
	ag := createMockAgent(t, "test-agent", agentLLM)
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR, orchestratorLLM)
	o.RegisterAgent("test-agent", ag)

	// Act
	result := o.GetMergeLLM()

	// Assert: should use the dedicated orchestrator LLM
	require.NotNil(t, result)
	// The orchestrator LLM is the same mock instance we set
	assert.Equal(t, orchestratorLLM, result)
}

func TestGetMergeLLM_NoProviderNoAgents_ReturnsNil(t *testing.T) {
	// When no explicit provider and no agents, should return nil
	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Act
	result := o.GetMergeLLM()

	// Assert
	assert.Nil(t, result)
}

func TestGetMergeLLM_ConcurrentAccess(t *testing.T) {
	// Verify GetMergeLLM is safe under concurrent access
	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	agentLLM := newMockLLMProvider("agent response")
	ag := createMockAgent(t, "test-agent", agentLLM)
	o.RegisterAgent("test-agent", ag)

	// Run concurrent reads -- race detector will catch any issues
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			result := o.GetMergeLLM()
			assert.NotNil(t, result)
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestGetMergeLLM_MultipleAgents verifies that GetMergeLLM can find an LLM from any registered agent.
func TestGetMergeLLM_MultipleAgents(t *testing.T) {
	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Register multiple agents
	for i := 0; i < 3; i++ {
		llm := newMockLLMProvider("response")
		ag := createMockAgent(t, "agent-"+string(rune('a'+i)), llm)
		o.RegisterAgent("agent-"+string(rune('a'+i)), ag)
	}

	// Act
	result := o.GetMergeLLM()

	// Assert: should find at least one agent's LLM
	require.NotNil(t, result)
}

// TestGetMergeLLM_AgentWithOrchestratorRole ensures that when one agent has
// a dedicated orchestrator LLM and another doesn't, the dedicated one is preferred
// when it happens to be found first (map iteration order is non-deterministic in Go,
// so this test verifies it works in the agent-with-dedicated-llm-only case).
func TestGetMergeLLM_AgentWithOrchestratorRole(t *testing.T) {
	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	})

	// Create an agent with a dedicated orchestrator LLM
	agentLLM := newMockLLMProvider("main response")
	orchLLM := newMockLLMProvider("orchestrator response")

	ag := agent.NewAgent(
		&mockBackend{},
		agentLLM,
		agent.WithName("orch-agent"),
		agent.WithOrchestratorLLM(orchLLM),
	)
	o.RegisterAgent("orch-agent", ag)

	// Act
	result := o.GetMergeLLM()

	// Assert: should return the orchestrator LLM, not the main agent LLM
	require.NotNil(t, result)
	assert.Equal(t, orchLLM, result)
}

// TestGetMergeLLM_MultipleAgents_WarnsNonDeterministic verifies that when multiple
// agents have orchestrator LLMs and no explicit Config.LLMProvider is set, a warning
// is logged indicating the selection is non-deterministic.
func TestGetMergeLLM_MultipleAgents_WarnsNonDeterministic(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      logger,
		Tracer:      observability.NewNoOpTracer(),
	})

	// Register multiple agents, each with an orchestrator LLM
	for _, name := range []string{"alpha", "beta", "gamma"} {
		llm := newMockLLMProvider(name + " response")
		ag := createMockAgent(t, name, llm)
		o.RegisterAgent(name, ag)
	}

	// Act
	result := o.GetMergeLLM()

	// Assert: an LLM was returned
	require.NotNil(t, result)

	// Assert: a warn-level log was emitted about non-deterministic selection
	warnEntries := logs.FilterMessage("Multiple agents have orchestrator LLMs; selection is non-deterministic. " +
		"Set orchestrator-level LLMProvider in Config for deterministic behavior")
	require.Equal(t, 1, warnEntries.Len(), "expected exactly one warning about non-deterministic selection")

	entry := warnEntries.All()[0]
	assert.Equal(t, zapcore.WarnLevel, entry.Level)

	// Verify the structured fields contain the expected keys
	fieldMap := make(map[string]interface{})
	for _, f := range entry.Context {
		switch f.Type {
		case zapcore.StringType:
			fieldMap[f.Key] = f.String
		case zapcore.ArrayMarshalerType:
			fieldMap[f.Key] = f.Interface
		}
	}
	assert.Contains(t, fieldMap, "selected_agent", "log should include selected_agent field")
	assert.Contains(t, fieldMap, "candidate_agents", "log should include candidate_agents field")
}

// TestGetMergeLLM_SingleAgent_NoWarning verifies that a single agent fallback
// produces a debug log (not a warning).
func TestGetMergeLLM_SingleAgent_NoWarning(t *testing.T) {
	// Use WarnLevel observer -- debug messages will NOT be captured,
	// so if only debug is emitted, warn count stays at 0.
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	o := NewOrchestrator(Config{
		LLMProvider: nil,
		Logger:      logger,
		Tracer:      observability.NewNoOpTracer(),
	})

	llm := newMockLLMProvider("only agent response")
	ag := createMockAgent(t, "solo-agent", llm)
	o.RegisterAgent("solo-agent", ag)

	// Act
	result := o.GetMergeLLM()

	// Assert: LLM returned
	require.NotNil(t, result)

	// Assert: no warnings were logged (single agent = deterministic, so only debug)
	assert.Equal(t, 0, logs.Len(), "no warnings should be logged when only one agent has an orchestrator LLM")
}

// TestGetMergeLLM_ExplicitProvider_NoWarning verifies that when an explicit
// LLMProvider is set, no warning is logged even if multiple agents have orchestrator LLMs.
func TestGetMergeLLM_ExplicitProvider_NoWarning(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	explicitLLM := newMockLLMProvider("explicit")

	o := NewOrchestrator(Config{
		LLMProvider: explicitLLM,
		Logger:      logger,
		Tracer:      observability.NewNoOpTracer(),
	})

	// Register multiple agents
	for _, name := range []string{"alpha", "beta"} {
		llm := newMockLLMProvider(name + " response")
		ag := createMockAgent(t, name, llm)
		o.RegisterAgent(name, ag)
	}

	// Act
	result := o.GetMergeLLM()

	// Assert: explicit provider returned, no warnings
	require.NotNil(t, result)
	assert.Equal(t, explicitLLM, result)
	assert.Equal(t, 0, logs.Len(), "no warnings should be logged when explicit LLMProvider is set")
}
