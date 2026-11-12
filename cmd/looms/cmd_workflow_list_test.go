// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stored workflow the loader rejects stays in the listing with the
// loader's message; before, it vanished and the operator learned why only
// from `looms workflow run`.
func TestScanWorkflowFiles_ListsLoaderRejectedFilesWithTheError(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("good.yaml", `apiVersion: loom/v1
kind: Workflow
metadata:
  name: good
  description: a pipeline
spec:
  type: pipeline
  initial_prompt: go
  stages:
    - agent_id: a
      prompt_template: "Do: {{previous}}"
`)
	write("stale-debate.yaml", `apiVersion: loom/v1
kind: Workflow
metadata:
  name: stale
spec:
  type: debate
  topic: t
  agent_ids: [a, b]
  rounds: 1
  merge_strategy: synthesis
`)
	write("agent.yaml", `apiVersion: loom/v1
kind: Agent
metadata:
  name: not-a-workflow
spec:
  system_prompt: hi
`)
	write("notes.txt", "not yaml")

	workflows, err := scanWorkflowFiles(dir)
	require.NoError(t, err)
	require.Len(t, workflows, 2, "agent configs and non-YAML files are not workflows")

	byPath := map[string]workflowInfo{}
	for _, wf := range workflows {
		byPath[wf.Path] = wf
	}
	good := byPath["good.yaml"]
	assert.True(t, good.IsValid)
	assert.Equal(t, "pipeline", good.Type)
	assert.Equal(t, "good", good.Name)
	assert.Empty(t, good.Error)

	stale := byPath["stale-debate.yaml"]
	assert.False(t, stale.IsValid)
	assert.Equal(t, "stale", stale.Name)
	assert.Contains(t, stale.Error, "merge_strategy")

	out := formatWorkflowList(workflows)
	assert.Contains(t, out, "2 workflow file(s), 1 valid, 1 the loader rejects")
	assert.Contains(t, out, "stale-debate.yaml (invalid)")
	assert.Contains(t, out, "Error: ")
	assert.Contains(t, out, "merge_strategy")
	assert.Contains(t, out, "Type: pipeline")
}

func TestFormatWorkflowList_AllValid(t *testing.T) {
	out := formatWorkflowList([]workflowInfo{{Path: "a.yaml", Type: "pipeline", IsValid: true}})
	assert.Contains(t, out, "Found 1 workflow(s):")
	assert.NotContains(t, out, "invalid")
}

// A workflow file whose YAML does not parse is still a stored workflow: it
// is listed with the parse error. A broken agent config in the same tree is
// not a workflow and stays out of the listing.
func TestScanWorkflowFiles_ListsSyntaxBrokenWorkflowFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("broken.yaml", "apiVersion: loom/v1\nkind: Workflow\nmetadata:\n  name: broken\nspec:\n  type: pipeline\n  initial_prompt: go\n  stages:\n    - agent_id: a\n   prompt_template: \"bad indent\"\n")
	write("broken-agent.yaml", "apiVersion: loom/v1\nkind: Agent\nmetadata:\n  name: x\nspec:\n  system_prompt: hi\n tools: [\n")

	workflows, err := scanWorkflowFiles(dir)
	require.NoError(t, err)
	require.Len(t, workflows, 1, "the syntax-broken workflow is listed, the broken agent config is not: %+v", workflows)
	assert.Equal(t, "broken.yaml", workflows[0].Path)
	assert.False(t, workflows[0].IsValid)
	assert.Contains(t, workflows[0].Error, "yaml parse")
	assert.Contains(t, formatWorkflowList(workflows), "broken.yaml (invalid)")
}
