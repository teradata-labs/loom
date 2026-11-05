// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
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
		run      func(o *Orchestrator, agents []*agent.Agent, strategy loomv1.MergeStrategy) (*loomv1.WorkflowResult, error)
	}{
		{
			name:     "fork-join concatenate",
			strategy: loomv1.MergeStrategy_CONCATENATE,
			run: func(o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				return o.Fork("analyze").WithAgents(agents...).Join(s).Execute(context.Background())
			},
		},
		{
			name:     "fork-join first",
			strategy: loomv1.MergeStrategy_FIRST,
			run: func(o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				return o.Fork("analyze").WithAgents(agents...).Join(s).Execute(context.Background())
			},
		},
		{
			name:     "parallel concatenate",
			strategy: loomv1.MergeStrategy_CONCATENATE,
			run: func(o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				b := o.Parallel()
				for i, ag := range agents {
					b = b.WithTask(ag, fmt.Sprintf("task %d", i))
				}
				return b.WithMergeStrategy(s).Execute(context.Background())
			},
		},
		{
			name:     "parallel first",
			strategy: loomv1.MergeStrategy_FIRST,
			run: func(o *Orchestrator, agents []*agent.Agent, s loomv1.MergeStrategy) (*loomv1.WorkflowResult, error) {
				b := o.Parallel()
				for i, ag := range agents {
					b = b.WithTask(ag, fmt.Sprintf("task %d", i))
				}
				return b.WithMergeStrategy(s).Execute(context.Background())
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

			result, err := tt.run(o, agents, tt.strategy)
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
