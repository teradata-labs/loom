// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// progressRecorder collects agent progress events from concurrent agents.
type progressRecorder struct {
	mu     sync.Mutex
	events []agent.ProgressEvent
}

func (r *progressRecorder) callback(ev agent.ProgressEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

// byAgent returns the number of events for each AgentID ("" counts events
// that name no agent).
func (r *progressRecorder) byAgent() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, ev := range r.events {
		out[ev.AgentID]++
	}
	return out
}

func twoAgentPatterns() map[string]*loomv1.WorkflowPattern {
	return map[string]*loomv1.WorkflowPattern{
		"pipeline": {Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "go",
			Stages: []*loomv1.PipelineStage{
				{AgentId: "agent-a", PromptTemplate: "{{previous}}"},
				{AgentId: "agent-b", PromptTemplate: "{{previous}}"},
			},
		}}},
		"parallel": {Pattern: &loomv1.WorkflowPattern_Parallel{Parallel: &loomv1.ParallelPattern{
			Tasks: []*loomv1.AgentTask{
				{AgentId: "agent-a", Prompt: "task a"},
				{AgentId: "agent-b", Prompt: "task b"},
			},
			MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
		}}},
		"fork-join": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
			Prompt:        "work",
			AgentIds:      []string{"agent-a", "agent-b"},
			MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
		}}},
	}
}

func newTwoAgentOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
	orch.RegisterAgent("agent-a", createMockAgent(t, "agent-a", newMockLLMProvider("out a")))
	orch.RegisterAgent("agent-b", createMockAgent(t, "agent-b", newMockLLMProvider("out b")))
	return orch
}

// Every executor sends each agent's progress events to the callback in the
// context, and every event names the agent that emitted it, so a consumer can
// tell apart agents that run at the same time.
func TestExecutors_ProgressEventsNameTheirAgent(t *testing.T) {
	t.Parallel()
	for name, pattern := range twoAgentPatterns() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := &progressRecorder{}
			ctx := agent.ContextWithProgressCallback(context.Background(), rec.callback)

			result, err := newTwoAgentOrchestrator(t).ExecutePattern(ctx, pattern)
			require.NoError(t, err)
			require.Len(t, result.AgentResults, 2)

			counts := rec.byAgent()
			assert.Positive(t, counts["agent-a"], "agent-a emitted no named events")
			assert.Positive(t, counts["agent-b"], "agent-b emitted no named events")
			assert.Zero(t, counts[""], "an event named no agent")
			assert.Len(t, counts, 2, "an event named an unknown agent: %v", counts)
		})
	}
}

// Without a callback in the context, every executor still runs its agents
// (plain Chat) and returns their output.
func TestExecutors_NoProgressCallbackStillRuns(t *testing.T) {
	t.Parallel()
	for name, pattern := range twoAgentPatterns() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := newTwoAgentOrchestrator(t).ExecutePattern(context.Background(), pattern)
			require.NoError(t, err)
			require.Len(t, result.AgentResults, 2)
			outputs := []string{result.AgentResults[0].Output, result.AgentResults[1].Output}
			assert.ElementsMatch(t, []string{"out a", "out b"}, outputs)
		})
	}
}

// The wrapper names events that name no agent, and keeps the AgentID of an
// event that already names one (for example one forwarded from a nested
// workflow).
func TestNameAgentProgress(t *testing.T) {
	t.Parallel()
	rec := &progressRecorder{}
	cb := nameAgentProgress(rec.callback, "outer")
	cb(agent.ProgressEvent{Message: "plain"})
	cb(agent.ProgressEvent{Message: "nested", AgentID: "inner"})
	assert.Equal(t, map[string]int{"outer": 1, "inner": 1}, rec.byAgent())
}

// The swarm and conditional executors also name each agent's events: every
// voter of a swarm, and both the condition agent and the branch agent of a
// conditional.
func TestSwarmAndConditional_ProgressEventsNameTheirAgent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		responses map[string]string
		pattern   *loomv1.WorkflowPattern
	}{
		"swarm": {
			responses: map[string]string{"agent-a": "yes", "agent-b": "yes"},
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Swarm{Swarm: &loomv1.SwarmPattern{
				Question: "ship it?",
				AgentIds: []string{"agent-a", "agent-b"},
				Strategy: loomv1.VotingStrategy_MAJORITY,
			}}},
		},
		"conditional": {
			responses: map[string]string{"agent-a": "bug", "agent-b": "fixed"},
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "agent-a",
				ConditionPrompt:  "classify",
				Branches: map[string]*loomv1.WorkflowPattern{
					"bug": {Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
						Prompt:        "fix it",
						AgentIds:      []string{"agent-b"},
						MergeStrategy: loomv1.MergeStrategy_FIRST,
					}}},
				},
			}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
			for id, out := range tc.responses {
				orch.RegisterAgent(id, createMockAgent(t, id, newMockLLMProvider(out)))
			}
			rec := &progressRecorder{}
			ctx := agent.ContextWithProgressCallback(context.Background(), rec.callback)

			_, err := orch.ExecutePattern(ctx, tc.pattern)
			require.NoError(t, err)

			counts := rec.byAgent()
			assert.Positive(t, counts["agent-a"], "agent-a emitted no named events")
			assert.Positive(t, counts["agent-b"], "agent-b emitted no named events")
			assert.Zero(t, counts[""], "an event named no agent")
			assert.Len(t, counts, 2, "an event named an unknown agent: %v", counts)
		})
	}
}
