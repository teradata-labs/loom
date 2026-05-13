// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/teradata-labs/loom/pkg/session"
)

// TestAgentManagementTool_Presets exercises the read-only listing path.
// Verifies (a) it works for both weaver and guide (read-only path), and
// (b) the response carries every preset's name + tools list so the LLM
// has enough to choose without a second roundtrip.
func TestAgentManagementTool_Presets(t *testing.T) {
	tool := NewAgentManagementTool()
	for _, agentID := range []string{"weaver", "guide"} {
		t.Run(agentID, func(t *testing.T) {
			ctx := session.WithAgentID(context.Background(), agentID)
			res, err := tool.Execute(ctx, map[string]interface{}{"action": "presets"})
			require.NoError(t, err)
			require.NotNil(t, res)
			require.True(t, res.Success, "presets action must succeed for %s; error: %+v", agentID, res.Error)
			data, ok := res.Data.(map[string]interface{})
			require.True(t, ok, "data must be a map[string]interface{}")
			presets, ok := data["presets"].([]map[string]interface{})
			require.True(t, ok, "presets key must carry a slice of maps")
			require.Greater(t, len(presets), 0, "must return at least one preset")

			// Spot-check personal_assistant — it should always be in the list.
			var found map[string]interface{}
			for _, p := range presets {
				if p["preset"] == "personal_assistant" {
					found = p
					break
				}
			}
			require.NotNil(t, found, "personal_assistant must be in the listing")
			assert.NotEmpty(t, found["display_name"])
			tools, _ := found["tools"].([]string)
			assert.NotEmpty(t, tools, "personal_assistant must carry a tool list")
		})
	}
}

// TestAgentManagementTool_Templates parallels the preset listing test for
// workflow templates.
func TestAgentManagementTool_Templates(t *testing.T) {
	tool := NewAgentManagementTool()
	for _, agentID := range []string{"weaver", "guide"} {
		t.Run(agentID, func(t *testing.T) {
			ctx := session.WithAgentID(context.Background(), agentID)
			res, err := tool.Execute(ctx, map[string]interface{}{"action": "templates"})
			require.NoError(t, err)
			require.True(t, res.Success, "templates action must succeed for %s", agentID)
			data, ok := res.Data.(map[string]interface{})
			require.True(t, ok)
			templates, ok := data["templates"].([]map[string]interface{})
			require.True(t, ok)
			require.Greater(t, len(templates), 0)
		})
	}
}

// TestAgentManagementTool_ApplyPreset_WritesYAML covers the happy path:
// scaffold a personal_assistant from the weaver, verify the YAML file
// lands in agents/, and that key preset fields propagated.
func TestAgentManagementTool_ApplyPreset_WritesYAML(t *testing.T) {
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action":        "apply_preset",
		"preset":        "personal_assistant",
		"name":          "my-assistant",
		"system_prompt": "Help the user manage daily tasks.",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "apply_preset must succeed; error: %+v", res.Error)

	path := filepath.Join(tmpDir, "agents", "my-assistant.yaml")
	contents, ferr := os.ReadFile(path)
	require.NoError(t, ferr, "preset YAML must be written to agents/<name>.yaml")

	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal(contents, &doc))
	assert.Equal(t, "loom/v1", doc["apiVersion"])
	assert.Equal(t, "Agent", doc["kind"])
	md, _ := doc["metadata"].(map[string]interface{})
	require.NotNil(t, md)
	assert.Equal(t, "my-assistant", md["name"])
	labels, _ := md["labels"].(map[string]interface{})
	require.NotNil(t, labels)
	assert.Equal(t, "personal_assistant", labels["preset"],
		"labels.preset must record the source preset for audit")
	spec, _ := doc["spec"].(map[string]interface{})
	require.NotNil(t, spec)
	assert.Equal(t, "Help the user manage daily tasks.", spec["system_prompt"])
	tools, _ := spec["tools"].([]interface{})
	require.NotEmpty(t, tools, "preset tools must be flattened into spec.tools")
}

func TestAgentManagementTool_ApplyPreset_ValidationErrors(t *testing.T) {
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	cases := []struct {
		name   string
		params map[string]interface{}
		want   string // substring of error message
	}{
		{
			name:   "missing preset",
			params: map[string]interface{}{"action": "apply_preset", "name": "x", "system_prompt": "p"},
			want:   "preset is required",
		},
		{
			name:   "missing name",
			params: map[string]interface{}{"action": "apply_preset", "preset": "personal_assistant", "system_prompt": "p"},
			want:   "name is required",
		},
		{
			name:   "missing system_prompt",
			params: map[string]interface{}{"action": "apply_preset", "preset": "personal_assistant", "name": "x"},
			want:   "system_prompt is required",
		},
		{
			name:   "unknown preset",
			params: map[string]interface{}{"action": "apply_preset", "preset": "not-real", "name": "x", "system_prompt": "p"},
			want:   "unknown preset",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tool.Execute(ctx, tc.params)
			require.NoError(t, err)
			require.False(t, res.Success)
			require.NotNil(t, res.Error)
			assert.Contains(t, res.Error.Message, tc.want)
		})
	}
}

