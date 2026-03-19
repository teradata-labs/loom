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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// kebabCaseRegex matches valid kebab-case identifiers: lowercase letters, digits, and hyphens.
var kebabCaseRegex = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// validDomains lists allowed domain values for skill metadata.
var validDomains = map[string]bool{
	"sql":          true,
	"code":         true,
	"data":         true,
	"ops":          true,
	"general":      true,
	"analytics":    true,
	"ml":           true,
	"data-quality": true,
	"rest-api":     true,
	"document":     true,
	"meta-agent":   true,
}

// validModes lists allowed activation mode values.
var validModes = map[string]bool{
	"":       true, // defaults to MANUAL
	"MANUAL": true,
	"AUTO":   true,
	"HYBRID": true,
	"ALWAYS": true,
}

// SkillYAML represents the YAML structure for a single skill file.
type SkillYAML struct {
	APIVersion      string            `yaml:"apiVersion"`
	Kind            string            `yaml:"kind"`
	Metadata        SkillMetadataYAML `yaml:"metadata"`
	Trigger         SkillTriggerYAML  `yaml:"trigger"`
	Prompt          SkillPromptYAML   `yaml:"prompt"`
	Tools           SkillToolsYAML    `yaml:"tools"`
	PatternRefs     []string          `yaml:"pattern_refs"`
	SkillRefs       []string          `yaml:"skill_refs"`
	MaxPromptTokens int32             `yaml:"max_prompt_tokens"`
	Sticky          bool              `yaml:"sticky"`
	Backend         string            `yaml:"backend"`
}

// SkillMetadataYAML holds skill metadata from YAML.
type SkillMetadataYAML struct {
	Name        string            `yaml:"name"`
	Title       string            `yaml:"title"`
	Description string            `yaml:"description"`
	Version     string            `yaml:"version"`
	Domain      string            `yaml:"domain"`
	Author      string            `yaml:"author"`
	Labels      map[string]string `yaml:"labels"`
}

// SkillTriggerYAML holds trigger configuration from YAML.
type SkillTriggerYAML struct {
	SlashCommands    []string `yaml:"slash_commands"`
	Keywords         []string `yaml:"keywords"`
	IntentCategories []string `yaml:"intent_categories"`
	Mode             string   `yaml:"mode"`
	MinConfidence    float64  `yaml:"min_confidence"`
}

// SkillPromptYAML holds prompt configuration from YAML.
type SkillPromptYAML struct {
	Instructions string             `yaml:"instructions"`
	Constraints  []string           `yaml:"constraints"`
	OutputFormat string             `yaml:"output_format"`
	Examples     []SkillExampleYAML `yaml:"examples"`
}

// SkillExampleYAML holds a single example from YAML.
type SkillExampleYAML struct {
	UserInput      string `yaml:"user_input"`
	ExpectedOutput string `yaml:"expected_output"`
	Explanation    string `yaml:"explanation"`
}

// SkillToolsYAML holds tool configuration from YAML.
type SkillToolsYAML struct {
	RequiredTools  []string `yaml:"required_tools"`
	PreferredOrder []string `yaml:"preferred_order"`
	ExcludedTools  []string `yaml:"excluded_tools"`
	MCPServers     []string `yaml:"mcp_servers"`
}

// SkillLibraryYAML represents the YAML structure for a skill library file.
type SkillLibraryYAML struct {
	APIVersion string                   `yaml:"apiVersion"`
	Kind       string                   `yaml:"kind"`
	Metadata   SkillLibraryMetadataYAML `yaml:"metadata"`
	Skills     []SkillYAML              `yaml:"skills"`
}

// SkillLibraryMetadataYAML holds library-level metadata from YAML.
type SkillLibraryMetadataYAML struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

// LoadSkill loads a single skill from a YAML file.
func LoadSkill(path string) (*Skill, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to read skill file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := os.ExpandEnv(string(data))

	var sy SkillYAML
	if err := yaml.Unmarshal([]byte(dataStr), &sy); err != nil {
		return nil, fmt.Errorf("failed to parse skill YAML %s: %w", path, err)
	}

	if err := validateSkillYAML(&sy); err != nil {
		return nil, fmt.Errorf("invalid skill %s: %w", path, err)
	}

	return yamlToSkill(&sy), nil
}

// LoadSkillLibrary loads a skill library YAML file containing multiple skills.
func LoadSkillLibrary(path string) ([]*Skill, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to read skill library file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := os.ExpandEnv(string(data))

	var lib SkillLibraryYAML
	if err := yaml.Unmarshal([]byte(dataStr), &lib); err != nil {
		return nil, fmt.Errorf("failed to parse skill library YAML %s: %w", path, err)
	}

	if lib.APIVersion != "loom/v1" {
		return nil, fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", lib.APIVersion)
	}
	if lib.Kind != "SkillLibrary" {
		return nil, fmt.Errorf("kind must be 'SkillLibrary', got: %s", lib.Kind)
	}
	if lib.Metadata.Name == "" {
		return nil, fmt.Errorf("metadata.name is required for skill library")
	}
	if len(lib.Skills) == 0 {
		return nil, fmt.Errorf("skill library must contain at least one skill")
	}

	skills := make([]*Skill, 0, len(lib.Skills))
	for i := range lib.Skills {
		if err := validateSkillYAML(&lib.Skills[i]); err != nil {
			return nil, fmt.Errorf("invalid skill at index %d in library: %w", i, err)
		}
		skills = append(skills, yamlToSkill(&lib.Skills[i]))
	}

	return skills, nil
}

