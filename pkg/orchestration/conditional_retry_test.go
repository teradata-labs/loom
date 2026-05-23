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

func TestConditionalRetry_GarbageOutputRetried(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// First call returns garbage, second call (retry) returns valid key
	classifierLLM := newMockLLMProvider("I'm not sure what category this falls into", "bug")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	bugLLM := newMockLLMProvider("Fixed the bug!")
	bugAgent := createMockAgent(t, "bug-agent", bugLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)
	orch.RegisterAgent("bug-agent", bugAgent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this issue",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {
						Pattern: &loomv1.WorkflowPattern_ForkJoin{
							ForkJoin: &loomv1.ForkJoinPattern{
								Prompt:        "Fix the bug",
								AgentIds:      []string{"bug-agent"},
								MergeStrategy: loomv1.MergeStrategy_FIRST,
							},
						},
					},
					"feature": {
						Pattern: &loomv1.WorkflowPattern_ForkJoin{
							ForkJoin: &loomv1.ForkJoinPattern{
								Prompt:        "Build the feature",
								AgentIds:      []string{"bug-agent"},
								MergeStrategy: loomv1.MergeStrategy_FIRST,
							},
						},
					},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries:         2,
					IncludeValidValues: true,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "conditional", result.PatternType)
	assert.Equal(t, "bug", result.Metadata["selected_branch"])
}

func TestConditionalRetry_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// All calls return garbage — no valid key ever
	classifierLLM := newMockLLMProvider(
		"I have no idea",
		"still confused",
		"what are you asking?",
	)
	classifier := createMockAgent(t, "classifier", classifierLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this issue",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug":     {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Fix", AgentIds: []string{"x"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
					"feature": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Build", AgentIds: []string{"x"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching branch found for condition")
}

func TestConditionalRetry_MaxRetriesWithDefault(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// All calls return garbage, but default branch is configured.
	// With retry_policy, retries happen BEFORE falling back to default.
	// 1 initial call + 1 retry = 2 total classifier calls, all garbage.
	classifierLLM := newMockLLMProvider("nonsense", "still nonsense")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	defaultLLM := newMockLLMProvider("Default handler output")
	defaultAgent := createMockAgent(t, "default-agent", defaultLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)
	orch.RegisterAgent("default-agent", defaultAgent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this issue",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Fix", AgentIds: []string{"default-agent"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
				},
				DefaultBranch: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_ForkJoin{
						ForkJoin: &loomv1.ForkJoinPattern{
							Prompt:        "Default handling",
							AgentIds:      []string{"default-agent"},
							MergeStrategy: loomv1.MergeStrategy_FIRST,
						},
					},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 1,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "default", result.Metadata["selected_branch"])

	// Verify retries actually happened before falling back to default:
	// 1 initial + 1 retry = 2 classifier calls
	classifierLLM.mu.Lock()
	assert.Equal(t, 2, classifierLLM.callCount, "should have retried before falling back to default")
	classifierLLM.mu.Unlock()
}

func TestConditionalRetry_NoRetryPolicy(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Garbage output, no retry policy — should fail immediately (old behavior)
	classifierLLM := newMockLLMProvider("I'm not sure")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this issue",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug":     {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Fix", AgentIds: []string{"x"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
					"feature": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Build", AgentIds: []string{"x"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
				},
				// No RetryPolicy — nil
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching branch found for condition")
}

func TestConditionalRetry_CoercionAvoidsRetry(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Verbose output that coercion can handle — no retry needed
	classifierLLM := newMockLLMProvider("Based on my analysis, this is clearly a **bug** report.")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	bugLLM := newMockLLMProvider("Bug fixed!")
	bugAgent := createMockAgent(t, "bug-fixer", bugLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)
	orch.RegisterAgent("bug-fixer", bugAgent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this issue",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {
						Pattern: &loomv1.WorkflowPattern_ForkJoin{
							ForkJoin: &loomv1.ForkJoinPattern{
								Prompt:        "Fix bug",
								AgentIds:      []string{"bug-fixer"},
								MergeStrategy: loomv1.MergeStrategy_FIRST,
							},
						},
					},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "bug", result.Metadata["selected_branch"])

	// Classifier should only have been called once (coercion handled it, no retry)
	classifierLLM.mu.Lock()
	assert.Equal(t, 1, classifierLLM.callCount, "coercion should avoid retry LLM call")
	classifierLLM.mu.Unlock()
}

func TestConditionalRetry_RetryCallCount(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// First two calls return garbage, third returns valid key
	classifierLLM := newMockLLMProvider("garbage", "still garbage", "bug")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	bugLLM := newMockLLMProvider("Fixed!")
	bugAgent := createMockAgent(t, "bug-agent", bugLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)
	orch.RegisterAgent("bug-agent", bugAgent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {
						Pattern: &loomv1.WorkflowPattern_ForkJoin{
							ForkJoin: &loomv1.ForkJoinPattern{
								Prompt:        "Fix bug",
								AgentIds:      []string{"bug-agent"},
								MergeStrategy: loomv1.MergeStrategy_FIRST,
							},
						},
					},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "bug", result.Metadata["selected_branch"])

	// Classifier called 3 times: 1 initial + 2 retries (2nd retry succeeds)
	classifierLLM.mu.Lock()
	assert.Equal(t, 3, classifierLLM.callCount)
	classifierLLM.mu.Unlock()
}

func TestConditionalRetry_ZeroMaxRetries(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// RetryPolicy with MaxRetries=0 should behave same as nil (no retry)
	classifierLLM := newMockLLMProvider("garbage")
	classifier := createMockAgent(t, "classifier", classifierLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("classifier", classifier)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "Classify this",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{Prompt: "Fix", AgentIds: []string{"x"}, MergeStrategy: loomv1.MergeStrategy_FIRST}}},
				},
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 0, // explicit zero
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching branch found")

	// Only 1 call — no retries
	classifierLLM.mu.Lock()
	assert.Equal(t, 1, classifierLLM.callCount)
	classifierLLM.mu.Unlock()
}

func TestBuildConditionRetryPrompt(t *testing.T) {
	t.Parallel()

	executor := &ConditionalExecutor{
		pattern: &loomv1.ConditionalPattern{
			RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 2},
		},
	}

	prompt := executor.buildConditionRetryPrompt("some garbage output", []string{"bug", "feature", "refactor"}, 1, 2)

	assert.Contains(t, prompt, `"some garbage output"`)
	assert.Contains(t, prompt, "could not be matched to any valid workflow branch")
	assert.Contains(t, prompt, "- bug")
	assert.Contains(t, prompt, "- feature")
	assert.Contains(t, prompt, "- refactor")
	assert.Contains(t, prompt, "retry 1 of 2")
}
