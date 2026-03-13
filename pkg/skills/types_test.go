//go:build fts5

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

	"github.com/stretchr/testify/assert"
)

func TestSkill_FormatForLLM(t *testing.T) {
	tests := []struct {
		name           string
		skill          Skill
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "basic skill with instructions only",
			skill: Skill{
				Title: "Basic Skill",
				Prompt: SkillPrompt{
					Instructions: "Do the basic thing.",
				},
			},
			wantContains: []string{
				"## Skill: Basic Skill",
				"Do the basic thing.",
			},
			wantNotContain: []string{
				"### Constraints",
				"### Output Format",
				"### Examples",
			},
		},
		{
			name: "skill with constraints and output format",
			skill: Skill{
				Title: "Full Skill",
				Prompt: SkillPrompt{
					Instructions: "Analyze code.",
					Constraints:  []string{"Be concise", "No behavior changes"},
					OutputFormat: "## Summary\n- Items",
				},
			},
			wantContains: []string{
				"## Skill: Full Skill",
				"### Constraints",
				"- Be concise",
				"- No behavior changes",
				"### Output Format",
				"## Summary",
			},
		},
		{
			name: "constraints limited to maxFormatConstraints",
			skill: Skill{
				Title: "Many Constraints",
				Prompt: SkillPrompt{
					Instructions: "Do things.",
					Constraints: []string{
						"c1", "c2", "c3", "c4", "c5", "c6", "c7",
					},
				},
			},
			wantContains: []string{
				"- c1", "- c2", "- c3", "- c4", "- c5",
			},
			wantNotContain: []string{
				"- c6", "- c7",
			},
		},
		{
			name: "examples limited to maxFormatExamples",
			skill: Skill{
				Title: "Many Examples",
				Prompt: SkillPrompt{
					Instructions: "Do things.",
					Examples: []SkillExample{
						{UserInput: "input1", ExpectedOutput: "output1"},
						{UserInput: "input2", ExpectedOutput: "output2"},
						{UserInput: "input3", ExpectedOutput: "output3"},
					},
				},
			},
			wantContains: []string{
				"**Input:** input1",
				"**Output:** output1",
				"**Input:** input2",
				"**Output:** output2",
			},
			wantNotContain: []string{
				"input3",
				"output3",
			},
		},
		{
			name: "example with explanation",
			skill: Skill{
				Title: "With Explanation",
				Prompt: SkillPrompt{
					Instructions: "Do things.",
					Examples: []SkillExample{
						{
							UserInput:      "review this",
							ExpectedOutput: "found 2 issues",
							Explanation:    "prioritize security",
						},
					},
				},
			},
			wantContains: []string{
				"**Input:** review this",
				"**Output:** found 2 issues",
				"**Why:** prioritize security",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.skill.FormatForLLM()
			for _, want := range tc.wantContains {
				assert.Contains(t, got, want, "FormatForLLM output should contain %q", want)
			}
			for _, notWant := range tc.wantNotContain {
				assert.NotContains(t, got, notWant, "FormatForLLM output should not contain %q", notWant)
			}
		})
	}
}

func TestSkill_FormatForLLM_ExactConstraintCount(t *testing.T) {
	constraints := make([]string, 10)
	for i := range constraints {
		constraints[i] = "UNIQUE_CONSTRAINT_" + string(rune('A'+i))
	}

	skill := Skill{
		Title: "Count Test",
		Prompt: SkillPrompt{
			Instructions: "Instructions here.",
			Constraints:  constraints,
		},
	}

	got := skill.FormatForLLM()
	count := strings.Count(got, "UNIQUE_CONSTRAINT_")
	assert.Equal(t, maxFormatConstraints, count, "should include exactly %d constraints", maxFormatConstraints)
}

func TestSkill_FormatForLLM_ExactExampleCount(t *testing.T) {
	examples := make([]SkillExample, 5)
	for i := range examples {
		examples[i] = SkillExample{
			UserInput:      "UNIQUE_INPUT_" + string(rune('A'+i)),
			ExpectedOutput: "UNIQUE_OUTPUT_" + string(rune('A'+i)),
		}
	}

	skill := Skill{
		Title: "Example Count Test",
		Prompt: SkillPrompt{
			Instructions: "Instructions here.",
			Examples:     examples,
		},
	}

	got := skill.FormatForLLM()
	count := strings.Count(got, "UNIQUE_INPUT_")
	assert.Equal(t, maxFormatExamples, count, "should include exactly %d examples", maxFormatExamples)
}

func TestSkill_Summary(t *testing.T) {
	tests := []struct {
		name  string
		skill Skill
		want  SkillSummary
	}{
		{
			name: "all fields populated",
			skill: Skill{
				Name:        "code-review",
				Title:       "Code Review",
				Description: "Reviews code for issues",
				Domain:      "code",
				Version:     "1.2.0",
				Labels:      map[string]string{"category": "dev"},
				Trigger: SkillTrigger{
					SlashCommands: []string{"/review", "/cr"},
				},
			},
			want: SkillSummary{
				Name:        "code-review",
				Title:       "Code Review",
				Description: "Reviews code for issues",
				Domain:      "code",
				Version:     "1.2.0",
				Labels:      map[string]string{"category": "dev"},
				Commands:    []string{"/review", "/cr"},
			},
		},
		{
			name: "minimal skill",
			skill: Skill{
				Name:    "minimal",
				Domain:  "general",
				Version: "1.0.0",
			},
			want: SkillSummary{
				Name:    "minimal",
				Domain:  "general",
				Version: "1.0.0",
			},
		},
		{
			name: "nil labels preserved as nil",
			skill: Skill{
				Name:    "no-labels",
				Domain:  "ops",
				Version: "1.0.0",
			},
			want: SkillSummary{
				Name:    "no-labels",
				Domain:  "ops",
				Version: "1.0.0",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.skill.Summary()
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSkillActivationMode_Constants(t *testing.T) {
	tests := []struct {
		mode SkillActivationMode
		want string
	}{
		{ActivationManual, "MANUAL"},
		{ActivationAuto, "AUTO"},
		{ActivationHybrid, "HYBRID"},
		{ActivationAlways, "ALWAYS"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, string(tc.mode))
		})
	}
}
