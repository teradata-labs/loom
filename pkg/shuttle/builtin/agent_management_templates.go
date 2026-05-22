// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/templates"
)

// This file extends pkg/shuttle/builtin/agent_management.go with four
// preset / template actions that mirror the Loom Cloud
// agent_management.{presets, apply_preset, templates, apply_template}
// surface. The actions write k8s-style YAML files into the loom data
// directory's agents/ + workflows/ subtrees — the OSS storage pattern
// the existing executeCreateAgent / executeCreateWorkflow paths use.
//
// All four actions reuse the same security gating as the parent tool:
// weaver may write, guide is read-only (only "presets" and "templates"
// pass the guide check; "apply_preset" / "apply_template" are blocked at
// the dispatch site in Execute).

// executePresets returns the preset registry formatted for the weaver.
// Read-only — no file mutation. Available to both weaver and guide.
func (t *AgentManagementTool) executePresets(start time.Time) (*shuttle.Result, error) {
	out := make([]map[string]interface{}, 0, len(templates.ListPresets()))
	for _, p := range templates.ListPresets() {
		entry := map[string]interface{}{
			"preset":       templates.PresetEnumToString(p.Preset),
			"display_name": p.DisplayName,
			"description":  p.Description,
			"icon":         p.Icon,
		}
		if p.Defaults != nil {
			entry["tools"] = p.Defaults.Tools
			entry["max_turns"] = p.Defaults.MaxTurns
			entry["max_tool_executions"] = p.Defaults.MaxToolExecutions
			entry["temperature"] = p.Defaults.Temperature
			entry["thinking_level"] = p.Defaults.ThinkingLevel
			entry["workload_profile"] = p.Defaults.WorkloadProfile
		}
		out = append(out, entry)
	}
	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":  "presets",
			"presets": out,
			"hint":    "Use apply_preset with preset=<name>, name=<agent-name>, system_prompt=<prompt> to scaffold an agent.",
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeTemplates returns the workflow template registry formatted for
// the weaver. Read-only — no file mutation.
func (t *AgentManagementTool) executeTemplates(start time.Time) (*shuttle.Result, error) {
	out := make([]map[string]interface{}, 0, len(templates.ListWorkflowTemplates()))
	for _, tmpl := range templates.ListWorkflowTemplates() {
		agents := make([]map[string]interface{}, 0, len(tmpl.Agents))
		for _, a := range tmpl.Agents {
			agents = append(agents, map[string]interface{}{
				"preset":       templates.PresetEnumToString(a.Preset),
				"default_name": a.DefaultName,
				"description":  a.Description,
			})
		}
		entry := map[string]interface{}{
			"template":     templates.WorkflowTemplateEnumToString(tmpl.Template),
			"display_name": tmpl.DisplayName,
			"description":  tmpl.Description,
			"icon":         tmpl.Icon,
			"category":     tmpl.Category,
			"pattern_type": templatePatternType(tmpl.DefaultWorkflowPattern),
			"agents":       agents,
			"schedulable":  tmpl.Schedulable,
		}
		if tmpl.Schedulable {
			entry["suggested_cron"] = tmpl.SuggestedCron
			entry["suggested_timezone"] = tmpl.SuggestedTimezone
		}
		out = append(out, entry)
	}
	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":    "templates",
			"templates": out,
			"hint":      "Use apply_template with name=<template-name>, workflow_name=<your-name> to scaffold agents + a workflow in one step.",
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeApplyPreset scaffolds a single agent from a preset. The user
// must supply name + system_prompt; the preset fills the rest (tools,
// limits, temperature, thinking, memory). Writes one agent YAML.
func (t *AgentManagementTool) executeApplyPreset(params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	presetName, _ := params["preset"].(string)
	if presetName == "" {
		return errInvalidParam("preset is required for apply_preset (e.g. personal_assistant, research_analyst)", start), nil
	}
	enum := templates.PresetEnumFromString(presetName)
	preset := templates.GetPreset(enum)
	if preset == nil {
		return errInvalidParam(fmt.Sprintf("unknown preset %q — valid: %s", presetName, knownPresetNames()), start), nil
	}

	name, _ := params["name"].(string)
	if name == "" {
		return errInvalidParam("name is required for apply_preset", start), nil
	}
	systemPrompt, _ := params["system_prompt"].(string)
	if systemPrompt == "" {
		return errInvalidParam("system_prompt is required for apply_preset", start), nil
	}
	activeProvider, _ := params["active_provider"].(string)

	yamlContent, err := buildAgentYAMLFromPreset(name, systemPrompt, activeProvider, preset, 0)
	if err != nil {
		return errInternal(fmt.Sprintf("build agent YAML: %v", err), start), nil
	}

	res, werr := t.writeAgentFile(name, yamlContent, false, start)
	if werr != nil {
		return res, werr
	}
	if res != nil && res.Success {
		// Annotate the success data with the preset metadata for the LLM's
		// audit log — useful when the weaver later wants to recall which
		// preset it applied without re-reading the YAML.
		if data, ok := res.Data.(map[string]interface{}); ok {
			data["preset"] = presetName
			data["preset_display_name"] = preset.DisplayName
		}
	}
	return res, nil
}

// executeApplyTemplate scaffolds N agents + 1 workflow from a template.
// Reuses any agent that already exists by name (skip-not-fail) so a
// re-run after partial success picks up where it left off. Writes N+1
// YAML files when nothing pre-exists.
func (t *AgentManagementTool) executeApplyTemplate(params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	templateName, _ := params["name"].(string)
	if templateName == "" {
		return errInvalidParam("name is required for apply_template (the template id, e.g. research-report)", start), nil
	}
	enum := templates.WorkflowTemplateEnumFromString(templateName)
	tmpl := templates.GetWorkflowTemplate(enum)
	if tmpl == nil {
		return errInvalidParam(fmt.Sprintf("unknown template %q — valid: %s", templateName, knownTemplateNames()), start), nil
	}

	workflowName, _ := params["workflow_name"].(string)
	if workflowName == "" {
		workflowName = templateName // sensible default — the template id doubles as the workflow id
	}
	activeProvider, _ := params["active_provider"].(string)

	// Optional name overrides — map of index → custom name. The weaver may
	// pass these as a map[string]interface{} (JSON object); coerce here.
	nameOverrides := map[int32]string{}
	if rawOverrides, ok := params["agent_name_overrides"].(map[string]interface{}); ok {
		for k, v := range rawOverrides {
			if s, ok := v.(string); ok {
				if idx, perr := parseInt32(k); perr == nil {
					nameOverrides[idx] = s
				}
			}
		}
	}

	created := []string{}
	reused := []string{}
	agentsDir := config.GetLoomSubDir("agents")

	for i, spec := range tmpl.Agents {
		preset := templates.GetPreset(spec.Preset)
		if preset == nil {
			return errInternal(fmt.Sprintf("template references unknown preset %v for slot %d", spec.Preset, i), start), nil
		}

		agentName := spec.DefaultName
		if override, ok := nameOverrides[int32(i)]; ok && override != "" {
			agentName = override
		}

		// Reuse-by-name: if a YAML already exists, skip creation.
		if existsAgentFile(agentsDir, agentName) {
			reused = append(reused, agentName)
			continue
		}

		yamlContent, err := buildAgentYAMLFromPreset(agentName, spec.SystemPrompt, activeProvider, preset, spec.TemperatureOverride)
		if err != nil {
			return errInternal(fmt.Sprintf("build agent YAML for slot %d (%q): %v", i, agentName, err), start), nil
		}
		// We deliberately call the file-write path *without* using its
		// success result — the parent handler aggregates its own summary
		// across all slots.
		res, werr := t.writeAgentFile(agentName, yamlContent, false, start)
		if werr != nil {
			return res, werr
		}
		if res == nil || !res.Success {
			return res, nil
		}
		created = append(created, agentName)
	}

	// Assemble + write the workflow YAML.
	resolvedNames := make([]string, len(tmpl.Agents))
	for i, spec := range tmpl.Agents {
		name := spec.DefaultName
		if override, ok := nameOverrides[int32(i)]; ok && override != "" {
			name = override
		}
		resolvedNames[i] = name
	}
	wfYAML, err := buildWorkflowYAMLFromTemplate(workflowName, tmpl, resolvedNames)
	if err != nil {
		return errInternal(fmt.Sprintf("build workflow YAML: %v", err), start), nil
	}
	if wfRes, werr := t.writeWorkflowFile(workflowName, wfYAML, false, start); werr != nil {
		return wfRes, werr
	} else if wfRes == nil || !wfRes.Success {
		return wfRes, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":         "apply_template",
			"template":       templateName,
			"workflow_name":  workflowName,
			"agents_created": created,
			"agents_reused":  reused,
			"agents_total":   len(tmpl.Agents),
			"workflow_file":  workflowName + ".yaml",
			"hint":           "Agents and workflow have been written to disk. The server will hot-reload them; you can also start the workflow via ExecuteWorkflow.",
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// =============================================================================
// YAML builders
// =============================================================================

// buildAgentYAMLFromPreset serialises an AgentConfig-equivalent k8s-style
// YAML document from a preset + caller-supplied name/prompt. The shape
// matches what cmd/looms/cmd_serve.go's boot loader and the
// agent.LoadAgentConfig parser expect. Empty fields are omitted to keep
// the file readable.
func buildAgentYAMLFromPreset(name, systemPrompt, activeProvider string, preset *loomv1.AgentPresetInfo, temperatureOverride float32) (string, error) {
	if preset == nil || preset.Defaults == nil {
		return "", fmt.Errorf("preset %v has no defaults", preset)
	}
	d := preset.Defaults

	temperature := d.Temperature
	if temperatureOverride != 0 {
		temperature = temperatureOverride
	}

	spec := map[string]interface{}{
		"system_prompt": systemPrompt,
	}
	if activeProvider != "" {
		spec["active_provider"] = activeProvider
	}
	if d.Rom != "" {
		spec["rom"] = d.Rom
	}
	if d.WorkloadProfile != "" {
		spec["workload_profile"] = strings.ToLower(d.WorkloadProfile)
	}
	if d.ThinkingLevel != "" {
		spec["thinking_level"] = d.ThinkingLevel
	}

	llm := map[string]interface{}{
		"temperature": temperature,
	}
	if d.MaxTokens > 0 {
		llm["max_tokens"] = d.MaxTokens
	}
	if d.MaxContextTokens > 0 {
		llm["max_context_tokens"] = d.MaxContextTokens
	}
	if d.ReservedOutputTokens > 0 {
		llm["reserved_output_tokens"] = d.ReservedOutputTokens
	}
	spec["llm"] = llm

	if len(d.Tools) > 0 {
		spec["tools"] = append([]string{}, d.Tools...)
	}

	behavior := map[string]interface{}{}
	if d.MaxTurns > 0 {
		behavior["max_turns"] = d.MaxTurns
	}
	if d.MaxToolExecutions > 0 {
		behavior["max_tool_executions"] = d.MaxToolExecutions
	}
	if d.MaxIterations > 0 {
		behavior["max_iterations"] = d.MaxIterations
	}
	if d.TimeoutSeconds > 0 {
		behavior["timeout_seconds"] = d.TimeoutSeconds
	}
	if d.OutputTokenCbThreshold != 0 {
		behavior["output_token_cb_threshold"] = d.OutputTokenCbThreshold
	}
	if len(behavior) > 0 {
		spec["config"] = behavior
	}

	// Graph memory + cache control belong in memory block.
	mem := map[string]interface{}{
		"type":        "memory",
		"max_history": int32(100),
	}
	if d.GraphMemoryEnabled != nil {
		gm := map[string]interface{}{"enabled": *d.GraphMemoryEnabled}
		if d.GraphMemoryBudgetPercent > 0 {
			gm["context_budget_percent"] = d.GraphMemoryBudgetPercent
		}
		if d.GraphMemoryMaxCandidates > 0 {
			gm["max_recall_candidates"] = d.GraphMemoryMaxCandidates
		}
		mem["graph_memory"] = gm
	}
	spec["memory"] = mem

	doc := map[string]interface{}{
		"apiVersion": "loom/v1",
		"kind":       "Agent",
		"metadata": map[string]interface{}{
			"name":        name,
			"version":     "1.0.0",
			"description": preset.Description,
			"labels": map[string]interface{}{
				"preset": templates.PresetEnumToString(preset.Preset),
				"source": "weaver-apply-preset",
			},
		},
		"spec": spec,
	}

	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildWorkflowYAMLFromTemplate serialises a workflow YAML referencing the
// given agents by name. The emitted shape matches the OSS workflow loader's
// expectations (validator-tested in pkg/validation/validator.go):
//
//   - spec.type uses hyphenated pattern names (e.g. "fork-join", not "fork_join")
//   - Pattern fields land directly under spec (stages / agent_ids / topic /
//     prompt / etc.), not nested under a per-pattern key
//   - Pipeline stages carry agent_id (which accepts a registered agent name
//     OR a uuid) plus a prompt_template
//
// Pattern variants without a registry-shipped template (parallel,
// conditional, iterative, pair_programming, teacher_student) return an
// error — extending here would require corresponding template entries
// first.
func buildWorkflowYAMLFromTemplate(workflowName string, tmpl *loomv1.WorkflowTemplateInfo, agentNames []string) (string, error) {
	if tmpl == nil || tmpl.DefaultWorkflowPattern == nil {
		return "", fmt.Errorf("template has no default_workflow_pattern")
	}

	spec := map[string]interface{}{}

	// Pattern-specific config flattened directly under spec.
	switch p := tmpl.DefaultWorkflowPattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		spec["type"] = "pipeline"
		// OSS pipelines require initial_prompt at YAML load time. The
		// template doesn't know the user's specific input ahead of time,
		// so we emit the literal "{{input}}" placeholder — the
		// orchestrator's interpolation pass (pkg/orchestration/interpolation.go)
		// substitutes it from the variables map at ExecuteWorkflow time.
		spec["initial_prompt"] = "{{input}}"
		stages := make([]map[string]interface{}, 0, len(p.Pipeline.Stages))
		for i, s := range p.Pipeline.Stages {
			stages = append(stages, map[string]interface{}{
				"agent_id":        agentNames[i],
				"prompt_template": s.PromptTemplate,
			})
		}
		spec["stages"] = stages
		if p.Pipeline.PassFullHistory {
			spec["pass_full_history"] = true
		}
	case *loomv1.WorkflowPattern_ForkJoin:
		spec["type"] = "fork-join"
		spec["agent_ids"] = append([]string{}, agentNames...)
		spec["prompt"] = p.ForkJoin.Prompt
		spec["merge_strategy"] = mergeStrategyName(p.ForkJoin.MergeStrategy)
		if p.ForkJoin.TimeoutSeconds > 0 {
			spec["timeout_seconds"] = p.ForkJoin.TimeoutSeconds
		}
	case *loomv1.WorkflowPattern_Debate:
		spec["type"] = "debate"
		spec["agent_ids"] = append([]string{}, agentNames...)
		spec["topic"] = p.Debate.Topic
		spec["rounds"] = p.Debate.Rounds
		spec["merge_strategy"] = mergeStrategyName(p.Debate.MergeStrategy)
	case *loomv1.WorkflowPattern_Swarm:
		spec["type"] = "swarm"
		spec["agent_ids"] = append([]string{}, agentNames...)
		spec["question"] = p.Swarm.Question
	default:
		return "", fmt.Errorf("unsupported pattern variant for YAML emission: %T", p)
	}

	if tmpl.Schedulable && tmpl.SuggestedCron != "" {
		spec["schedule"] = map[string]interface{}{
			"cron":     tmpl.SuggestedCron,
			"timezone": tmpl.SuggestedTimezone,
			"enabled":  false, // suggested, not active — operator must opt in
		}
	}

	doc := map[string]interface{}{
		"apiVersion": "loom/v1",
		"kind":       "Workflow",
		"metadata": map[string]interface{}{
			"name":        workflowName,
			"version":     "1.0.0",
			"description": tmpl.Description,
			"labels": map[string]interface{}{
				"template": templates.WorkflowTemplateEnumToString(tmpl.Template),
				"source":   "weaver-apply-template",
			},
		},
		"spec": spec,
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// =============================================================================
// helpers
// =============================================================================

func errInvalidParam(msg string, start time.Time) *shuttle.Result {
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "INVALID_PARAMS",
			Message: msg,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}
}

func errInternal(msg string, start time.Time) *shuttle.Result {
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "INTERNAL_ERROR",
			Message: msg,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}
}

func parseInt32(s string) (int32, error) {
	var n int32
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func existsAgentFile(dir, agentName string) bool {
	for _, suffix := range []string{".yaml", ".yml"} {
		if _, err := os.Stat(filepath.Join(dir, agentName+suffix)); err == nil {
			return true
		}
	}
	return false
}

func knownPresetNames() string {
	all := templates.ListPresets()
	out := make([]string, 0, len(all))
	for _, p := range all {
		out = append(out, templates.PresetEnumToString(p.Preset))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func knownTemplateNames() string {
	all := templates.ListWorkflowTemplates()
	out := make([]string, 0, len(all))
	for _, t := range all {
		out = append(out, templates.WorkflowTemplateEnumToString(t.Template))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func templatePatternType(p *loomv1.WorkflowPattern) string {
	if p == nil {
		return ""
	}
	switch p.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		return "pipeline"
	case *loomv1.WorkflowPattern_ForkJoin:
		return "fork_join"
	case *loomv1.WorkflowPattern_Debate:
		return "debate"
	case *loomv1.WorkflowPattern_Swarm:
		return "swarm"
	case *loomv1.WorkflowPattern_Parallel:
		return "parallel"
	case *loomv1.WorkflowPattern_Conditional:
		return "conditional"
	case *loomv1.WorkflowPattern_PairProgramming:
		return "pair_programming"
	case *loomv1.WorkflowPattern_TeacherStudent:
		return "teacher_student"
	case *loomv1.WorkflowPattern_Iterative:
		return "iterative"
	default:
		return ""
	}
}

func mergeStrategyName(m loomv1.MergeStrategy) string {
	switch m {
	case loomv1.MergeStrategy_CONSENSUS:
		return "consensus"
	case loomv1.MergeStrategy_VOTING:
		return "voting"
	case loomv1.MergeStrategy_CONCATENATE:
		return "concatenate"
	case loomv1.MergeStrategy_FIRST:
		return "first"
	case loomv1.MergeStrategy_BEST:
		return "best"
	case loomv1.MergeStrategy_SUMMARY:
		return "summary"
	default:
		return ""
	}
}
