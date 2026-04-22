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

// Package skills provides skill loading, matching, and lifecycle management.
// Skills are activatable behaviors that combine prompt injection, tool preferences,
// trigger conditions, and optional persistence across turns.
package skills

import (
	"fmt"
	"strings"
	"time"
)

// SkillActivationMode controls how a skill can be triggered.
type SkillActivationMode string

const (
	// ActivationManual requires explicit slash command or API call.
	ActivationManual SkillActivationMode = "MANUAL"
	// ActivationAuto enables auto-detection from user message content.
	ActivationAuto SkillActivationMode = "AUTO"
	// ActivationHybrid supports both manual and auto-detection.
	ActivationHybrid SkillActivationMode = "HYBRID"
	// ActivationAlways keeps the skill active whenever configured.
	ActivationAlways SkillActivationMode = "ALWAYS"
)

// maxFormatExamples is the maximum number of examples included in FormatForLLM output.
const maxFormatExamples = 2

// maxFormatConstraints is the maximum number of constraints included in FormatForLLM output.
const maxFormatConstraints = 5

// Skill represents an activatable behavior that combines prompt injection,
// tool preferences, trigger conditions, and optional persistence across turns.
type Skill struct {
	// Metadata
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Domain      string            `json:"domain"`
	Labels      map[string]string `json:"labels,omitempty"`
	Author      string            `json:"author,omitempty"`

	// Trigger configuration
	Trigger SkillTrigger `json:"trigger"`

	// Prompt instructions
	Prompt SkillPrompt `json:"prompt"`

	// Tool requirements and preferences
	Tools SkillToolConfig `json:"tools"`

	// Co-inject these patterns when skill is active
	PatternRefs []string `json:"pattern_refs,omitempty"`

	// Compose with sub-skills (max depth: 2)
	SkillRefs []string `json:"skill_refs,omitempty"`

	// Token budget for this skill's prompt (0 = use default 1500)
	MaxPromptTokens int32 `json:"max_prompt_tokens,omitempty"`

	// Persist across turns once activated
	Sticky bool `json:"sticky,omitempty"`

	// Target backend (empty = agnostic)
	Backend string `json:"backend,omitempty"`
}

// SkillTrigger defines how a skill gets activated.
type SkillTrigger struct {
	// Slash commands that activate this skill: ["/review", "/code-review"]
	SlashCommands []string `json:"slash_commands,omitempty"`

	// Keywords for auto-detection from user messages
	Keywords []string `json:"keywords,omitempty"`

	// Freeform intent labels for intent-based activation (matched against pattern intents)
	IntentCategories []string `json:"intent_categories,omitempty"`

	// How this skill can be activated
	Mode SkillActivationMode `json:"mode,omitempty"`

	// Minimum confidence for AUTO/HYBRID activation (default: 0.7)
	MinConfidence float64 `json:"min_confidence,omitempty"`
}

// SkillPrompt defines the instructions injected when a skill is active.
type SkillPrompt struct {
	// Main prompt instructions (supports {{.var}} interpolation)
	Instructions string `json:"instructions"`

	// Rules and constraints to enforce
	Constraints []string `json:"constraints,omitempty"`

	// Expected output structure description
	OutputFormat string `json:"output_format,omitempty"`

	// Few-shot examples
	Examples []SkillExample `json:"examples,omitempty"`
}

// SkillExample provides a few-shot example for the skill.
type SkillExample struct {
	UserInput      string `json:"user_input"`
	ExpectedOutput string `json:"expected_output"`
	Explanation    string `json:"explanation,omitempty"`
}

// SkillToolConfig defines tool requirements and preferences for a skill.
type SkillToolConfig struct {
	// Ensure these tools are registered when skill is active
	RequiredTools []string `json:"required_tools,omitempty"`

	// Suggested tool execution ordering
	PreferredOrder []string `json:"preferred_order,omitempty"`

	// Tools to hide when skill is active
	ExcludedTools []string `json:"excluded_tools,omitempty"`

	// MCP servers to enable when skill is active
	MCPServers []string `json:"mcp_servers,omitempty"`
}

// ActiveSkill tracks an activated skill within a session.
type ActiveSkill struct {
	Skill        *Skill    `json:"skill"`
	TriggerType  string    `json:"trigger_type"`
	TriggerValue string    `json:"trigger_value"`
	Confidence   float64   `json:"confidence"`
	ActivatedAt  time.Time `json:"activated_at"`
	SessionID    string    `json:"session_id"`
	AgentID      string    `json:"agent_id"`
}

// MatchResult represents the result of matching a skill against user input.
type MatchResult struct {
	Skill        *Skill  `json:"skill"`
	Confidence   float64 `json:"confidence"`
	TriggerType  string  `json:"trigger_type"`
	TriggerValue string  `json:"trigger_value"`
}

// ScoredSkill represents a skill with a relevance score for search/ranking.
type ScoredSkill struct {
	Skill *Skill  `json:"skill"`
	Score float64 `json:"score"`
}

// SkillSummary provides lightweight metadata for catalog listing.
type SkillSummary struct {
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Domain      string            `json:"domain"`
	Version     string            `json:"version"`
	Labels      map[string]string `json:"labels,omitempty"`
	Commands    []string          `json:"commands,omitempty"`
}

// Summary returns a lightweight SkillSummary from this Skill.
func (s *Skill) Summary() SkillSummary {
	return SkillSummary{
		Name:        s.Name,
		Title:       s.Title,
		Description: s.Description,
		Domain:      s.Domain,
		Version:     s.Version,
		Labels:      s.Labels,
		Commands:    s.Trigger.SlashCommands,
	}
}

// FormatForLLM formats the skill for LLM injection.
// Returns a concise, actionable representation optimized for token efficiency.
// Target: <2000 tokens per skill.
func (s *Skill) FormatForLLM() string {
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("## Skill: %s\n", s.Title))
	sb.WriteString(s.Prompt.Instructions)
	sb.WriteString("\n")

	// Constraints (limit to maxFormatConstraints)
	if len(s.Prompt.Constraints) > 0 {
		sb.WriteString("\n### Constraints\n")
		limit := len(s.Prompt.Constraints)
		if limit > maxFormatConstraints {
			limit = maxFormatConstraints
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(fmt.Sprintf("- %s\n", s.Prompt.Constraints[i]))
		}
	}

	// Output format
	if s.Prompt.OutputFormat != "" {
		sb.WriteString(fmt.Sprintf("\n### Output Format\n%s\n", s.Prompt.OutputFormat))
	}

	// Examples (limit to maxFormatExamples)
	if len(s.Prompt.Examples) > 0 {
		sb.WriteString("\n### Examples\n")
		limit := len(s.Prompt.Examples)
		if limit > maxFormatExamples {
			limit = maxFormatExamples
		}
		for i := 0; i < limit; i++ {
			ex := s.Prompt.Examples[i]
			sb.WriteString(fmt.Sprintf("**Input:** %s\n", ex.UserInput))
			sb.WriteString(fmt.Sprintf("**Output:** %s\n", ex.ExpectedOutput))
			if ex.Explanation != "" {
				sb.WriteString(fmt.Sprintf("**Why:** %s\n", ex.Explanation))
			}
			if i < limit-1 {
				sb.WriteString("\n")
			}
		}
	}

	return sb.String()
}
