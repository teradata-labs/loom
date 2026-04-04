// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

func TestPipelineRetry_SchemaValidationFailureRetried(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// First call returns invalid JSON, second returns valid JSON matching schema
	agentLLM := newMockLLMProvider(
		"Here is some text without JSON",
		`{"result": "success", "count": 42}`,
	)
	ag := createMockAgent(t, "data-agent", agentLLM)

	// Need a merge LLM for validation (not used for schema validation, but needed for LLM validation)
	orch := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})
	orch.RegisterAgent("data-agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Extract data",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "data-agent",
						PromptTemplate: "Extract structured data from: {{previous}}",
						OutputSchema:   `{"type":"object","required":["result","count"],"properties":{"result":{"type":"string"},"count":{"type":"integer"}}}`,
						RetryPolicy: &loomv1.OutputRetryPolicy{
							MaxRetries: 2,
						},
					},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "pipeline", result.PatternType)
	assert.Contains(t, result.MergedOutput, `"result": "success"`)
}

func TestPipelineRetry_SchemaRetryPromptIncludesSchema(t *testing.T) {
	t.Parallel()

	executor := &PipelineExecutor{}
	schema := `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`

	prompt := executor.buildStageRetryPrompt(
		"Original task here",
		"bad output",
		"JSON Schema validation failed: missing required field: name",
		schema,
		true, // includeValidValues
		1, 2,
	)

	assert.Contains(t, prompt, "OUTPUT VALIDATION FAILED (retry 1 of 2)")
	assert.Contains(t, prompt, "bad output")
	assert.Contains(t, prompt, "missing required field: name")
	assert.Contains(t, prompt, "REQUIRED JSON SCHEMA:")
	assert.Contains(t, prompt, schema)
	assert.Contains(t, prompt, "MUST be valid JSON conforming to the schema")
	assert.Contains(t, prompt, "Original task here")
}

func TestPipelineRetry_LLMValidationRetryPrompt(t *testing.T) {
	t.Parallel()

	executor := &PipelineExecutor{}

	prompt := executor.buildStageRetryPrompt(
		"Original task here",
		"bad output",
		"LLM validation failed against criteria: output must contain a table",
		"", // no schema
		true,
		1, 2,
	)

	assert.Contains(t, prompt, "OUTPUT VALIDATION FAILED (retry 1 of 2)")
	assert.Contains(t, prompt, "bad output")
	assert.Contains(t, prompt, "LLM validation failed")
	assert.NotContains(t, prompt, "REQUIRED JSON SCHEMA")
	assert.Contains(t, prompt, "Re-read the original task below")
	assert.Contains(t, prompt, "Original task here")
}

func TestPipelineRetry_IncludeValidValuesFalse(t *testing.T) {
	t.Parallel()

	executor := &PipelineExecutor{}
	schema := `{"type":"object","required":["name"]}`

	prompt := executor.buildStageRetryPrompt(
		"Original task here",
		"bad output",
		"JSON Schema validation failed",
		schema,
		false, // includeValidValues = false
		1, 2,
	)

	// Schema should NOT be included when includeValidValues is false
	assert.NotContains(t, prompt, "REQUIRED JSON SCHEMA")
	assert.Contains(t, prompt, "Re-read the original task below")
}

func TestPipelineRetry_BothSchemaAndLLMValidation(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Agent outputs valid JSON that passes schema but fails LLM validation,
	// then on retry outputs JSON that passes both.
	agentLLM := newMockLLMProvider(
		`{"result": "incomplete"}`,    // passes schema, but LLM says invalid
		`{"result": "complete data"}`, // passes both
	)
	ag := createMockAgent(t, "agent", agentLLM)

	// Merge LLM for validation: first call says "no" (invalid), second says "yes" (valid)
	validationLLM := newMockLLMProvider("no, the result is incomplete", "yes, this is valid")

	orch := NewOrchestrator(Config{
		Logger:      logger,
		Tracer:      tracer,
		LLMProvider: validationLLM,
	})
	orch.RegisterAgent("agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Extract data",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:          "agent",
						PromptTemplate:   "Extract: {{previous}}",
						OutputSchema:     `{"type":"object","required":["result"],"properties":{"result":{"type":"string"}}}`,
						ValidationPrompt: "Does the result field contain complete data? Answer yes or no.",
						RetryPolicy: &loomv1.OutputRetryPolicy{
							MaxRetries:         2,
							IncludeValidValues: true,
						},
					},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Contains(t, result.MergedOutput, "complete data")
}

