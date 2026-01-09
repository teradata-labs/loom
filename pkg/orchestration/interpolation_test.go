// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestInterpolateVariables_NoVariables(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Test prompt",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "agent1", PromptTemplate: "Stage 1"},
				},
			},
		},
	}

	// Should return same pattern when no variables provided
	result := InterpolateVariables(pattern, nil)
	if result != pattern {
		t.Error("Expected same pattern when no variables provided")
	}

	result = InterpolateVariables(pattern, map[string]string{})
	if result != pattern {
		t.Error("Expected same pattern when empty variables map provided")
	}
}

func TestInterpolateVariables_Pipeline(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Analyze {{language}} code",
				Stages: []*loomv1.PipelineStage{
					{
						AgentId:          "agent1",
						PromptTemplate:   "Check {{check_type}} in {{language}}",
						ValidationPrompt: "Validate {{check_type}} results",
					},
				},
			},
		},
	}

	vars := map[string]string{
		"language":   "Go",
		"check_type": "syntax",
	}

	result := InterpolateVariables(pattern, vars)

	// Verify initial prompt
	pipeline := result.Pattern.(*loomv1.WorkflowPattern_Pipeline).Pipeline
	if pipeline.InitialPrompt != "Analyze Go code" {
		t.Errorf("Expected 'Analyze Go code', got '%s'", pipeline.InitialPrompt)
	}

	// Verify stage prompt
	if pipeline.Stages[0].PromptTemplate != "Check syntax in Go" {
		t.Errorf("Expected 'Check syntax in Go', got '%s'", pipeline.Stages[0].PromptTemplate)
	}

	// Verify validation prompt
	if pipeline.Stages[0].ValidationPrompt != "Validate syntax results" {
		t.Errorf("Expected 'Validate syntax results', got '%s'", pipeline.Stages[0].ValidationPrompt)
	}
}

func TestInterpolateVariables_ForkJoin(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: &loomv1.ForkJoinPattern{
				Prompt:   "Review {{document_type}} for {{aspect}}",
				AgentIds: []string{"agent1", "agent2"},
			},
		},
	}

	vars := map[string]string{
		"document_type": "contract",
		"aspect":        "legal compliance",
	}

	result := InterpolateVariables(pattern, vars)
	forkJoin := result.Pattern.(*loomv1.WorkflowPattern_ForkJoin).ForkJoin

	if forkJoin.Prompt != "Review contract for legal compliance" {
		t.Errorf("Expected 'Review contract for legal compliance', got '%s'", forkJoin.Prompt)
	}
}

func TestInterpolateVariables_Parallel(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{
			Parallel: &loomv1.ParallelPattern{
				Tasks: []*loomv1.AgentTask{
					{AgentId: "agent1", Prompt: "Task 1: {{task1_input}}"},
					{AgentId: "agent2", Prompt: "Task 2: {{task2_input}}"},
				},
			},
		},
	}

	vars := map[string]string{
		"task1_input": "analyze metrics",
		"task2_input": "generate report",
	}

	result := InterpolateVariables(pattern, vars)
	parallel := result.Pattern.(*loomv1.WorkflowPattern_Parallel).Parallel

	if parallel.Tasks[0].Prompt != "Task 1: analyze metrics" {
		t.Errorf("Expected 'Task 1: analyze metrics', got '%s'", parallel.Tasks[0].Prompt)
	}

	if parallel.Tasks[1].Prompt != "Task 2: generate report" {
		t.Errorf("Expected 'Task 2: generate report', got '%s'", parallel.Tasks[1].Prompt)
	}
}

func TestInterpolateVariables_Debate(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{
			Debate: &loomv1.DebatePattern{
				Topic:            "Should we use {{technology}} for {{use_case}}?",
				AgentIds:         []string{"pro", "con"},
				ModeratorAgentId: "moderator",
			},
		},
	}

	vars := map[string]string{
		"technology": "microservices",
		"use_case":   "our backend",
	}

	result := InterpolateVariables(pattern, vars)
	debate := result.Pattern.(*loomv1.WorkflowPattern_Debate).Debate

	if debate.Topic != "Should we use microservices for our backend?" {
		t.Errorf("Expected 'Should we use microservices for our backend?', got '%s'", debate.Topic)
	}
}

