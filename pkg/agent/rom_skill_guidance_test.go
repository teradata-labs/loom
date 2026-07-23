// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// D-6 (Skills v4 management) component acceptance test for Seam 6: standing
// ROM guidance (config text, not code) that a turn ended to ask for
// skill-approval is task progress, not a stall or failure.
//
// Covers: rom-guidance-turn-end-for-approval-is-progress

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const skillApprovalProgressGuidance = "ending your turn to ask for that approval is task progress"

func TestLoadROMContent_IncludesSkillApprovalProgressGuidance(t *testing.T) {
	content := LoadROMContent("", "")
	assert.Contains(t, content, skillApprovalProgressGuidance,
		"the base ROM must record that ending a turn to ask for skill approval counts as progress, not a stall")
	assert.Contains(t, content, "not a stall or a failure")
}

func TestLoadROMContent_SkillApprovalGuidance_SurvivesDomainComposition(t *testing.T) {
	// The guidance is base-ROM content and must still be present even when a
	// domain ROM (e.g. Teradata) is composed alongside it.
	content := LoadROMContent("TD", "")
	assert.Contains(t, content, skillApprovalProgressGuidance)
}

func TestLoadROMContent_None_OptsOutOfAllGuidance(t *testing.T) {
	// romID="none" is an explicit, total opt-out — the guidance is config
	// text, so opting out of ROM entirely also opts out of this sentence.
	content := LoadROMContent("none", "")
	assert.Empty(t, content)
}

func TestGetBaseROM_IncludesSkillApprovalProgressGuidance(t *testing.T) {
	content := string(GetBaseROM())
	assert.Contains(t, content, skillApprovalProgressGuidance)
	assert.True(t, strings.Contains(content, "Skills and Patterns"),
		"the guidance must live under the Skills and Patterns section of the base ROM")
}