// validateSkillYAML validates a skill YAML structure.
func validateSkillYAML(sy *SkillYAML) error {
	// metadata.name required and must be kebab-case
	if sy.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if !kebabCaseRegex.MatchString(sy.Metadata.Name) {
		return fmt.Errorf("metadata.name must be kebab-case (lowercase, hyphens, no spaces), got: %q", sy.Metadata.Name)
	}

	// metadata.domain required and must be valid
	if sy.Metadata.Domain == "" {
		return fmt.Errorf("metadata.domain is required")
	}
	if !validDomains[strings.ToLower(sy.Metadata.Domain)] {
		return fmt.Errorf("invalid domain: %q (must be one of: sql, code, data, ops, general, analytics, ml, data-quality, rest-api, document)", sy.Metadata.Domain)
	}

	// trigger.mode must be valid
	if !validModes[strings.ToUpper(sy.Trigger.Mode)] && !validModes[sy.Trigger.Mode] {
		return fmt.Errorf("invalid trigger.mode: %q (must be MANUAL, AUTO, HYBRID, ALWAYS, or empty)", sy.Trigger.Mode)
	}

	// prompt.instructions required
	if strings.TrimSpace(sy.Prompt.Instructions) == "" {
		return fmt.Errorf("prompt.instructions is required (non-empty)")
	}

	// skill_refs max depth 2
	if len(sy.SkillRefs) > 2 {
		return fmt.Errorf("skill_refs max depth is 2, got %d refs", len(sy.SkillRefs))
	}

	return nil
}

// yamlToSkill converts a SkillYAML to a Skill Go struct.
func yamlToSkill(sy *SkillYAML) *Skill {
	// Normalize mode: empty defaults to MANUAL
	mode := SkillActivationMode(strings.ToUpper(sy.Trigger.Mode))
	if mode == "" {
		mode = ActivationManual
	}

	// Normalize min_confidence: default to 0.7
	minConf := sy.Trigger.MinConfidence
	if minConf == 0 {
		minConf = 0.7
	}

	// Normalize version
	version := sy.Metadata.Version
	if version == "" {
		version = "1.0.0"
	}

	// Convert examples
	examples := make([]SkillExample, 0, len(sy.Prompt.Examples))
	for _, ex := range sy.Prompt.Examples {
		examples = append(examples, SkillExample(ex))
	}

	return &Skill{
		Name:        sy.Metadata.Name,
		Title:       sy.Metadata.Title,
		Description: sy.Metadata.Description,
		Version:     version,
		Domain:      strings.ToLower(sy.Metadata.Domain),
		Labels:      sy.Metadata.Labels,
		Author:      sy.Metadata.Author,
		Trigger: SkillTrigger{
			SlashCommands:    sy.Trigger.SlashCommands,
			Keywords:         sy.Trigger.Keywords,
			IntentCategories: sy.Trigger.IntentCategories,
			Mode:             mode,
			MinConfidence:    minConf,
		},
		Prompt: SkillPrompt{
			Instructions: sy.Prompt.Instructions,
			Constraints:  sy.Prompt.Constraints,
			OutputFormat: sy.Prompt.OutputFormat,
			Examples:     examples,
		},
		Tools: SkillToolConfig{
			RequiredTools:  sy.Tools.RequiredTools,
			PreferredOrder: sy.Tools.PreferredOrder,
			ExcludedTools:  sy.Tools.ExcludedTools,
			MCPServers:     sy.Tools.MCPServers,
		},
		PatternRefs:     sy.PatternRefs,
		SkillRefs:       sy.SkillRefs,
		MaxPromptTokens: sy.MaxPromptTokens,
		Sticky:          sy.Sticky,
		Backend:         sy.Backend,
	}
}

// SkillToYAML converts a Skill Go struct to YAML bytes.
// This is used by the MCP create_skill tool to serialize skills.
func SkillToYAML(s *Skill) ([]byte, error) {
	// Convert examples
	examples := make([]SkillExampleYAML, 0, len(s.Prompt.Examples))
	for _, ex := range s.Prompt.Examples {
		examples = append(examples, SkillExampleYAML(ex))
	}

	sy := SkillYAML{
		APIVersion: "loom/v1",
		Kind:       "Skill",
		Metadata: SkillMetadataYAML{
			Name:        s.Name,
			Title:       s.Title,
			Description: s.Description,
			Version:     s.Version,
			Domain:      s.Domain,
			Author:      s.Author,
			Labels:      s.Labels,
		},
		Trigger: SkillTriggerYAML{
			SlashCommands:    s.Trigger.SlashCommands,
			Keywords:         s.Trigger.Keywords,
			IntentCategories: s.Trigger.IntentCategories,
			Mode:             string(s.Trigger.Mode),
			MinConfidence:    s.Trigger.MinConfidence,
		},
		Prompt: SkillPromptYAML{
			Instructions: s.Prompt.Instructions,
			Constraints:  s.Prompt.Constraints,
			OutputFormat: s.Prompt.OutputFormat,
			Examples:     examples,
		},
		Tools: SkillToolsYAML{
			RequiredTools:  s.Tools.RequiredTools,
			PreferredOrder: s.Tools.PreferredOrder,
			ExcludedTools:  s.Tools.ExcludedTools,
			MCPServers:     s.Tools.MCPServers,
		},
		PatternRefs:     s.PatternRefs,
		SkillRefs:       s.SkillRefs,
		MaxPromptTokens: s.MaxPromptTokens,
		Sticky:          s.Sticky,
		Backend:         s.Backend,
	}

	data, err := yaml.Marshal(&sy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal skill to YAML: %w", err)
	}

	return data, nil
}
