// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// gatedLLMProvider answers only after the gate it waits on closes, then
// closes its own gate. Chaining the gates makes the agents finish in a
// chosen order.
type gatedLLMProvider struct {
	response string
	wait     <-chan struct{} // nil: answer at once
	done     chan struct{}
}

func (g *gatedLLMProvider) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if g.wait != nil {
		select {
		case <-g.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// Give the branch that just answered time to hand its result back.
		time.Sleep(25 * time.Millisecond)
	}
	defer close(g.done)
	return &llmtypes.LLMResponse{
		Content:    g.response,
		StopReason: "stop",
		Usage:      llmtypes.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CostUSD: 0.001},
	}, nil
}

func (g *gatedLLMProvider) Name() string  { return "gated" }
func (g *gatedLLMProvider) Model() string { return "gated-model" }

// reverseOrderAgents returns n agents that finish last-to-first: agent i
// answers only after agent i+1 has answered.
func reverseOrderAgents(t *testing.T, n int) []*agent.Agent {
	t.Helper()
	llms := make([]*gatedLLMProvider, n)
	for i := n - 1; i >= 0; i-- {
		llms[i] = &gatedLLMProvider{response: fmt.Sprintf("output of agent %d", i), done: make(chan struct{})}
		if i < n-1 {
			llms[i].wait = llms[i+1].done
		}
	}
	agents := make([]*agent.Agent, n)
	for i := range agents {
		agents[i] = createMockAgent(t, fmt.Sprintf("agent%d", i), llms[i])
	}
	return agents
}

// Callers (loom-cloud among them) map AgentResults back to their agents by
// index, so the results must keep the configured order, not the order the
// branches finish in.
func TestFanInResultsKeepConfiguredOrder(t *testing.T) {
	const n = 4

	tests := []struct {
		name     string
		strategy loomv1.MergeStrategy
		run      func(ctx context.Context, o *Orchestrator, agents []*agent.Agent, strategy loomv1.MergeStrategy) (*loomv1.WorkflowResult, error)
	}{
		{
			name:     "fork-join concatenate",
			strategy: loomv1.MergeStrategy_CONCATENATE,
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				return o.Fork("analyze").WithAgents(agents...).Join(s).Execute(ctx)
			},
		},
		{
			name:     "fork-join first",
			strategy: loomv1.MergeStrategy_FIRST,
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				return o.Fork("analyze").WithAgents(agents...).Join(s).Execute(ctx)
			},
		},
		{
			name:     "parallel concatenate",
			strategy: loomv1.MergeStrategy_CONCATENATE,
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				b := o.Parallel()
				for i, ag := range agents {
					b = b.WithTask(ag, fmt.Sprintf("task %d", i))
				}
				return b.WithMergeStrategy(s).Execute(ctx)
			},
		},
		{
			name:     "parallel first",
			strategy: loomv1.MergeStrategy_FIRST,
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				b := o.Parallel()
				for i, ag := range agents {
					b = b.WithTask(ag, fmt.Sprintf("task %d", i))
				}
				return b.WithMergeStrategy(s).Execute(ctx)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewOrchestrator(Config{
				Logger:      zaptest.NewLogger(t),
				Tracer:      observability.NewNoOpTracer(),
				LLMProvider: newMockLLMProvider("merged"),
			})
			agents := reverseOrderAgents(t, n)
			// The gates chain agent i to agent i+1, so they deadlock if an
			// LLM semaphore has fewer slots than agents. The timeout makes
			// that a failure instead of a hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := tt.run(ctx, o, agents, tt.strategy)
			require.NoError(t, err)
			require.Len(t, result.AgentResults, n)

			for i, ar := range result.AgentResults {
				require.NotNil(t, ar, "result %d", i)
				assert.Equal(t, fmt.Sprintf("output of agent %d", i), ar.Output, "result %d is out of order", i)
			}
			if tt.strategy == loomv1.MergeStrategy_FIRST {
				assert.Equal(t, "output of agent 0", result.MergedOutput, "FIRST must pick the first configured agent")
			}
		})
	}
}

// failingLLMProvider fails every call.
type failingLLMProvider struct{}

func (failingLLMProvider) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return nil, errors.New("provider unavailable")
}

func (failingLLMProvider) Name() string  { return "failing" }
func (failingLLMProvider) Model() string { return "failing-model" }

// FIRST returns the first configured agent that succeeded. A failed agent 0
// must not make its error text the workflow's answer.
func TestFirstMergeSkipsFailedAgents(t *testing.T) {
	newAgents := func(t *testing.T) []*agent.Agent {
		return []*agent.Agent{
			createMockAgent(t, "agent0", failingLLMProvider{}),
			createMockAgent(t, "agent1", &gatedLLMProvider{response: "output of agent 1", done: make(chan struct{})}),
		}
	}
	tests := []struct {
		name string
		run  func(ctx context.Context, o *Orchestrator, agents []*agent.Agent) (*loomv1.WorkflowResult, error)
	}{
		{
			name: "fork-join",
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent) (*loomv1.WorkflowResult, error) {
				return o.Fork("analyze").WithAgents(agents...).Join(loomv1.MergeStrategy_FIRST).Execute(ctx)
			},
		},
		{
			name: "parallel",
			run: func(ctx context.Context, o *Orchestrator, agents []*agent.Agent) (*loomv1.WorkflowResult, error) {
				b := o.Parallel()
				for i, ag := range agents {
					b = b.WithTask(ag, fmt.Sprintf("task %d", i))
				}
				return b.WithMergeStrategy(loomv1.MergeStrategy_FIRST).Execute(ctx)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := NewOrchestrator(Config{
				Logger:      zaptest.NewLogger(t),
				Tracer:      observability.NewNoOpTracer(),
				LLMProvider: newMockLLMProvider("merged"),
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			result, err := tt.run(ctx, o, newAgents(t))
			require.NoError(t, err)
			require.Len(t, result.AgentResults, 2)
			assert.NotEmpty(t, result.AgentResults[0].Metadata["error"], "agent 0 should have failed")
			assert.Equal(t, "output of agent 1", result.MergedOutput)
		})
	}
}

func TestFirstSuccessfulOutput(t *testing.T) {
	failed := func(out string) *loomv1.AgentResult {
		return &loomv1.AgentResult{Output: out, Metadata: map[string]string{"error": "boom"}}
	}
	ok := func(out string) *loomv1.AgentResult { return &loomv1.AgentResult{Output: out} }

	tests := []struct {
		name    string
		results []*loomv1.AgentResult
		want    string
	}{
		{"empty", nil, ""},
		{"first succeeds", []*loomv1.AgentResult{ok("a"), ok("b")}, "a"},
		{"first failed", []*loomv1.AgentResult{failed("Error: a"), ok("b")}, "b"},
		{"nil slot skipped", []*loomv1.AgentResult{nil, ok("b")}, "b"},
		{"all failed falls back to the first", []*loomv1.AgentResult{failed("Error: a"), failed("Error: b")}, "Error: a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, firstSuccessfulOutput(tt.results))
		})
	}
}