func TestPipelineRetry_GracefulDegradation(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// All outputs fail schema validation
	agentLLM := newMockLLMProvider(
		"not json at all",
		"still not json",
		"definitely not json",
	)
	ag := createMockAgent(t, "bad-agent", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("bad-agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Extract data",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "bad-agent",
						PromptTemplate: "Extract: {{previous}}",
						OutputSchema:   `{"type":"object","required":["result"]}`,
						RetryPolicy: &loomv1.OutputRetryPolicy{
							MaxRetries: 2,
						},
					},
				},
			},
		},
	}

	// Should succeed with graceful degradation (uses last output)
	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "pipeline", result.PatternType)
	// Output should be from the first call (graceful degradation keeps original)
	assert.Equal(t, "not json at all", result.MergedOutput)
}

func TestPipelineRetry_NoRetryPolicyFatalError(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	agentLLM := newMockLLMProvider("not json")
	ag := createMockAgent(t, "agent", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Do something",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "agent",
						PromptTemplate: "Process: {{previous}}",
						OutputSchema:   `{"type":"object","required":["result"]}`,
						// No RetryPolicy — fatal on validation failure
					},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output validation failed")
	assert.Contains(t, err.Error(), "JSON Schema validation failed")
}

func TestPipelineRetry_SchemaOnlyNoValidationPrompt(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Valid JSON matching schema on first try — no retry needed
	agentLLM := newMockLLMProvider(`{"name": "test", "value": 123}`)
	ag := createMockAgent(t, "agent", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Generate data",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "agent",
						PromptTemplate: "Generate: {{previous}}",
						OutputSchema:   `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`,
						// No validation_prompt, schema only
					},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Contains(t, result.MergedOutput, "test")
}

func TestPipelineRetry_ExtractJSONFromMixedText(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Agent wraps JSON in prose — schema validation should still extract and validate
	agentLLM := newMockLLMProvider(`Here is the result: {"name": "extracted", "count": 5} That's the data.`)
	ag := createMockAgent(t, "agent", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Extract data",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "agent",
						PromptTemplate: "Extract: {{previous}}",
						OutputSchema:   `{"type":"object","required":["name","count"],"properties":{"name":{"type":"string"},"count":{"type":"integer"}}}`,
					},
				},
			},
		},
	}

	// Should pass because JSON is extracted from the mixed text before validation
	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Contains(t, result.MergedOutput, "extracted")
}

func TestPipelineRetry_RetryCallCount(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// First and second fail, third succeeds
	agentLLM := newMockLLMProvider(
		"bad output",
		"still bad",
		`{"result": "valid"}`,
	)
	ag := createMockAgent(t, "agent", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Do task",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:        "agent",
						PromptTemplate: "Process: {{previous}}",
						OutputSchema:   `{"type":"object","required":["result"]}`,
						RetryPolicy: &loomv1.OutputRetryPolicy{
							MaxRetries: 2,
						},
					},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Contains(t, result.MergedOutput, "valid")

	// 3 calls: initial + 2 retries
	agentLLM.mu.Lock()
	assert.Equal(t, 3, agentLLM.callCount)
	agentLLM.mu.Unlock()
}

func TestValidateStageOutputSchema(t *testing.T) {
	t.Parallel()

	executor := &PipelineExecutor{}
	schema := `{"type":"object","required":["name","count"],"properties":{"name":{"type":"string"},"count":{"type":"integer"}}}`

	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{"valid JSON", `{"name":"test","count":42}`, false},
		{"missing required field", `{"name":"test"}`, true},
		{"wrong type", `{"name":"test","count":"not-a-number"}`, true},
		{"not JSON", "just plain text", true},
		{"JSON in prose", `Result: {"name":"test","count":42} done`, false},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			extractedJSON, err := executor.validateStageOutputSchema(tt.output, schema)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, extractedJSON)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, extractedJSON)
			}
		})
	}
}
