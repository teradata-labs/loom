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
)

// PipelineBuilder provides a fluent API for building pipeline patterns.
// Pipelines execute agents sequentially, where each agent's output
// becomes input for the next agent.
type PipelineBuilder struct {
	orchestrator    *Orchestrator
	initialPrompt   string
	stages          []*loomv1.PipelineStage
	passFullHistory bool
}

// WithStage adds a stage to the pipeline.
// The promptTemplate can include placeholders:
// - {{previous}}: Replaced with the previous stage's output
// - {{history}}: Replaced with all previous outputs
func (b *PipelineBuilder) WithStage(ag *agent.Agent, promptTemplate string) *PipelineBuilder {
	// Register agent with orchestrator using pointer as unique ID
	agentID := fmt.Sprintf("pipeline_agent_%p", ag)
	b.orchestrator.RegisterAgent(agentID, ag)

	stage := &loomv1.PipelineStage{
		AgentId:        agentID,
		PromptTemplate: promptTemplate,
	}

	b.stages = append(b.stages, stage)
	return b
}

// WithStageValidation adds a stage with output validation.
// The validationPrompt should check if the output meets requirements.
// Use {{output}} placeholder to reference the stage output.
func (b *PipelineBuilder) WithStageValidation(ag *agent.Agent, promptTemplate string, validationPrompt string) *PipelineBuilder {
	// Register agent with orchestrator using pointer as unique ID
	agentID := fmt.Sprintf("pipeline_agent_%p", ag)
	b.orchestrator.RegisterAgent(agentID, ag)

	stage := &loomv1.PipelineStage{
		AgentId:          agentID,
		PromptTemplate:   promptTemplate,
		ValidationPrompt: validationPrompt,
	}

	b.stages = append(b.stages, stage)
	return b
}

// WithStageByID adds a stage using an agent ID.
// This allows referencing agents already registered with the orchestrator.
func (b *PipelineBuilder) WithStageByID(agentID string, promptTemplate string) *PipelineBuilder {
	stage := &loomv1.PipelineStage{
		AgentId:        agentID,
		PromptTemplate: promptTemplate,
	}

	b.stages = append(b.stages, stage)
	return b
}

// WithFullHistory enables passing full history to each stage.
// By default, only the previous stage's output is available.
func (b *PipelineBuilder) WithFullHistory() *PipelineBuilder {
	b.passFullHistory = true
	return b
}

// Execute runs the pipeline and returns the final result.
func (b *PipelineBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	// Validate configuration
	if b.initialPrompt == "" {
		return nil, fmt.Errorf("pipeline initial prompt cannot be empty")
	}
	if len(b.stages) < 1 {
		return nil, fmt.Errorf("pipeline requires at least 1 stage, got %d", len(b.stages))
	}

	// Build pipeline pattern
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt:   b.initialPrompt,
				Stages:          b.stages,
				PassFullHistory: b.passFullHistory,
			},
		},
	}

	// Execute through orchestrator
	return b.orchestrator.ExecutePattern(ctx, pattern)
}
