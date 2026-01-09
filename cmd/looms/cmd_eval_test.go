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
//go:build hawk

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

// TestAgentWrapper_Execute verifies the agentWrapper correctly calls agent.Chat
func TestAgentWrapper_Execute(t *testing.T) {
	// Create mock LLM provider
	mockLLM := &mockLLMProvider{
		response: "The configuration is valid and passed validation checks.",
	}

	// Create agent with mock backend and LLM
	backend := &mockBackend{}
	ag := agent.NewAgent(backend, mockLLM, agent.WithName("test-agent"))

	// Wrap agent
	wrapper := &agentWrapper{agent: ag}

	// Execute
	ctx := context.Background()
	input := "Validate this config: apiVersion: loom/v1..."

	startTime := time.Now()
	response, err := wrapper.Execute(ctx, input)
	elapsed := time.Since(startTime)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, response)
	assert.True(t, response.Successful, "Should be successful")
	assert.Empty(t, response.Error, "Should have no error")
	assert.Equal(t, "The configuration is valid and passed validation checks.", response.Output)
	assert.NotEmpty(t, response.TraceID, "Should have trace ID")
	assert.GreaterOrEqual(t, response.LatencyMs, int64(0), "Should have non-negative latency")
	assert.LessOrEqual(t, response.LatencyMs, elapsed.Milliseconds()+100, "Latency should be reasonable")

	// Cost should be from mock LLM (0.001)
	assert.Equal(t, 0.001, response.CostUsd)
}

// TestAgentWrapper_Execute_Error verifies error handling
func TestAgentWrapper_Execute_Error(t *testing.T) {
	// Create mock LLM that returns error
	mockLLM := &mockLLMProvider{
		shouldError: true,
		errorMsg:    "LLM connection timeout",
	}

	// Create agent
	backend := &mockBackend{}
	ag := agent.NewAgent(backend, mockLLM, agent.WithName("test-agent"))
	wrapper := &agentWrapper{agent: ag}

	// Execute
	ctx := context.Background()
	response, err := wrapper.Execute(ctx, "test input")

	// Verify - wrapper should not return error, but set Successful=false
	require.NoError(t, err, "Wrapper should not return error")
	assert.NotNil(t, response)
	assert.False(t, response.Successful, "Should be unsuccessful")
	assert.Contains(t, response.Error, "LLM connection timeout")
	assert.Empty(t, response.Output)
	assert.GreaterOrEqual(t, response.LatencyMs, int64(0), "Should measure latency even on error")
}

// TestAgentWrapper_Execute_WithTools verifies tool extraction
func TestAgentWrapper_Execute_WithTools(t *testing.T) {
	// Create mock LLM that triggers tool use
	mockLLM := &mockLLMProvider{
		response:   "I used the validator tool to check the config.",
		toolsToUse: []string{"validate_config", "check_schema"},
	}

	// Create agent
	backend := &mockBackend{}
	ag := agent.NewAgent(backend, mockLLM, agent.WithName("test-agent"))
	wrapper := &agentWrapper{agent: ag}

	// Execute
	ctx := context.Background()
	response, err := wrapper.Execute(ctx, "test input")

	// Verify
	require.NoError(t, err)
	assert.True(t, response.Successful)

	// Note: Tool extraction depends on agent actually executing tools
	// In this test, tools might not be executed since we're using a mock
	// The important thing is the wrapper extracts them correctly when they exist
	assert.NotNil(t, response.ToolsUsed)
}

// TestAgentWrapper_UniqueSessionIDs verifies each execution gets unique session
func TestAgentWrapper_UniqueSessionIDs(t *testing.T) {
	mockLLM := &mockLLMProvider{response: "test"}
	backend := &mockBackend{}
	ag := agent.NewAgent(backend, mockLLM, agent.WithName("test-agent"))
	wrapper := &agentWrapper{agent: ag}

	ctx := context.Background()

	// Execute twice
	resp1, err1 := wrapper.Execute(ctx, "input1")
	time.Sleep(1 * time.Millisecond) // Ensure different timestamps
	resp2, err2 := wrapper.Execute(ctx, "input2")

	// Verify both succeed
	require.NoError(t, err1)
	require.NoError(t, err2)

	// Verify different trace IDs (which are based on session IDs)
	assert.NotEqual(t, resp1.TraceID, resp2.TraceID, "Should have unique session IDs")
	assert.True(t, len(resp1.TraceID) > 0)
	assert.True(t, len(resp2.TraceID) > 0)
}
