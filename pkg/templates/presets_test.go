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

// TestPresetRegistryCovers verifies every non-UNSPECIFIED enum value has a
// registry entry. Catches the easy regression where someone adds an enum
// without a registry entry (or vice versa).
func TestPresetRegistryCovers(t *testing.T) {
	enumValues := []loomv1.AgentPreset{
		loomv1.AgentPreset_AGENT_PRESET_PERSONAL_ASSISTANT,
		loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
		loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST,
		loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER,
		loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
		loomv1.AgentPreset_AGENT_PRESET_TASK_AUTOMATOR,
		loomv1.AgentPreset_AGENT_PRESET_QUICK_CHAT,
		loomv1.AgentPreset_AGENT_PRESET_COORDINATOR,
	}
	for _, e := range enumValues {
		got := GetPreset(e)
		require.NotNil(t, got, "preset %v missing from registry", e)
		assert.Equal(t, e, got.Preset)
		assert.NotEmpty(t, got.DisplayName, "preset %v has empty display name", e)
		assert.NotEmpty(t, got.Description, "preset %v has empty description", e)
		require.NotNil(t, got.Defaults, "preset %v has nil defaults", e)
	}
	assert.Len(t, ListPresets(), len(enumValues),
		"registry length must match enum cardinality so List returns every preset")
}

func TestPresetEnumStringRoundtrip(t *testing.T) {
	for _, p := range ListPresets() {
		s := PresetEnumToString(p.Preset)
		require.NotEmpty(t, s, "enum %v has no string mapping", p.Preset)
		got := PresetEnumFromString(s)
		assert.Equal(t, p.Preset, got, "roundtrip failed for %q", s)
	}
	// Unknown strings resolve to UNSPECIFIED so callers can detect.
	assert.Equal(t, loomv1.AgentPreset_AGENT_PRESET_UNSPECIFIED,
		PresetEnumFromString("does-not-exist"))
}

// TestApplyPreset_ZeroValueMergeRespectsUserOverrides covers the cloud-
// compatible merge contract: user-supplied non-zero numeric / string
// fields win; preset fills only the unset gaps. Critical for the weaver
// flow where the user explicitly tightens limits.
func TestApplyPreset_ZeroValueMergeRespectsUserOverrides(t *testing.T) {
	user := AppliedPreset{
		MaxTurns:      42,    // user override
		Temperature:   0.05,  // user override
		ThinkingLevel: "low", // user override
	}
	got := ApplyPreset(loomv1.AgentPreset_AGENT_PRESET_PERSONAL_ASSISTANT, user)

	assert.EqualValues(t, 42, got.MaxTurns, "user MaxTurns must win")
	assert.InDelta(t, 0.05, got.Temperature, 0.001, "user Temperature must win")
	assert.Equal(t, "low", got.ThinkingLevel, "user ThinkingLevel must win")

	// Fields the user didn't set come from the preset.
	assert.EqualValues(t, 1000, got.MaxToolExecutions,
		"preset MaxToolExecutions must fill the unset field")
	assert.Equal(t, "CONVERSATIONAL", got.WorkloadProfile,
		"preset WorkloadProfile must fill the unset field")
	assert.NotEmpty(t, got.Tools, "preset must supply tools when user has none")
}

func TestApplyPreset_UserToolsBlockPresetTools(t *testing.T) {
	user := AppliedPreset{
		Tools: []string{"my_custom_tool"},
	}
	got := ApplyPreset(loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST, user)
	assert.Equal(t, []string{"my_custom_tool"}, got.Tools,
		"any user-supplied tools must take the entire slot; preset tools are ignored")
}

func TestApplyPreset_UnknownEnumIsNoOp(t *testing.T) {
	user := AppliedPreset{MaxTurns: 10}
	got := ApplyPreset(loomv1.AgentPreset_AGENT_PRESET_UNSPECIFIED, user)
	assert.Equal(t, user, got,
		"UNSPECIFIED must pass through unchanged so callers can detect missing preset")
}

// TestPresetTools_OSSAugmentation locks in the user choice: research_analyst
// and task_automator carry OSS-only tools (parse_document, shell_execute,
// file_*) that the cloud counterparts lack.
func TestPresetTools_OSSAugmentation(t *testing.T) {
	research := GetPreset(loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST)
	require.NotNil(t, research)
	assert.Contains(t, research.Defaults.Tools, "parse_document",
		"research_analyst must include parse_document (OSS augmentation)")

	automator := GetPreset(loomv1.AgentPreset_AGENT_PRESET_TASK_AUTOMATOR)
	require.NotNil(t, automator)
	for _, want := range []string{"shell_execute", "file_read", "file_write"} {
		assert.Contains(t, automator.Defaults.Tools, want,
			"task_automator must include %s (OSS augmentation)", want)
	}
}
