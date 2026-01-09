// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/proto"
)

// InterpolateVariables replaces {{var}} placeholders in pattern prompts with provided values.
// Returns a new WorkflowPattern with interpolated prompts (does not modify original).
// This is used by the ExecuteWorkflow RPC to inject user-provided variables into workflow prompts.
func InterpolateVariables(pattern *loomv1.WorkflowPattern, vars map[string]string) *loomv1.WorkflowPattern {
	if len(vars) == 0 {
		return pattern // No variables to interpolate
	}

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		pipeline := proto.Clone(p.Pipeline).(*loomv1.PipelinePattern)
		pipeline.InitialPrompt = interpolateString(pipeline.InitialPrompt, vars)
		for _, stage := range pipeline.Stages {
			stage.PromptTemplate = interpolateString(stage.PromptTemplate, vars)
			stage.ValidationPrompt = interpolateString(stage.ValidationPrompt, vars)
		}
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: pipeline}}

	case *loomv1.WorkflowPattern_ForkJoin:
		forkJoin := proto.Clone(p.ForkJoin).(*loomv1.ForkJoinPattern)
		forkJoin.Prompt = interpolateString(forkJoin.Prompt, vars)
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: forkJoin}}

	case *loomv1.WorkflowPattern_Parallel:
		parallel := proto.Clone(p.Parallel).(*loomv1.ParallelPattern)
		for _, task := range parallel.Tasks {
			task.Prompt = interpolateString(task.Prompt, vars)
		}
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Parallel{Parallel: parallel}}

	case *loomv1.WorkflowPattern_Debate:
		debate := proto.Clone(p.Debate).(*loomv1.DebatePattern)
		debate.Topic = interpolateString(debate.Topic, vars)
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Debate{Debate: debate}}

	case *loomv1.WorkflowPattern_Conditional:
		conditional := proto.Clone(p.Conditional).(*loomv1.ConditionalPattern)
		conditional.ConditionPrompt = interpolateString(conditional.ConditionPrompt, vars)
		// Recursively interpolate branches
		for key, branch := range conditional.Branches {
			conditional.Branches[key] = InterpolateVariables(branch, vars)
		}
		if conditional.DefaultBranch != nil {
			conditional.DefaultBranch = InterpolateVariables(conditional.DefaultBranch, vars)
		}
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: conditional}}

	case *loomv1.WorkflowPattern_Swarm:
		swarm := proto.Clone(p.Swarm).(*loomv1.SwarmPattern)
		swarm.Question = interpolateString(swarm.Question, vars)
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Swarm{Swarm: swarm}}

	case *loomv1.WorkflowPattern_Iterative:
		iterative := proto.Clone(p.Iterative).(*loomv1.IterativeWorkflowPattern)
		if iterative.Pipeline != nil {
			iterative.Pipeline.InitialPrompt = interpolateString(iterative.Pipeline.InitialPrompt, vars)
			for _, stage := range iterative.Pipeline.Stages {
				stage.PromptTemplate = interpolateString(stage.PromptTemplate, vars)
				stage.ValidationPrompt = interpolateString(stage.ValidationPrompt, vars)
			}
		}
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: iterative}}

	case *loomv1.WorkflowPattern_PairProgramming:
		pairProg := proto.Clone(p.PairProgramming).(*loomv1.PairProgrammingPattern)
		pairProg.Task = interpolateString(pairProg.Task, vars)
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_PairProgramming{PairProgramming: pairProg}}

	case *loomv1.WorkflowPattern_TeacherStudent:
		teacherStudent := proto.Clone(p.TeacherStudent).(*loomv1.TeacherStudentPattern)
		teacherStudent.Objective = interpolateString(teacherStudent.Objective, vars)
		return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_TeacherStudent{TeacherStudent: teacherStudent}}
	}

	// Unknown pattern type, return as-is
	return pattern
}

// interpolateString replaces all {{key}} placeholders with corresponding values from vars.
// Example: "Hello {{name}}!" with {"name": "World"} becomes "Hello World!"
func interpolateString(s string, vars map[string]string) string {
	if s == "" || len(vars) == 0 {
		return s
	}

	result := s
	for key, value := range vars {
		placeholder := fmt.Sprintf("{{%s}}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
