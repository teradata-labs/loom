// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skills

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "empty string", input: "", expected: 0},
		{name: "one char", input: "a", expected: 1},
		{name: "four chars", input: "abcd", expected: 1},
		{name: "five chars", input: "abcde", expected: 2},
		{name: "eight chars", input: "abcdefgh", expected: 2},
		{name: "nine chars", input: "abcdefghi", expected: 3},
		{name: "100 chars", input: strings.Repeat("x", 100), expected: 25},
		{name: "101 chars", input: strings.Repeat("x", 101), expected: 26},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestComputeSkillBudget(t *testing.T) {
	tests := []struct {
		name             string
		maxContextTokens int
		budgetPercent    int
		expected         int
	}{
		{name: "standard 5%", maxContextTokens: 100000, budgetPercent: 5, expected: 5000},
		{name: "10% budget", maxContextTokens: 100000, budgetPercent: 10, expected: 10000},
		{name: "small context", maxContextTokens: 4096, budgetPercent: 5, expected: 204},
		{name: "zero percent uses default 5%", maxContextTokens: 100000, budgetPercent: 0, expected: 5000},
		{name: "negative percent uses default 5%", maxContextTokens: 100000, budgetPercent: -1, expected: 5000},
		{name: "zero context uses fallback 100k", maxContextTokens: 0, budgetPercent: 5, expected: 5000},
		{name: "negative context uses fallback 100k", maxContextTokens: -1, budgetPercent: 5, expected: 5000},
		{name: "both zero use defaults", maxContextTokens: 0, budgetPercent: 0, expected: 5000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSkillBudget(tc.maxContextTokens, tc.budgetPercent)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestFormatSkillsForInjection_Empty(t *testing.T) {
	result, names := FormatSkillsForInjection(nil, 5000)
	assert.Empty(t, result)
	assert.Nil(t, names)

	result, names = FormatSkillsForInjection([]*ActiveSkill{}, 5000)
	assert.Empty(t, result)
	assert.Nil(t, names)
}

func TestFormatSkillsForInjection_SingleSkill(t *testing.T) {
	skill := &Skill{
		Name:  "test-skill",
		Title: "Test Skill",
		Prompt: SkillPrompt{
			Instructions: "Do the thing.",
		},
	}
	active := []*ActiveSkill{
		{Skill: skill, ActivatedAt: time.Now()},
	}

	result, names := FormatSkillsForInjection(active, 5000)
	require.Len(t, names, 1)
	assert.Equal(t, "test-skill", names[0])
	assert.Contains(t, result, "# Active Skills")
	assert.Contains(t, result, "Test Skill")
	assert.Contains(t, result, "Do the thing.")
}

func TestFormatSkillsForInjection_BudgetConstraint(t *testing.T) {
	// Create two skills: one small and one that will blow the budget.
	smallSkill := &Skill{
		Name:  "small",
		Title: "Small",
		Prompt: SkillPrompt{
			Instructions: "Short instructions.",
		},
	}
	largeSkill := &Skill{
		Name:  "large",
		Title: "Large",
		Prompt: SkillPrompt{
			Instructions: strings.Repeat("x", 4000), // ~1000 tokens
		},
	}

	active := []*ActiveSkill{
		{Skill: smallSkill, ActivatedAt: time.Now()},
		{Skill: largeSkill, ActivatedAt: time.Now()},
	}

	// Give a budget that fits the small skill but not both.
	// Small skill FormatForLLM: "## Skill: Small\nShort instructions.\n" ~ 10 tokens
	// Header: "# Active Skills\n\n" ~ 5 tokens
	// Total small ~ 15 tokens. Set budget to 20 so large cannot fit.
	result, names := FormatSkillsForInjection(active, 20)
	require.Len(t, names, 1)
	assert.Equal(t, "small", names[0])
	assert.Contains(t, result, "Small")
	assert.NotContains(t, result, "Large")
}

func TestFormatSkillsForInjection_AllSkillsFit(t *testing.T) {
	skill1 := &Skill{
		Name:  "alpha",
		Title: "Alpha",
		Prompt: SkillPrompt{
			Instructions: "Alpha instructions.",
		},
	}
	skill2 := &Skill{
		Name:  "beta",
		Title: "Beta",
		Prompt: SkillPrompt{
			Instructions: "Beta instructions.",
		},
	}

	active := []*ActiveSkill{
		{Skill: skill1, ActivatedAt: time.Now()},
		{Skill: skill2, ActivatedAt: time.Now()},
	}

	result, names := FormatSkillsForInjection(active, 5000)
	require.Len(t, names, 2)
	assert.Equal(t, "alpha", names[0])
	assert.Equal(t, "beta", names[1])
	assert.Contains(t, result, "# Active Skills")
	assert.Contains(t, result, "Alpha")
	assert.Contains(t, result, "Beta")
	assert.Contains(t, result, "\n---\n")
}

func TestFormatSkillsForInjection_NoneWithinBudget(t *testing.T) {
	skill := &Skill{
		Name:  "big",
		Title: "Big Skill",
		Prompt: SkillPrompt{
			Instructions: strings.Repeat("x", 400), // ~100 tokens
		},
	}
	active := []*ActiveSkill{
		{Skill: skill, ActivatedAt: time.Now()},
	}

	// Budget of 5 tokens -- header alone is ~5, skill cannot fit.
	result, names := FormatSkillsForInjection(active, 5)
	assert.Empty(t, result)
	assert.Nil(t, names)
}

func TestFormatSkillHeader(t *testing.T) {
	header := FormatSkillHeader()
	assert.Equal(t, "# Active Skills\n\n", header)
}
