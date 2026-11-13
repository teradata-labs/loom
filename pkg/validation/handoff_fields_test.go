// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The structure validator must accept the join and history blocks the
// loader parses, and point a debate's dead merge_strategy at its replacement.
func TestValidateOrchestrationSchema_JoinAndHistoryFields(t *testing.T) {
	accepted := map[string]string{
		"fork-join join": `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: fj
spec:
  type: fork-join
  prompt: "Analyze"
  agent_ids: [a, b]
  join:
    agent_id: presenter
`,
		"parallel join": `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: par
spec:
  type: parallel
  tasks:
    - agent_id: a
      prompt: "x"
  join:
    agent_id: editor
`,
		"pipeline history": `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: pipe
spec:
  type: pipeline
  initial_prompt: "go"
  stages:
    - agent_id: a
      prompt_template: "A"
    - agent_id: b
      prompt_template: "{{history}}"
  history:
    max_input_tokens: 1000
`,
		"debate join": `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: deb
spec:
  type: debate
  topic: "t"
  agent_ids: [a, b]
  moderator_agent_id: m
  join:
    max_branch_bytes: 1024
`,
	}
	for name, yaml := range accepted {
		t.Run(name, func(t *testing.T) {
			result := ValidateYAMLContent(yaml, "")
			for _, e := range result.Errors {
				assert.NotContains(t, e.Message, "Unexpected field", "%s: %+v", name, e)
				assert.NotContains(t, e.Message, "Invalid field", "%s: %+v", name, e)
			}
		})
	}

	result := ValidateYAMLContent(`
apiVersion: loom/v1
kind: Workflow
metadata:
  name: deb
spec:
  type: debate
  topic: "t"
  agent_ids: [a, b]
  merge_strategy: consensus
`, "")
	assert.False(t, result.Valid)
	found := false
	for _, e := range result.Errors {
		if e.Field == "spec.merge_strategy" && strings.Contains(e.Fix, "Debate ignores 'merge_strategy'") {
			found = true
		}
	}
	assert.True(t, found, "debate merge_strategy is flagged with the replacement: %+v", result.Errors)
}
