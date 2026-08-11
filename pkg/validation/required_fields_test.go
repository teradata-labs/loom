// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrchestrationRequiredFieldsMatchConverter pins the validator's
// per-pattern required-field checks to what the canonical converter
// (pkg/orchestration convert*Pattern) actually rejects. Before these checks,
// a swarm spec without 'question' validated cleanly but failed at
// LoadWorkflowFromYAML — the required-field flavor of the vocabulary drift
// fixed by the canonical pattern-type contract.
func TestOrchestrationRequiredFieldsMatchConverter(t *testing.T) {
	tests := []struct {
		name      string
		spec      string
		wantField string
	}{
		{
			name: "swarm missing question",
			spec: `  type: swarm
  agent_ids:
    - voter-a
    - voter-b
`,
			wantField: "spec.question",
		},
		{
			name: "swarm missing agent_ids",
			spec: `  type: swarm
  question: '{{input}}'
`,
			wantField: "spec.agent_ids",
		},
		{
			name: "conditional missing condition_agent_id",
			spec: `  type: conditional
  condition_prompt: 'Classify: {{input}}'
  branches:
    ok:
      type: fork-join
      prompt: 'Handle: {{input}}'
      agent_ids:
        - handler
`,
			wantField: "spec.condition_agent_id",
		},
		{
			name: "conditional missing condition_prompt",
			spec: `  type: conditional
  condition_agent_id: classifier
  branches:
    ok:
      type: fork-join
      prompt: 'Handle: {{input}}'
      agent_ids:
        - handler
`,
			wantField: "spec.condition_prompt",
		},
		{
			name: "conditional missing branches",
			spec: `  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Classify: {{input}}'
`,
			wantField: "spec.branches",
		},
		{
			name: "iterative missing pipeline",
			spec: `  type: iterative
  max_iterations: 2
`,
			wantField: "spec.pipeline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: required-fields
spec:
` + tt.spec

			result := ValidateYAMLContent(workflowYAML, "required-fields.yaml")
			require.False(t, result.Valid, "expected validation to fail")

			found := false
			for _, e := range result.Errors {
				if e.Field == tt.wantField {
					found = true
					break
				}
			}
			assert.True(t, found, "expected an error on %s, got: %+v", tt.wantField, result.Errors)
		})
	}
}
