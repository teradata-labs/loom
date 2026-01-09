// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// AgentProvider retrieves agents by ID.
// This interface allows different agent sources (registry, orchestrator, etc.)
type AgentProvider interface {
	// GetAgent retrieves an agent by ID.
	// Context parameter allows for cancellation and deadlines.
	GetAgent(ctx context.Context, id string) (*agent.Agent, error)
}

// Engine orchestrates multi-agent collaboration patterns.
// It dispatches to specialized orchestrators based on pattern type.
type Engine struct {
	provider       AgentProvider
	tracer         observability.Tracer
	logger         *zap.Logger
	debater        *DebateOrchestrator
	swarm          *SwarmOrchestrator
	pairProgrammer *PairProgrammingOrchestrator
	teacherStudent *TeacherStudentOrchestrator
}

// NewEngine creates a new collaboration engine.
func NewEngine(provider AgentProvider) *Engine {
	return NewEngineWithObservability(provider, observability.NewNoOpTracer(), zap.NewNop())
}

// NewEngineWithObservability creates a new collaboration engine with observability.
func NewEngineWithObservability(provider AgentProvider, tracer observability.Tracer, logger *zap.Logger) *Engine {
	return &Engine{
		provider:       provider,
		tracer:         tracer,
		logger:         logger,
		debater:        NewDebateOrchestratorWithObservability(provider, tracer, logger),
		swarm:          NewSwarmOrchestratorWithObservability(provider, tracer, logger),
		pairProgrammer: NewPairProgrammingOrchestrator(provider),
		teacherStudent: NewTeacherStudentOrchestrator(provider),
	}
}

// Execute runs a collaboration pattern and returns detailed results.
func (e *Engine) Execute(ctx context.Context, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	// Start tracing
	ctx, span := e.tracer.StartSpan(ctx, observability.SpanWorkflowExecution)
	defer e.tracer.EndSpan(span)

	patternType := getCollaborationPatternType(pattern)
	if span != nil {
		span.SetAttribute("pattern_type", patternType)
		span.SetAttribute("collaboration", "true")
	}

	e.logger.Info("Starting collaboration execution",
		zap.String("pattern_type", patternType))

	var result *loomv1.WorkflowResult
	var err error

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate:
		result, err = e.debater.Execute(ctx, p.Debate)
	case *loomv1.WorkflowPattern_Swarm:
		result, err = e.swarm.Execute(ctx, p.Swarm)
	case *loomv1.WorkflowPattern_PairProgramming:
		result, err = e.pairProgrammer.Execute(ctx, p.PairProgramming)
	case *loomv1.WorkflowPattern_TeacherStudent:
		result, err = e.teacherStudent.Execute(ctx, p.TeacherStudent)
	default:
		return nil, fmt.Errorf("unsupported collaboration pattern: %T", p)
	}

	if err != nil {
		e.logger.Error("Collaboration execution failed",
			zap.String("pattern_type", patternType),
			zap.Error(err))
		return nil, fmt.Errorf("collaboration execution failed: %w", err)
	}

	// Calculate total duration
	result.DurationMs = time.Since(startTime).Milliseconds()

	e.logger.Info("Collaboration completed",
		zap.String("pattern_type", patternType),
		zap.Int64("duration_ms", result.DurationMs),
		zap.Float64("total_cost_usd", result.Cost.TotalCostUsd))

	return result, nil
}

// getCollaborationPatternType returns a string representation of the collaboration pattern type.
func getCollaborationPatternType(pattern *loomv1.WorkflowPattern) string {
	switch pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Debate:
		return "debate"
	case *loomv1.WorkflowPattern_Swarm:
		return "swarm"
	case *loomv1.WorkflowPattern_PairProgramming:
		return "pair_programming"
	case *loomv1.WorkflowPattern_TeacherStudent:
		return "teacher_student"
	default:
		return "unknown"
	}
}
