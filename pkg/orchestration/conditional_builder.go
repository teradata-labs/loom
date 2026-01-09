// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ConditionalBuilder provides a fluent API for building conditional patterns.
// Conditional patterns route execution based on a classifier agent's decision.
type ConditionalBuilder struct {
	orchestrator    *Orchestrator
	classifierID    string
	conditionPrompt string
	branches        map[string]*loomv1.WorkflowPattern
	defaultBranch   *loomv1.WorkflowPattern
}

// When adds a conditional branch.
// The condition string should match the expected output from the classifier agent.
// Matching is case-insensitive and supports substring matching.
func (b *ConditionalBuilder) When(condition string, pattern *loomv1.WorkflowPattern) *ConditionalBuilder {
	b.branches[condition] = pattern
	return b
}

// Default sets the default branch to execute if no conditions match.
// This is optional but recommended to handle unexpected classifier outputs.
func (b *ConditionalBuilder) Default(pattern *loomv1.WorkflowPattern) *ConditionalBuilder {
	b.defaultBranch = pattern
	return b
}

// Execute runs the conditional pattern and returns the result from the selected branch.
func (b *ConditionalBuilder) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	// Validate configuration
	if b.classifierID == "" {
		return nil, fmt.Errorf("conditional requires a classifier agent")
	}
	if b.conditionPrompt == "" {
		return nil, fmt.Errorf("conditional requires a condition prompt")
	}
	if len(b.branches) == 0 && b.defaultBranch == nil {
		return nil, fmt.Errorf("conditional requires at least one branch or a default branch")
	}

	// Build conditional pattern
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: b.classifierID,
				ConditionPrompt:  b.conditionPrompt,
				Branches:         b.branches,
				DefaultBranch:    b.defaultBranch,
			},
		},
	}

	// Execute through orchestrator
	return b.orchestrator.ExecutePattern(ctx, pattern)
}
