// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
)

// ForkJoinBuilder provides a fluent API for building fork-join patterns.
// Fork-join executes multiple agents in parallel with the same prompt,
// then merges their results using a specified strategy.
type ForkJoinBuilder struct {
	orchestrator  *Orchestrator
	prompt        string
	agentIDs      []string
	mergeStrategy loomv1.MergeStrategy
	timeoutSecs   int32
}

// WithAgents adds agents to execute in parallel.
// All agents will receive the same prompt.
func (b *ForkJoinBuilder) WithAgents(agents ...*agent.Agent) *ForkJoinBuilder {
	for _, ag := range agents {
		// Register agent with orchestrator using pointer as unique ID
		agentID := fmt.Sprintf("fork_join_agent_%p", ag)
		b.orchestrator.RegisterAgent(agentID, ag)
		b.agentIDs = append(b.agentIDs, agentID)
	}
	return b
}

// WithAgentIDs adds agents by their registry IDs.
// This allows referencing agents already registered with the orchestrator.
func (b *ForkJoinBuilder) WithAgentIDs(agentIDs ...string) *ForkJoinBuilder {
	b.agentIDs = append(b.agentIDs, agentIDs...)
	return b
}

// WithTimeout sets the maximum execution time in seconds.
// If agents don't complete within this time, the execution will be cancelled.
func (b *ForkJoinBuilder) WithTimeout(seconds int) *ForkJoinBuilder {
	b.timeoutSecs = types.SafeInt32(seconds)
	return b
}

// Join sets the merge strategy for combining agent outputs.
// Options: CONSENSUS, VOTING, SUMMARY, CONCATENATE, FIRST, BEST.
func (b *ForkJoinBuilder) Join(strategy loomv1.MergeStrategy) *ForkJoinBuilder {
	b.mergeStrategy = strategy
	return b
}

// Execute runs the fork-join pattern and returns the merged result.
func (b *ForkJoinBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	// Validate configuration
	if b.prompt == "" {
		return nil, fmt.Errorf("fork-join prompt cannot be empty")
	}
	if len(b.agentIDs) < 1 {
		return nil, fmt.Errorf("fork-join requires at least 1 agent, got %d", len(b.agentIDs))
	}

	// Build fork-join pattern
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: &loomv1.ForkJoinPattern{
				Prompt:         b.prompt,
				AgentIds:       b.agentIDs,
				MergeStrategy:  b.mergeStrategy,
				TimeoutSeconds: b.timeoutSecs,
			},
		},
	}

	// Execute through orchestrator
	return b.orchestrator.ExecutePattern(ctx, pattern)
}
