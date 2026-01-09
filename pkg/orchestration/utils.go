// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// extractToolNames extracts unique tool names from ToolExecutions slice.
func extractToolNames(toolExecutions []agent.ToolExecution) []string {
	seen := make(map[string]bool)
	names := make([]string, 0)
	for _, tool := range toolExecutions {
		if !seen[tool.ToolName] {
			seen[tool.ToolName] = true
			names = append(names, tool.ToolName)
		}
	}
	return names
}

// ExtractAgentIDs returns all agent IDs referenced in a workflow pattern.
// Used to determine which agents need to be loaded before execution.
// This is called by ExecuteWorkflow RPC to load all required agents from the registry.
func ExtractAgentIDs(pattern *loomv1.WorkflowPattern) []string {
	var ids []string

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		for _, stage := range p.Pipeline.Stages {
			ids = append(ids, stage.AgentId)
		}

	case *loomv1.WorkflowPattern_ForkJoin:
		ids = append(ids, p.ForkJoin.AgentIds...)

	case *loomv1.WorkflowPattern_Parallel:
		for _, task := range p.Parallel.Tasks {
			ids = append(ids, task.AgentId)
		}

	case *loomv1.WorkflowPattern_Debate:
		ids = append(ids, p.Debate.AgentIds...)
		if p.Debate.ModeratorAgentId != "" {
			ids = append(ids, p.Debate.ModeratorAgentId)
		}

	case *loomv1.WorkflowPattern_Conditional:
		if p.Conditional.ConditionAgentId != "" {
			ids = append(ids, p.Conditional.ConditionAgentId)
		}
		// Extract from branches
		for _, branch := range p.Conditional.Branches {
			ids = append(ids, ExtractAgentIDs(branch)...)
		}
		if p.Conditional.DefaultBranch != nil {
			ids = append(ids, ExtractAgentIDs(p.Conditional.DefaultBranch)...)
		}

	case *loomv1.WorkflowPattern_Swarm:
		ids = append(ids, p.Swarm.AgentIds...)
		if p.Swarm.JudgeAgentId != "" {
			ids = append(ids, p.Swarm.JudgeAgentId)
		}

	case *loomv1.WorkflowPattern_Iterative:
		// Iterative wraps a pipeline
		if p.Iterative.Pipeline != nil {
			for _, stage := range p.Iterative.Pipeline.Stages {
				ids = append(ids, stage.AgentId)
			}
		}

	case *loomv1.WorkflowPattern_PairProgramming:
		if p.PairProgramming.DriverAgentId != "" {
			ids = append(ids, p.PairProgramming.DriverAgentId)
		}
		if p.PairProgramming.NavigatorAgentId != "" {
			ids = append(ids, p.PairProgramming.NavigatorAgentId)
		}

	case *loomv1.WorkflowPattern_TeacherStudent:
		if p.TeacherStudent.TeacherAgentId != "" {
			ids = append(ids, p.TeacherStudent.TeacherAgentId)
		}
		if p.TeacherStudent.StudentAgentId != "" {
			ids = append(ids, p.TeacherStudent.StudentAgentId)
		}
	}

	return uniqueStrings(ids)
}

// uniqueStrings returns a slice with duplicate strings removed.
func uniqueStrings(strs []string) []string {
	seen := make(map[string]bool)
	result := []string{}

	for _, s := range strs {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return result
}
