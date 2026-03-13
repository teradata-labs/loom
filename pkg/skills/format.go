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

import "strings"

// EstimateTokens estimates the token count for a string.
// Uses the approximation: 1 token ~ 4 characters.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4 // Round up
}

// ComputeSkillBudget calculates the token budget for skills given the model's
// max context tokens and the configured budget percentage.
// Returns the total budget for skills+patterns combined.
func ComputeSkillBudget(maxContextTokens, budgetPercent int) int {
	if budgetPercent <= 0 {
		budgetPercent = 5 // default 5%
	}
	if maxContextTokens <= 0 {
		maxContextTokens = 100000 // fallback
	}
	return maxContextTokens * budgetPercent / 100
}

// FormatSkillsForInjection formats multiple skills for LLM system message injection.
// It respects the token budget, including skills in priority order.
// Returns the formatted string and the list of skill names that were included.
func FormatSkillsForInjection(skills []*ActiveSkill, maxTokens int) (string, []string) {
	if len(skills) == 0 {
		return "", nil
	}

	header := FormatSkillHeader()
	var sb strings.Builder
	sb.WriteString(header)

	usedTokens := EstimateTokens(header)
	var included []string

	for _, active := range skills {
		formatted := active.Skill.FormatForLLM()
		formattedTokens := EstimateTokens(formatted)

		// Account for separator between skills.
		separatorTokens := 0
		if len(included) > 0 {
			separatorTokens = EstimateTokens("\n---\n")
		}

		if usedTokens+formattedTokens+separatorTokens > maxTokens {
			continue // skip this skill, does not fit
		}

		if len(included) > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(formatted)
		usedTokens += formattedTokens + separatorTokens
		included = append(included, active.Skill.Name)
	}

	// If nothing was included, return empty.
	if len(included) == 0 {
		return "", nil
	}

	return sb.String(), included
}

// FormatSkillHeader returns the header for the skills injection block.
func FormatSkillHeader() string {
	return "# Active Skills\n\n"
}
