// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/templates"
)

// ListAgentPresets returns every registered single-agent preset. Pure
// metadata RPC — no state mutation, no auth required beyond the standard
// LoomService gating.
func (s *MultiAgentServer) ListAgentPresets(_ context.Context, _ *loomv1.ListAgentPresetsRequest) (*loomv1.ListAgentPresetsResponse, error) {
	return &loomv1.ListAgentPresetsResponse{
		Presets: templates.ListPresets(),
	}, nil
}

// ListWorkflowTemplates returns every registered workflow template. Pure
// metadata RPC.
func (s *MultiAgentServer) ListWorkflowTemplates(_ context.Context, _ *loomv1.ListWorkflowTemplatesRequest) (*loomv1.ListWorkflowTemplatesResponse, error) {
	return &loomv1.ListWorkflowTemplatesResponse{
		Templates: templates.ListWorkflowTemplates(),
	}, nil
}

// CreateWorkflowFromTemplate instantiates a workflow template:
//  1. Walk template.Agents. For each spec, look up an existing agent with
//     the resolved name; create one if missing using the preset defaults
//     merged with the spec's curated system prompt.
//  2. Materialize the template's WorkflowPattern by slotting in the
//     resolved agent ids on every stage / participant.
//  3. Return the names, ids, reuse-flags, and materialized pattern. The
//     caller is responsible for invoking ExecuteWorkflow / StreamWorkflow
//     / ScheduleWorkflow with the returned pattern.
//
// Idempotent by agent name: re-running with the same template after some
// agents already exist reuses them rather than failing on duplicate-name.
// This matches the Loom Cloud apply_template contract.
func (s *MultiAgentServer) CreateWorkflowFromTemplate(ctx context.Context, req *loomv1.CreateWorkflowFromTemplateRequest) (*loomv1.CreateWorkflowFromTemplateResponse, error) {
	if req.Template == loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "template is required")
	}
	if req.WorkflowName == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_name is required")
	}
	tmpl := templates.GetWorkflowTemplate(req.Template)
	if tmpl == nil {
		return nil, status.Errorf(codes.NotFound, "unknown workflow template: %v", req.Template)
	}

	// Phase 1 — resolve every agent, creating as needed.
	agentNames := make([]string, len(tmpl.Agents))
	agentIDs := make([]string, len(tmpl.Agents))
	reused := make([]bool, len(tmpl.Agents))

	for i, spec := range tmpl.Agents {
		name := spec.DefaultName
		if override, ok := req.AgentNameOverrides[int32(i)]; ok && override != "" {
			name = override
		}
		if name == "" {
			return nil, status.Errorf(codes.InvalidArgument,
				"template agent %d has no default name and no override", i)
		}
		agentNames[i] = name

		// Reuse path: any agent already known to the server with this name.
		if existingID, ok := s.lookupAgentIDByName(name); ok {
			agentIDs[i] = existingID
			reused[i] = true
			continue
		}

		// Create path: synthesise an AgentConfig from the preset + spec.
		cfg, err := agentConfigFromTemplateSpec(name, spec, req.ActiveProvider)
		if err != nil {
			return nil, status.Errorf(codes.Internal,
				"build agent config for %q: %v", name, err)
		}

		info, err := s.CreateAgentFromConfig(ctx, &loomv1.CreateAgentRequest{Config: cfg})
		if err != nil {
			return nil, status.Errorf(codes.Internal,
				"create agent %q from template: %v", name, err)
		}
		agentIDs[i] = info.Id
		reused[i] = false

		if s.logger != nil {
			s.logger.Info("template: agent created",
				zap.String("template", templates.WorkflowTemplateEnumToString(req.Template)),
				zap.String("agent_name", name),
				zap.String("agent_id", info.Id),
				zap.String("preset", templates.PresetEnumToString(spec.Preset)))
		}
	}

	// Phase 2 — materialize the workflow pattern with the resolved agent ids.
	materialized, err := materializeTemplatePattern(tmpl.DefaultWorkflowPattern, agentIDs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "materialize pattern: %v", err)
	}
	materialized.WorkflowId = req.WorkflowName

	if s.logger != nil {
		s.logger.Info("template: workflow materialized",
			zap.String("template", templates.WorkflowTemplateEnumToString(req.Template)),
			zap.String("workflow_name", req.WorkflowName),
			zap.Int("agents_total", len(agentIDs)),
			zap.Int("agents_reused", countTrue(reused)))
	}

	return &loomv1.CreateWorkflowFromTemplateResponse{
		AgentNames:      agentNames,
		AgentIds:        agentIDs,
		Reused:          reused,
		WorkflowName:    req.WorkflowName,
		WorkflowPattern: materialized,
	}, nil
}

