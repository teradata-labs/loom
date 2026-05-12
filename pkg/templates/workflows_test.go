// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestWorkflowTemplateRegistryCovers verifies every non-UNSPECIFIED enum
// value has a registry entry — same regression guard as the preset test.
func TestWorkflowTemplateRegistryCovers(t *testing.T) {
	enumValues := []loomv1.WorkflowTemplate{
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_RESEARCH_REPORT,
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_TO_DASHBOARD,
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_COMPETITIVE_INTEL,
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DATA_QUALITY_AUDIT,
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_PERFORMANCE_REPORT,
		loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_DEEP_RESEARCH,
	}
	for _, e := range enumValues {
		got := GetWorkflowTemplate(e)
		require.NotNil(t, got, "template %v missing from registry", e)
		assert.NotEmpty(t, got.DisplayName)
		assert.NotEmpty(t, got.Description)
		require.NotNil(t, got.DefaultWorkflowPattern,
			"template %v has nil default_workflow_pattern", e)
		require.NotEmpty(t, got.Agents,
			"template %v ships with zero agent specs", e)
	}
	assert.Len(t, ListWorkflowTemplates(), len(enumValues))
}

func TestWorkflowTemplateEnumStringRoundtrip(t *testing.T) {
	for _, tmpl := range ListWorkflowTemplates() {
		s := WorkflowTemplateEnumToString(tmpl.Template)
		require.NotEmpty(t, s, "enum %v has no string mapping", tmpl.Template)
		got := WorkflowTemplateEnumFromString(s)
		assert.Equal(t, tmpl.Template, got, "roundtrip failed for %q", s)
	}
	assert.Equal(t, loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_UNSPECIFIED,
		WorkflowTemplateEnumFromString("not-a-real-template"))
}

// TestPipelineTemplateStageCountMatchesAgentCount guards against template
// authoring errors where an agent slot has no corresponding pipeline stage
// (or vice versa). The server's materializer would error out at runtime;
// this surfaces the mistake at build time.
func TestPipelineTemplateStageCountMatchesAgentCount(t *testing.T) {
	for _, tmpl := range ListWorkflowTemplates() {
		if p, ok := tmpl.DefaultWorkflowPattern.Pattern.(*loomv1.WorkflowPattern_Pipeline); ok {
			assert.Equal(t, len(tmpl.Agents), len(p.Pipeline.Stages),
				"template %s has %d agents but %d pipeline stages",
				WorkflowTemplateEnumToString(tmpl.Template),
				len(tmpl.Agents), len(p.Pipeline.Stages))
		}
	}
}

// TestSchedulableTemplatesHaveCron locks in that any template flagged
// schedulable also ships a suggested cron the UI / weaver can surface.
// A schedulable template with no suggested_cron would force users to
// invent a schedule themselves on first run, defeating the curation.
func TestSchedulableTemplatesHaveCron(t *testing.T) {
	for _, tmpl := range ListWorkflowTemplates() {
		if tmpl.Schedulable {
			assert.NotEmpty(t, tmpl.SuggestedCron,
				"schedulable template %s must ship a suggested_cron",
				WorkflowTemplateEnumToString(tmpl.Template))
			assert.NotEmpty(t, tmpl.SuggestedTimezone,
				"schedulable template %s must ship a suggested_timezone",
				WorkflowTemplateEnumToString(tmpl.Template))
		}
	}
}

// TestEveryAgentSpecReferencesKnownPreset catches template authoring drift
// where an agent slot references an enum that has no preset entry.
func TestEveryAgentSpecReferencesKnownPreset(t *testing.T) {
	for _, tmpl := range ListWorkflowTemplates() {
		for i, spec := range tmpl.Agents {
			preset := GetPreset(spec.Preset)
			require.NotNil(t, preset,
				"template %s agent slot %d references unknown preset %v",
				WorkflowTemplateEnumToString(tmpl.Template), i, spec.Preset)
			assert.NotEmpty(t, spec.DefaultName,
				"template %s agent slot %d has empty default_name",
				WorkflowTemplateEnumToString(tmpl.Template), i)
			assert.NotEmpty(t, spec.SystemPrompt,
				"template %s agent slot %d has empty system_prompt",
				WorkflowTemplateEnumToString(tmpl.Template), i)
		}
	}
}
