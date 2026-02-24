// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// patternLibraryYAML represents the YAML structure for pattern library configuration
// Duplicated from pkg/patterns to avoid circular dependency
type patternLibraryYAML struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   patternMetadataYAML    `yaml:"metadata"`
	Spec       patternLibrarySpecYAML `yaml:"spec"`
}

type patternMetadataYAML struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Domain      string            `yaml:"domain"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

type patternLibrarySpecYAML struct {
	Entries []patternEntryYAML `yaml:"entries"`
}

type patternEntryYAML struct {
	Name              string           `yaml:"name"`
	Description       string           `yaml:"description"`
	TriggerConditions []string         `yaml:"trigger_conditions"`
	Template          string           `yaml:"template"`
	Example           string           `yaml:"example"`
	Rule              *patternRuleYAML `yaml:"rule"`
	Priority          int              `yaml:"priority"`
	Tags              []string         `yaml:"tags"`
}

type patternRuleYAML struct {
	Condition string `yaml:"condition"`
	Action    string `yaml:"action"`
	Rationale string `yaml:"rationale"`
}

// UpdatePatternPriority updates the priority field for a specific pattern in a YAML file.
// This function preserves comments, formatting, and structure of the YAML file.
func UpdatePatternPriority(yamlPath, patternName string, newPriority int32) error {
	// Read the YAML file
	yamlPath = filepath.Clean(yamlPath)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("failed to read YAML file: %w", err)
	}

	// Parse YAML with yaml.v3 to preserve structure
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Find and update the pattern entry
	updated := false
	if err := updatePriorityInNode(&node, patternName, newPriority, &updated); err != nil {
		return fmt.Errorf("failed to update priority: %w", err)
	}

	if !updated {
		return fmt.Errorf("pattern '%s' not found in %s", patternName, yamlPath)
	}

	// Marshal back to YAML with preserved formatting
	output, err := yaml.Marshal(&node)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write back to file
	if err := os.WriteFile(yamlPath, output, 0600); err != nil {
		return fmt.Errorf("failed to write YAML file: %w", err)
	}

	return nil
}

// updatePriorityInNode recursively searches for the pattern entry and updates its priority
func updatePriorityInNode(node *yaml.Node, patternName string, newPriority int32, updated *bool) error {
	if node == nil || *updated {
		return nil
	}

	// Handle different node kinds
	switch node.Kind {
	case yaml.DocumentNode:
		// Document node - recurse into content
		for _, child := range node.Content {
			if err := updatePriorityInNode(child, patternName, newPriority, updated); err != nil {
				return err
			}
		}

	case yaml.MappingNode:
		// Check if this is a pattern entry by looking for "name" field
		var entryName string
		var priorityNode *yaml.Node

		// Mapping nodes have key-value pairs as alternating Content elements
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Value == "name" && valueNode.Kind == yaml.ScalarNode {
				entryName = valueNode.Value
			}
			if keyNode.Value == "priority" {
				priorityNode = valueNode
			}
		}

		// If this entry matches our pattern name, update priority
		if entryName == patternName && priorityNode != nil {
			priorityNode.Value = fmt.Sprintf("%d", newPriority)
			*updated = true
			return nil
		}

		// Recurse into child mappings/sequences
		for _, child := range node.Content {
			if err := updatePriorityInNode(child, patternName, newPriority, updated); err != nil {
				return err
			}
		}

	case yaml.SequenceNode:
		// Sequence (array) - recurse into each element
		for _, child := range node.Content {
			if err := updatePriorityInNode(child, patternName, newPriority, updated); err != nil {
				return err
			}
		}
	}

	return nil
}

// FindPatternYAMLFile searches for a YAML file containing the specified pattern.
// It searches all .yaml and .yml files in the given directory (non-recursive).
func FindPatternYAMLFile(libraryPath, patternName string) (string, error) {
	// Check if libraryPath is a file or directory
	info, err := os.Stat(libraryPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat path: %w", err)
	}

	if !info.IsDir() {
		// It's a file - check if it contains the pattern
		if containsPattern(libraryPath, patternName) {
			return libraryPath, nil
		}
		return "", fmt.Errorf("pattern '%s' not found in %s", patternName, libraryPath)
	}

	// It's a directory - search all YAML files
	entries, err := os.ReadDir(libraryPath)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		fullPath := filepath.Join(libraryPath, name)
		if containsPattern(fullPath, patternName) {
			return fullPath, nil
		}
	}

	return "", fmt.Errorf("pattern '%s' not found in any YAML file in %s", patternName, libraryPath)
}

// containsPattern checks if a YAML file contains a pattern with the given name
func containsPattern(yamlPath, patternName string) bool {
	yamlPath = filepath.Clean(yamlPath)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return false
	}

	var config patternLibraryYAML
	if err := yaml.Unmarshal(data, &config); err != nil {
		return false
	}

	for _, entry := range config.Spec.Entries {
		if entry.Name == patternName {
			return true
		}
	}

	return false
}

// GetCurrentPriority reads the current priority value for a pattern from a YAML file
func GetCurrentPriority(yamlPath, patternName string) (int32, error) {
	yamlPath = filepath.Clean(yamlPath)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read YAML file: %w", err)
	}

	var config patternLibraryYAML
	if err := yaml.Unmarshal(data, &config); err != nil {
		return 0, fmt.Errorf("failed to parse YAML: %w", err)
	}

	for _, entry := range config.Spec.Entries {
		if entry.Name == patternName {
			return safeInt32(entry.Priority), nil
		}
	}

	return 0, fmt.Errorf("pattern '%s' not found in %s", patternName, yamlPath)
}
