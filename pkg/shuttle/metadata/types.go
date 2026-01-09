// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package metadata

// ToolMetadata provides comprehensive self-describing metadata for builtin tools.
// This metadata enables LLM-driven tool selection, conflict detection, and intelligent recommendations.
// Inspired by the pattern library's self-describing structure.
type ToolMetadata struct {
	// Core identity
	Name        string `yaml:"name" json:"name"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Category    string `yaml:"category" json:"category"` // "web", "file", "communication", "data", etc.

	// Capabilities and classification
	Capabilities []string `yaml:"capabilities" json:"capabilities"` // Semantic tags: "search", "http", "rest_api", etc.
	Keywords     []string `yaml:"keywords" json:"keywords"`         // Additional search terms

	// Tool selection guidance
	UseCases []UseCase `yaml:"use_cases" json:"use_cases"` // When to use this tool

	// Conflict detection
	Conflicts    []Conflict    `yaml:"conflicts,omitempty" json:"conflicts,omitempty"`       // Tools that conflict
	Alternatives []Alternative `yaml:"alternatives,omitempty" json:"alternatives,omitempty"` // Alternative tools
	Complements  []Complement  `yaml:"complements,omitempty" json:"complements,omitempty"`   // Complementary tools

	// Examples and best practices
	Examples      []Example     `yaml:"examples,omitempty" json:"examples,omitempty"`
	BestPractices string        `yaml:"best_practices,omitempty" json:"best_practices,omitempty"`
	CommonErrors  []CommonError `yaml:"common_errors,omitempty" json:"common_errors,omitempty"`

	// Prerequisites and requirements
	Prerequisites []Prerequisite `yaml:"prerequisites,omitempty" json:"prerequisites,omitempty"`
	RateLimit     *RateLimit     `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`

	// Backend/provider information
	Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"` // For multi-provider tools
	Backend   string   `yaml:"backend,omitempty" json:"backend,omitempty"`     // Target backend if specific
}

// UseCase describes a specific scenario where the tool should be used.
type UseCase struct {
	Title     string `yaml:"title" json:"title"`
	WhenToUse string `yaml:"when_to_use" json:"when_to_use"`
	Example   string `yaml:"example,omitempty" json:"example,omitempty"`
	NotFor    string `yaml:"not_for,omitempty" json:"not_for,omitempty"` // When NOT to use
}

// Conflict describes a conflict with another tool (builtin or MCP).
type Conflict struct {
	ToolName        string `yaml:"tool" json:"tool"` // Can be "tool_name" or "mcp:server_name"
	Reason          string `yaml:"reason" json:"reason"`
	WhenPreferThis  string `yaml:"when_prefer_this" json:"when_prefer_this"`
	WhenPreferOther string `yaml:"when_prefer_other" json:"when_prefer_other"`
	Severity        string `yaml:"severity,omitempty" json:"severity,omitempty"` // "high", "medium", "low"
}

// Alternative suggests an alternative tool for specific scenarios.
type Alternative struct {
	ToolName string `yaml:"tool" json:"tool"`
	When     string `yaml:"when" json:"when"`
	Benefits string `yaml:"benefits,omitempty" json:"benefits,omitempty"`
}

// Complement describes a tool that works well with this one.
type Complement struct {
	ToolName string `yaml:"tool" json:"tool"`
	Scenario string `yaml:"scenario" json:"scenario"`
	Example  string `yaml:"example,omitempty" json:"example,omitempty"`
}

// Example provides a complete worked example with input and output.
type Example struct {
	Name        string                 `yaml:"name" json:"name"`
	Description string                 `yaml:"description,omitempty" json:"description,omitempty"`
	Input       map[string]interface{} `yaml:"input" json:"input"`
	Output      interface{}            `yaml:"output" json:"output"`
	Explanation string                 `yaml:"explanation,omitempty" json:"explanation,omitempty"`
}

// CommonError documents frequently encountered errors.
type CommonError struct {
	Error    string `yaml:"error" json:"error"`
	Cause    string `yaml:"cause,omitempty" json:"cause,omitempty"`
	Solution string `yaml:"solution" json:"solution"`
}

// Prerequisite describes requirements for using the tool.
type Prerequisite struct {
	Name        string   `yaml:"name" json:"name"`
	RequiredFor []string `yaml:"required_for,omitempty" json:"required_for,omitempty"` // Which providers/modes
	EnvVars     []string `yaml:"env_vars,omitempty" json:"env_vars,omitempty"`
	HowToGet    string   `yaml:"how_to_get" json:"how_to_get"`
	Fallback    string   `yaml:"fallback,omitempty" json:"fallback,omitempty"` // Fallback if unavailable
}

// RateLimit describes rate limiting for the tool/providers.
type RateLimit struct {
	Limits map[string]int `yaml:"limits,omitempty" json:"limits,omitempty"` // provider -> requests_per_month
	Notes  string         `yaml:"notes,omitempty" json:"notes,omitempty"`
}