func TestInterpolateVariables_Conditional(t *testing.T) {
	// Create a simple conditional with no branches (minimal test)
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Conditional{
			Conditional: &loomv1.ConditionalPattern{
				ConditionPrompt: "Classify {{input_type}} request",
				Branches:        make(map[string]*loomv1.WorkflowPattern),
			},
		},
	}

	vars := map[string]string{
		"input_type": "support",
	}

	result := InterpolateVariables(pattern, vars)
	conditional := result.Pattern.(*loomv1.WorkflowPattern_Conditional).Conditional

	if conditional.ConditionPrompt != "Classify support request" {
		t.Errorf("Expected 'Classify support request', got '%s'", conditional.ConditionPrompt)
	}
}

func TestInterpolateVariables_Swarm(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:     "How to optimize {{system}} for {{goal}}?",
				AgentIds:     []string{"agent1", "agent2"},
				JudgeAgentId: "judge",
			},
		},
	}

	vars := map[string]string{
		"system": "database",
		"goal":   "low latency",
	}

	result := InterpolateVariables(pattern, vars)
	swarm := result.Pattern.(*loomv1.WorkflowPattern_Swarm).Swarm

	if swarm.Question != "How to optimize database for low latency?" {
		t.Errorf("Expected 'How to optimize database for low latency?', got '%s'", swarm.Question)
	}
}

func TestInterpolateVariables_Iterative(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Iteratively improve {{feature}}",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "Improve {{feature}}"},
					},
				},
			},
		},
	}

	vars := map[string]string{
		"feature": "performance",
	}

	result := InterpolateVariables(pattern, vars)
	iterative := result.Pattern.(*loomv1.WorkflowPattern_Iterative).Iterative

	if iterative.Pipeline.InitialPrompt != "Iteratively improve performance" {
		t.Errorf("Expected 'Iteratively improve performance', got '%s'", iterative.Pipeline.InitialPrompt)
	}
}

func TestInterpolateVariables_PairProgramming(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_PairProgramming{
			PairProgramming: &loomv1.PairProgrammingPattern{
				Task:             "Implement {{feature}} with {{constraint}}",
				DriverAgentId:    "driver",
				NavigatorAgentId: "navigator",
			},
		},
	}

	vars := map[string]string{
		"feature":    "authentication",
		"constraint": "zero trust",
	}

	result := InterpolateVariables(pattern, vars)
	pairProg := result.Pattern.(*loomv1.WorkflowPattern_PairProgramming).PairProgramming

	if pairProg.Task != "Implement authentication with zero trust" {
		t.Errorf("Expected 'Implement authentication with zero trust', got '%s'", pairProg.Task)
	}
}

func TestInterpolateVariables_TeacherStudent(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_TeacherStudent{
			TeacherStudent: &loomv1.TeacherStudentPattern{
				Objective:      "Learn {{subject}} with focus on {{topic}}",
				TeacherAgentId: "teacher",
				StudentAgentId: "student",
			},
		},
	}

	vars := map[string]string{
		"subject": "Kubernetes",
		"topic":   "networking",
	}

	result := InterpolateVariables(pattern, vars)
	teacherStudent := result.Pattern.(*loomv1.WorkflowPattern_TeacherStudent).TeacherStudent

	if teacherStudent.Objective != "Learn Kubernetes with focus on networking" {
		t.Errorf("Expected 'Learn Kubernetes with focus on networking', got '%s'", teacherStudent.Objective)
	}
}

func TestInterpolateVariables_MultipleReplacements(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "{{var1}} and {{var1}} and {{var2}}",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "agent1", PromptTemplate: "{{var1}}"},
				},
			},
		},
	}

	vars := map[string]string{
		"var1": "first",
		"var2": "second",
	}

	result := InterpolateVariables(pattern, vars)
	pipeline := result.Pattern.(*loomv1.WorkflowPattern_Pipeline).Pipeline

	// Should replace all occurrences
	if pipeline.InitialPrompt != "first and first and second" {
		t.Errorf("Expected 'first and first and second', got '%s'", pipeline.InitialPrompt)
	}
}

func TestInterpolateVariables_UnmatchedPlaceholders(t *testing.T) {
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "{{known}} and {{unknown}}",
				Stages:        []*loomv1.PipelineStage{},
			},
		},
	}

	vars := map[string]string{
		"known": "value",
	}

	result := InterpolateVariables(pattern, vars)
	pipeline := result.Pattern.(*loomv1.WorkflowPattern_Pipeline).Pipeline

	// Unmatched placeholders should remain as-is
	if pipeline.InitialPrompt != "value and {{unknown}}" {
		t.Errorf("Expected 'value and {{unknown}}', got '%s'", pipeline.InitialPrompt)
	}
}