// TestAgentManagementTool_ApplyTemplate_WritesAgentsAndWorkflow covers the
// full template flow: 3 agents written, 1 workflow YAML written, agents
// referenced by name in the workflow. Uses research-report as a stable
// representative template.
func TestAgentManagementTool_ApplyTemplate_WritesAgentsAndWorkflow(t *testing.T) {
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	res, err := tool.Execute(ctx, map[string]interface{}{
		"action":        "apply_template",
		"name":          "research-report",
		"workflow_name": "my-research-flow",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "apply_template must succeed; error: %+v", res.Error)

	// research-report ships 3 agents — every one should exist on disk now.
	agentsDir := filepath.Join(tmpDir, "agents")
	for _, expected := range []string{
		"research-report:researcher",
		"research-report:writer",
		"research-report:dashboard",
	} {
		path := filepath.Join(agentsDir, expected+".yaml")
		_, ferr := os.Stat(path)
		assert.NoError(t, ferr, "agent %s must be written by apply_template", expected)
	}

	// Workflow file with the user's chosen name.
	wfPath := filepath.Join(tmpDir, "workflows", "my-research-flow.yaml")
	wfContents, ferr := os.ReadFile(wfPath)
	require.NoError(t, ferr, "workflow YAML must be written to workflows/<name>.yaml")

	var wf map[string]interface{}
	require.NoError(t, yaml.Unmarshal(wfContents, &wf))
	assert.Equal(t, "Workflow", wf["kind"])
	spec, _ := wf["spec"].(map[string]interface{})
	require.NotNil(t, spec)
	assert.Equal(t, "pipeline", spec["type"])
	// Pipeline stages carry per-stage agent_id (matching the OSS workflow
	// loader shape). Every template agent must show up as a stage's
	// agent_id; the template registry's invariant is that stage count
	// equals agent count.
	stages, _ := spec["stages"].([]interface{})
	require.Len(t, stages, 3, "pipeline must materialize one stage per template agent")
	seen := map[string]bool{}
	for _, s := range stages {
		stage, ok := s.(map[string]interface{})
		require.True(t, ok)
		if id, _ := stage["agent_id"].(string); id != "" {
			seen[id] = true
		}
	}
	assert.True(t, seen["research-report:researcher"], "researcher must be wired to a stage")
	assert.True(t, seen["research-report:writer"], "writer must be wired to a stage")
	assert.True(t, seen["research-report:dashboard"], "dashboard must be wired to a stage")
}

// TestAgentManagementTool_ApplyTemplate_ReusesExistingAgents verifies the
// idempotency contract: if agents from a prior run already exist, the
// handler reuses them instead of failing with FILE_EXISTS. Critical for
// rerunning a template after a partial failure.
func TestAgentManagementTool_ApplyTemplate_ReusesExistingAgents(t *testing.T) {
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// First run lays down the agents + workflow.
	res1, err := tool.Execute(ctx, map[string]interface{}{
		"action": "apply_template",
		"name":   "data-to-dashboard",
	})
	require.NoError(t, err)
	require.True(t, res1.Success)

	// Drop the workflow so the rerun can land that file without colliding;
	// keep the agents to exercise the reuse path.
	require.NoError(t, os.Remove(filepath.Join(tmpDir, "workflows", "data-to-dashboard.yaml")))

	res2, err := tool.Execute(ctx, map[string]interface{}{
		"action": "apply_template",
		"name":   "data-to-dashboard",
	})
	require.NoError(t, err)
	require.True(t, res2.Success, "rerun must succeed via agent reuse; error: %+v", res2.Error)

	data, ok := res2.Data.(map[string]interface{})
	require.True(t, ok)
	reused, _ := data["agents_reused"].([]string)
	created, _ := data["agents_created"].([]string)
	assert.Len(t, reused, 2, "every agent from data-to-dashboard must be reused on rerun")
	assert.Empty(t, created, "no new agents should be created on rerun")
}

// TestAgentManagementTool_GuideCannotApply confirms the security gate:
// guide is read-only and cannot trigger apply_preset / apply_template.
func TestAgentManagementTool_GuideCannotApply(t *testing.T) {
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "guide")
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	for _, action := range []string{"apply_preset", "apply_template"} {
		t.Run(action, func(t *testing.T) {
			res, err := tool.Execute(ctx, map[string]interface{}{
				"action":        action,
				"preset":        "personal_assistant",
				"name":          "x",
				"system_prompt": "p",
			})
			require.NoError(t, err)
			require.False(t, res.Success, "%s must be rejected for guide", action)
			require.NotNil(t, res.Error)
			assert.Equal(t, "UNAUTHORIZED", res.Error.Code)
		})
	}
}
