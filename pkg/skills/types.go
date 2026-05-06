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

// SkillBindingMode controls how an agent loads a bound skill into context.
// Mirrors loomv1.SkillBindingMode.
type SkillBindingMode string

const (
	// BindingEager keeps the skill formatted into the system-prompt budget every turn.
	BindingEager SkillBindingMode = "EAGER"
	// BindingLazy promotes the skill into the orchestrator only when discovery selects it.
	BindingLazy SkillBindingMode = "LAZY"
	// BindingAlways forces the skill active every turn, overriding its trigger mode.
	BindingAlways SkillBindingMode = "ALWAYS"
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

	// Optional explicit decomposition. When nil and EmitTasks resolves to true
	// (default), task.Decomposer is invoked at activation with the skill prompt
	// as goal. The emitter uses TaskTemplate.Steps to materialize tasks via
	// task.Manager.CreateTask + AddDependency.
	TaskTemplate *SkillTaskTemplate `json:"task_template,omitempty"`

	// Index path used by the hierarchical router to place this skill in the
	// SkillIndex tree, e.g. "enterprise/sql/optimization". Empty falls back to
	// "unclassified/<domain>".
	ParentIndexPath string `json:"parent_index_path,omitempty"`

	// Whether activation emits tracked tasks. Pointer mirrors proto3 optional:
	// nil = not specified (default true), &false = explicitly suppressed.
	// EffectiveEmitTasks() resolves this against zero-value semantics.
	EmitTasks *bool `json:"emit_tasks,omitempty"`
}

// EffectiveEmitTasks returns the resolved emit-tasks decision for this skill,
// applying default-true semantics when EmitTasks is unset.
func (s *Skill) EffectiveEmitTasks() bool {
	if s.EmitTasks == nil {
		return true
	}
	return *s.EmitTasks
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

// SkillBinding ties a skill (or a glob/label-selector over the skill namespace)
// to an agent with a specific load policy. Mirrors loomv1.SkillBinding.
type SkillBinding struct {
	// Name is an exact skill name or a path.Match-style glob ("enterprise/sql/*").
	Name string `json:"name"`
	// Mode controls when the skill is loaded into the orchestrator.
	Mode SkillBindingMode `json:"mode,omitempty"`
	// Priority breaks ties when context budget is tight (higher wins).
	Priority int32 `json:"priority,omitempty"`
	// LabelMatch is an optional AND-selector against Skill.Labels.
	LabelMatch map[string]string `json:"label_match,omitempty"`
	// MinVersion is a semver lower bound; empty means any version.
	MinVersion string `json:"min_version,omitempty"`
}

// SkillTaskStep is one entry in an authored skill task template. Category
// and Priority are plain strings here (e.g. "research", "P1") and are
// converted to loomv1 enum values by the task emitter at materialization
// time.
type SkillTaskStep struct {
	Title              string  `json:"title"`
	Objective          string  `json:"objective,omitempty"`
	AcceptanceCriteria string  `json:"acceptance_criteria,omitempty"`
	Category           string  `json:"category,omitempty"`
	Priority           string  `json:"priority,omitempty"`
	DependsOn          []int32 `json:"depends_on,omitempty"`
	EstimatedEffort    string  `json:"estimated_effort,omitempty"`
	// Tags propagate onto the materialized Task.
	Tags []string `json:"tags,omitempty"`
}

// SkillTaskTemplate is an optional authored decomposition that materializes
// when the skill activates. When absent, the emitter falls back to
// task.Decomposer.Decompose with the skill's prompt as goal.
type SkillTaskTemplate struct {
	Steps []SkillTaskStep `json:"steps,omitempty"`
	// RootTitle names the parent task created to group emitted children.
	// Empty defaults to the skill's title.
	RootTitle string `json:"root_title,omitempty"`
	// EphemeralOnDeactivate deletes unstarted (still-OPEN) tasks if the
	// skill deactivates before they are claimed. In-progress and done
	// tasks are always preserved.
	EphemeralOnDeactivate bool `json:"ephemeral_on_deactivate,omitempty"`
	// MaxTasks caps the emitted task count. Default 8. Applies to
	// authored Steps and to decomposer fallback output alike.
	MaxTasks int32 `json:"max_tasks,omitempty"`
}

// SkillIndexNode is one node in the hierarchical PageIndex-style router tree.
// Built by index.Builder, persisted by index.Store, consumed by index.Router.
type SkillIndexNode struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary,omitempty"`
	Children  []string          `json:"children,omitempty"`
	SkillRefs []string          `json:"skill_refs,omitempty"`
	Depth     int32             `json:"depth"`
	Labels    map[string]string `json:"labels,omitempty"`
	// ContentHash dirties only this subtree on YAML hot-reload; full index
	// rebuilds happen only on bulk add/remove.
	ContentHash string `json:"content_hash,omitempty"`
}

// SkillIndex is the full router tree, persisted across boots.
type SkillIndex struct {
	ID           string            `json:"id"`
	RootID       string            `json:"root_id"`
	Nodes        []*SkillIndexNode `json:"nodes,omitempty"`
	BuiltAtMs    int64             `json:"built_at_ms,omitempty"`
	BuiltByModel string            `json:"built_by_model,omitempty"`
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
