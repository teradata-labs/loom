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

// ParallelBuilder provides a fluent API for building parallel patterns.
// Parallel patterns execute independent tasks concurrently, where each
// task has its own agent and prompt.
type ParallelBuilder struct {
	orchestrator  *Orchestrator
	tasks         []*loomv1.AgentTask
	mergeStrategy loomv1.MergeStrategy
	timeoutSecs   int32
}

// WithTask adds an independent task with a specific prompt.
// Each task runs concurrently with its own agent and prompt.
func (b *ParallelBuilder) WithTask(ag *agent.Agent, prompt string) *ParallelBuilder {
	// Register agent with orchestrator using pointer as unique ID
	agentID := fmt.Sprintf("parallel_agent_%p", ag)
	b.orchestrator.RegisterAgent(agentID, ag)

	task := &loomv1.AgentTask{
		AgentId:  agentID,
		Prompt:   prompt,
		Metadata: make(map[string]string),
	}

	b.tasks = append(b.tasks, task)
	return b
}

// WithTaskMetadata adds a task with metadata.
// Metadata can be used to label or categorize tasks.
func (b *ParallelBuilder) WithTaskMetadata(ag *agent.Agent, prompt string, metadata map[string]string) *ParallelBuilder {
	// Register agent with orchestrator using pointer as unique ID
	agentID := fmt.Sprintf("parallel_agent_%p", ag)
	b.orchestrator.RegisterAgent(agentID, ag)

	task := &loomv1.AgentTask{
		AgentId:  agentID,
		Prompt:   prompt,
		Metadata: metadata,
	}

	b.tasks = append(b.tasks, task)
	return b
}

// WithTaskByID adds a task using an agent ID.
// This allows referencing agents already registered with the orchestrator.
func (b *ParallelBuilder) WithTaskByID(agentID string, prompt string) *ParallelBuilder {
	task := &loomv1.AgentTask{
		AgentId:  agentID,
		Prompt:   prompt,
		Metadata: make(map[string]string),
	}

	b.tasks = append(b.tasks, task)
	return b
}

// WithMergeStrategy sets how to merge the task results.
// Options: CONSENSUS, VOTING, SUMMARY, CONCATENATE, FIRST, BEST.
func (b *ParallelBuilder) WithMergeStrategy(strategy loomv1.MergeStrategy) *ParallelBuilder {
	b.mergeStrategy = strategy
	return b
}

// WithTimeout sets the maximum execution time in seconds.
// If tasks don't complete within this time, the execution will be cancelled.
func (b *ParallelBuilder) WithTimeout(seconds int) *ParallelBuilder {
	b.timeoutSecs = types.SafeInt32(seconds)
	return b
}

// Execute runs all tasks in parallel and returns the merged result.
func (b *ParallelBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	// Validate configuration
	if len(b.tasks) < 1 {
		return nil, fmt.Errorf("parallel execution requires at least 1 task, got %d", len(b.tasks))
	}

	// Build parallel pattern
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks:          b.tasks,
				MergeStrategy:  b.mergeStrategy,
				TimeoutSeconds: b.timeoutSecs,
			},
		},
	}

	// Execute through orchestrator
	return b.orchestrator.ExecutePattern(ctx, pattern)
}
