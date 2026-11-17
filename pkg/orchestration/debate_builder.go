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

// DebateBuilder provides a fluent API for building multi-agent debates.
// Agents participate in rounds where each agent sees previous round outputs
// and can build on or challenge them.
type DebateBuilder struct {
	orchestrator  *Orchestrator
	topic         string
	agentIDs      []string
	rounds        int32
	mergeStrategy loomv1.MergeStrategy
	moderatorID   string
	joinPolicy    *loomv1.JoinPolicy
}

// WithAgents adds agents to the debate.
// Each agent will participate in all debate rounds.
func (b *DebateBuilder) WithAgents(agents ...*agent.Agent) *DebateBuilder {
	for _, ag := range agents {
		// Register agent with orchestrator using pointer as unique ID
		agentID := fmt.Sprintf("debate_agent_%p", ag)
		b.orchestrator.RegisterAgent(agentID, ag)
		b.agentIDs = append(b.agentIDs, agentID)
	}
	return b
}

// WithAgentIDs adds agents by their registry IDs.
// This allows referencing agents already registered with the orchestrator.
func (b *DebateBuilder) WithAgentIDs(agentIDs ...string) *DebateBuilder {
	b.agentIDs = append(b.agentIDs, agentIDs...)
	return b
}

// WithRounds sets the number of debate rounds.
// Default is 1 round. More rounds allow agents to refine arguments.
func (b *DebateBuilder) WithRounds(rounds int) *DebateBuilder {
	b.rounds = types.SafeInt32(rounds)
	return b
}

// WithMergeStrategy records a merge strategy that the debate engine never
// reads. The debate's output is the moderator's synthesis when a moderator
// is set, else the last round's synthesis line.
//
// Deprecated: has no effect; kept so existing callers compile. Use
// WithModerator for a synthesized outcome and WithJoinPolicy to bound it.
func (b *DebateBuilder) WithMergeStrategy(strategy loomv1.MergeStrategy) *DebateBuilder {
	b.mergeStrategy = strategy
	return b
}

// WithJoinPolicy bounds the two prompts that carry other agents' full
// positions: each debater's round 2+ response prompt and the moderator's
// synthesis. agent_id must be empty (the moderator is the join agent).
func (b *DebateBuilder) WithJoinPolicy(policy *loomv1.JoinPolicy) *DebateBuilder {
	b.joinPolicy = policy
	return b
}

// WithModerator adds a moderator agent to guide the debate.
// The moderator can provide structure and ensure productive discussion.
func (b *DebateBuilder) WithModerator(moderator *agent.Agent) *DebateBuilder {
	moderatorID := fmt.Sprintf("debate_moderator_%p", moderator)
	b.orchestrator.RegisterAgent(moderatorID, moderator)
	b.moderatorID = moderatorID
	return b
}

// Execute runs the debate and returns the merged result.
func (b *DebateBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	// Validate configuration
	if b.topic == "" {
		return nil, fmt.Errorf("debate topic cannot be empty")
	}
	if len(b.agentIDs) < 2 {
		return nil, fmt.Errorf("debate requires at least 2 agents, got %d", len(b.agentIDs))
	}
	if b.rounds < 1 {
		return nil, fmt.Errorf("debate requires at least 1 round, got %d", b.rounds)
	}
	if b.joinPolicy.GetAgentId() != "" {
		return nil, fmt.Errorf("debate join policy must not set agent_id (the moderator is the join agent), got %q", b.joinPolicy.GetAgentId())
	}

	// Build debate pattern. The deprecated merge_strategy is not carried:
	// nothing reads it.
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{
			Debate: &loomv1.DebatePattern{
				Topic:            b.topic,
				AgentIds:         b.agentIDs,
				Rounds:           b.rounds,
				ModeratorAgentId: b.moderatorID,
				JoinPolicy:       b.joinPolicy,
			},
		},
	}

	// Execute through orchestrator
	return b.orchestrator.ExecutePattern(ctx, pattern)
}