// lookupAgentIDByName scans the running-agents map for a matching name.
// Returns the GUID + ok=true on hit. Read-locks the registry mutex so
// concurrent CreateAgent calls don't race.
func (s *MultiAgentServer) lookupAgentIDByName(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, ag := range s.agents {
		if ag.GetName() == name {
			return id, true
		}
	}
	return "", false
}

// agentConfigFromTemplateSpec turns a WorkflowTemplateAgentSpec + preset
// into a CreateAgentRequest-ready AgentConfig. Mirrors the cloud
// apply_template+apply_preset flow: preset defaults seed the agent, the
// template's curated system_prompt overrides the (empty) preset prompt,
// and optional temperature overrides win where present.
func agentConfigFromTemplateSpec(name string, spec *loomv1.WorkflowTemplateAgentSpec, activeProvider string) (*loomv1.AgentConfig, error) {
	preset := templates.GetPreset(spec.Preset)
	if preset == nil || preset.Defaults == nil {
		return nil, fmt.Errorf("unknown preset %v for agent %q", spec.Preset, name)
	}
	d := preset.Defaults

	tools := &loomv1.ToolsConfig{
		Builtin: append([]string{}, d.Tools...),
	}

	temperature := d.Temperature
	if spec.TemperatureOverride != 0 {
		temperature = spec.TemperatureOverride
	}

	cfg := &loomv1.AgentConfig{
		Name:           name,
		Description:    spec.Description,
		SystemPrompt:   spec.SystemPrompt,
		ActiveProvider: activeProvider,
		Llm: &loomv1.LLMConfig{
			Temperature:          temperature,
			MaxTokens:            d.MaxTokens,
			MaxContextTokens:     d.MaxContextTokens,
			ReservedOutputTokens: d.ReservedOutputTokens,
		},
		Tools: tools,
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:     d.MaxIterations,
			MaxTurns:          d.MaxTurns,
			MaxToolExecutions: d.MaxToolExecutions,
			TimeoutSeconds:    d.TimeoutSeconds,
		},
		Memory: &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 100,
		},
		Rom: d.Rom,
	}
	return cfg, nil
}

// materializeTemplatePattern returns a deep copy of the template's
// WorkflowPattern with agent ids slotted into every position that expects
// one. Returns an error if the pattern variant is unsupported (the
// template registry should never produce an unsupported variant — the
// error is defensive for future template additions).
func materializeTemplatePattern(pattern *loomv1.WorkflowPattern, agentIDs []string) (*loomv1.WorkflowPattern, error) {
	if pattern == nil {
		return nil, fmt.Errorf("template default_workflow_pattern is nil")
	}
	// Clone first so we never mutate the registry entry shared across calls.
	clone, ok := proto.Clone(pattern).(*loomv1.WorkflowPattern)
	if !ok || clone == nil {
		return nil, fmt.Errorf("clone workflow pattern: type assertion failed")
	}
	switch p := clone.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		if p.Pipeline == nil {
			return nil, fmt.Errorf("pipeline pattern has nil Pipeline")
		}
		if len(p.Pipeline.Stages) != len(agentIDs) {
			return nil, fmt.Errorf("pipeline expects %d stages but template has %d agents",
				len(p.Pipeline.Stages), len(agentIDs))
		}
		for i, stage := range p.Pipeline.Stages {
			stage.AgentId = agentIDs[i]
		}
	case *loomv1.WorkflowPattern_ForkJoin:
		if p.ForkJoin == nil {
			return nil, fmt.Errorf("fork_join pattern has nil ForkJoin")
		}
		p.ForkJoin.AgentIds = append([]string{}, agentIDs...)
	case *loomv1.WorkflowPattern_Debate:
		if p.Debate == nil {
			return nil, fmt.Errorf("debate pattern has nil Debate")
		}
		p.Debate.AgentIds = append([]string{}, agentIDs...)
	case *loomv1.WorkflowPattern_Swarm:
		if p.Swarm == nil {
			return nil, fmt.Errorf("swarm pattern has nil Swarm")
		}
		p.Swarm.AgentIds = append([]string{}, agentIDs...)
	// ParallelPattern carries per-task agent assignments via its
	// AgentTask slice rather than a flat agent_ids list. The shipped
	// templates don't use Parallel; if a future template adds one, the
	// materializer needs custom AgentTask construction logic here.
	default:
		return nil, fmt.Errorf("template pattern variant %T not yet materializable", p)
	}
	return clone, nil
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}
