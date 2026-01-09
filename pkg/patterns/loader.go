// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package patterns

import (
	"fmt"
	"os"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// PatternLibraryYAML represents the YAML structure for pattern library configuration
type PatternLibraryYAML struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   PatternMetadataYAML    `yaml:"metadata"`
	Spec       PatternLibrarySpecYAML `yaml:"spec"`
}

type PatternMetadataYAML struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Domain      string            `yaml:"domain"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

type PatternLibrarySpecYAML struct {
	Entries []PatternEntryYAML `yaml:"entries"`
}

type PatternEntryYAML struct {
	Name              string           `yaml:"name"`
	Description       string           `yaml:"description"`
	TriggerConditions []string         `yaml:"trigger_conditions"`
	Template          string           `yaml:"template"`
	Example           string           `yaml:"example"`
	Rule              *PatternRuleYAML `yaml:"rule"`
	Priority          int              `yaml:"priority"`
	Tags              []string         `yaml:"tags"`
}

type PatternRuleYAML struct {
	Condition string `yaml:"condition"`
	Action    string `yaml:"action"`
	Rationale string `yaml:"rationale"`
}

// LoadPatternLibrary loads a pattern library configuration from a YAML file
func LoadPatternLibrary(path string) (*loomv1.PatternLibrary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read pattern library file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := expandEnvVars(string(data))

	var yamlConfig PatternLibraryYAML
	if err := yaml.Unmarshal([]byte(dataStr), &yamlConfig); err != nil {
		return nil, fmt.Errorf("failed to parse pattern library YAML: %w", err)
	}

	// Validate structure
	if err := validatePatternLibraryYAML(&yamlConfig); err != nil {
		return nil, fmt.Errorf("invalid pattern library config: %w", err)
	}

	// Convert to proto
	library := yamlToProtoPatternLibrary(&yamlConfig)

	return library, nil
}

// validatePatternLibraryYAML validates the YAML structure
func validatePatternLibraryYAML(yaml *PatternLibraryYAML) error {
	if yaml.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if yaml.APIVersion != "loom/v1" {
		return fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", yaml.APIVersion)
	}
	if yaml.Kind != "PatternLibrary" {
		return fmt.Errorf("kind must be 'PatternLibrary', got: %s", yaml.Kind)
	}
	if yaml.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if yaml.Metadata.Domain == "" {
		return fmt.Errorf("metadata.domain is required")
	}

	// Validate domain is a known type
	validDomains := map[string]bool{
		"sql":          true,
		"teradata":     true,
		"postgres":     true,
		"mysql":        true,
		"code-review":  true,
		"rest-api":     true,
		"graphql":      true,
		"document":     true,
		"ml":           true,
		"analytics":    true,
		"data-quality": true,
	}
	if !validDomains[strings.ToLower(yaml.Metadata.Domain)] {
		return fmt.Errorf("invalid domain: %s (must be: sql, teradata, postgres, mysql, code-review, rest-api, graphql, document, ml, analytics, data-quality)", yaml.Metadata.Domain)
	}

	// Validate entries
	if len(yaml.Spec.Entries) == 0 {
		return fmt.Errorf("spec.entries cannot be empty - must have at least one pattern")
	}

	for i, entry := range yaml.Spec.Entries {
		if err := validatePatternEntry(&entry, i); err != nil {
			return err
		}
	}

	return nil
}

// validatePatternEntry validates a single pattern entry
func validatePatternEntry(entry *PatternEntryYAML, index int) error {
	if entry.Name == "" {
		return fmt.Errorf("entries[%d].name is required", index)
	}
	if entry.Description == "" {
		return fmt.Errorf("entries[%d].description is required", index)
	}

	// Must have at least one content type
	hasContent := entry.Template != "" || entry.Example != "" || entry.Rule != nil
	if !hasContent {
		return fmt.Errorf("entries[%d] must have at least one of: template, example, or rule", index)
	}

	// Validate rule if present
	if entry.Rule != nil {
		if entry.Rule.Condition == "" {
			return fmt.Errorf("entries[%d].rule.condition is required", index)
		}
		if entry.Rule.Action == "" {
			return fmt.Errorf("entries[%d].rule.action is required", index)
		}
	}

	// Validate priority is reasonable
	if entry.Priority < 0 || entry.Priority > 100 {
		return fmt.Errorf("entries[%d].priority must be between 0 and 100, got: %d", index, entry.Priority)
	}

	return nil
}

// yamlToProtoPatternLibrary converts YAML to proto
func yamlToProtoPatternLibrary(yaml *PatternLibraryYAML) *loomv1.PatternLibrary {
	library := &loomv1.PatternLibrary{
		Metadata: &loomv1.PatternMetadata{
			Name:        yaml.Metadata.Name,
			Version:     yaml.Metadata.Version,
			Domain:      strings.ToLower(yaml.Metadata.Domain),
			Description: yaml.Metadata.Description,
			Labels:      yaml.Metadata.Labels,
		},
		Spec: &loomv1.PatternSpec{
			Entries: make([]*loomv1.PatternEntry, 0, len(yaml.Spec.Entries)),
		},
	}

	// Convert entries
	for _, entry := range yaml.Spec.Entries {
		protoEntry := &loomv1.PatternEntry{
			Name:              entry.Name,
			Description:       entry.Description,
			TriggerConditions: entry.TriggerConditions,
			Priority:          int32(entry.Priority),
			Tags:              entry.Tags,
		}

		// Set content based on what's provided (proto oneof)
		if entry.Template != "" {
			protoEntry.Content = &loomv1.PatternEntry_Template{
				Template: entry.Template,
			}
		} else if entry.Example != "" {
			protoEntry.Content = &loomv1.PatternEntry_Example{
				Example: entry.Example,
			}
		} else if entry.Rule != nil {
			protoEntry.Content = &loomv1.PatternEntry_Rule{
				Rule: &loomv1.PatternEntryRule{
					Condition: entry.Rule.Condition,
					Action:    entry.Rule.Action,
					Rationale: entry.Rule.Rationale,
				},
			}
		}

		library.Spec.Entries = append(library.Spec.Entries, protoEntry)
	}

	// Set defaults
	if library.Metadata.Version == "" {
		library.Metadata.Version = "1.0.0"
	}

	return library
}

// expandEnvVars expands environment variables in YAML content
func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}
