// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package templates

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// presetRegistry is the authoritative list of agent presets shipped with
// the OSS binary. One entry per AgentPreset enum value (except UNSPECIFIED).
//
// Tool lists are augmented over the cloud-side presets to take advantage of
// OSS-only builtins (parse_document for research, shell_execute + file_*
// for the task automator). The augmentation is documented per-preset where
// it diverges from the cloud equivalent.
var presetRegistry = []*loomv1.AgentPresetInfo{
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_PERSONAL_ASSISTANT,
		DisplayName: "Personal Assistant",
		Description: "Unlimited turns, full tools, deep thinking, long-term graph memory. Your always-on AI assistant.",
		Icon:        "message-square",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:                 1000,
			MaxToolExecutions:        1000,
			MaxIterations:            0, // unlimited
			TimeoutSeconds:           3600,
			Temperature:              0.7,
			ThinkingLevel:            "high",
			CacheControlEnabled:      ptrTrue(),
			WorkloadProfile:          "CONVERSATIONAL",
			HistoryWindowSize:        0, // full history
			GraphMemoryEnabled:       ptrTrue(),
			GraphMemoryBudgetPercent: 20,
			GraphMemoryMaxCandidates: 100,
			// OSS-augmented: file_read + file_write give the assistant disk access
			// the cloud counterpart lacks; parse_document handles uploads.
			Tools: []string{
				"web_search",
				"http_request",
				"create_ui_app",
				"manage_ephemeral_agents",
				"file_read",
				"file_write",
				"parse_document",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST,
		DisplayName: "Research Analyst",
		Description: "Deep web research with high context window, systematic data synthesis and citation.",
		Icon:        "search",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:                 1000,
			MaxToolExecutions:        1000,
			MaxIterations:            0,
			TimeoutSeconds:           1800,
			Temperature:              0.3,
			ThinkingLevel:            "high",
			CacheControlEnabled:      ptrTrue(),
			WorkloadProfile:          "DATA_INTENSIVE",
			MaxContextTokens:         200000,
			GraphMemoryEnabled:       ptrTrue(),
			GraphMemoryBudgetPercent: 10,
			GraphMemoryMaxCandidates: 50,
			// OSS-augmented: parse_document for handling PDFs / docx; file_read
			// for inspecting local research corpora.
			Tools: []string{
				"web_search",
				"http_request",
				"parse_document",
				"file_read",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST,
		DisplayName: "Teradata Analyst",
		Description: "SQL and Teradata data analysis with TD ROM, data-intensive workload, and visualization.",
		Icon:        "database",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:            1000,
			MaxToolExecutions:   1000,
			MaxIterations:       0,
			TimeoutSeconds:      1800,
			Temperature:         0.2,
			ThinkingLevel:       "high",
			CacheControlEnabled: ptrTrue(),
			WorkloadProfile:     "DATA_INTENSIVE",
			BackendType:         "teradata",
			Rom:                 "TD",
			// Unlimited query-result inspection — let the agent see full result sets.
			QueryToolResultThresholdBytes: -1,
			MaxToolResults:                0,
			GraphMemoryEnabled:            ptrTrue(),
			GraphMemoryBudgetPercent:      10,
			GraphMemoryMaxCandidates:      50,
			Tools: []string{
				"http_request",
				"create_ui_app",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER,
		DisplayName: "Creative Writer",
		Description: "High temperature for creativity, strong memory for continuity across long-form projects.",
		Icon:        "pencil",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:                 150,
			MaxToolExecutions:        50,
			MaxIterations:            0,
			TimeoutSeconds:           3600,
			Temperature:              1.0,
			ThinkingLevel:            "high",
			CacheControlEnabled:      ptrTrue(),
			WorkloadProfile:          "CONVERSATIONAL",
			GraphMemoryEnabled:       ptrTrue(),
			GraphMemoryBudgetPercent: 25,
			GraphMemoryMaxCandidates: 100,
			// OSS-augmented: file_write so the agent can persist drafts to the workspace.
			Tools: []string{
				"web_search",
				"file_read",
				"file_write",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST,
		DisplayName: "UI Specialist",
		Description: "Dashboard and visualization builder with full internet access for data and reference.",
		Icon:        "layout",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:                 75,
			MaxToolExecutions:        150,
			MaxIterations:            0,
			TimeoutSeconds:           1800,
			Temperature:              0.4,
			ThinkingLevel:            "high",
			CacheControlEnabled:      ptrTrue(),
			WorkloadProfile:          "BALANCED",
			GraphMemoryEnabled:       ptrTrue(),
			GraphMemoryBudgetPercent: 10,
			GraphMemoryMaxCandidates: 50,
			Tools: []string{
				"create_ui_app",
				"web_search",
				"http_request",
				"file_read",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_TASK_AUTOMATOR,
		DisplayName: "Task Automator",
		Description: "Agentic workhorse with sub-agent delegation, shell + file tools, and disabled output-token circuit breaker for long-running runs.",
		Icon:        "cog",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:                 100,
			MaxToolExecutions:        500,
			MaxIterations:            0,
			TimeoutSeconds:           3600,
			Temperature:              0.3,
			ThinkingLevel:            "medium",
			CacheControlEnabled:      ptrTrue(),
			WorkloadProfile:          "BALANCED",
			OutputTokenCbThreshold:   -1, // disable for long-running tasks
			GraphMemoryEnabled:       ptrTrue(),
			GraphMemoryBudgetPercent: 10,
			GraphMemoryMaxCandidates: 50,
			// OSS-augmented: the task automator is the canonical preset that
			// benefits from OSS-only shell + file tools. Cloud could not offer
			// these in a hosted sandbox; OSS users running locally typically can.
			Tools: []string{
				"web_search",
				"http_request",
				"manage_ephemeral_agents",
				"agent_management",
				"create_ui_app",
				"shell_execute",
				"file_read",
				"file_write",
				"parse_document",
			},
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_QUICK_CHAT,
		DisplayName: "Quick Chat",
		Description: "Fast, lightweight Q&A with minimal overhead and low cost. No tools, short context.",
		Icon:        "zap",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:            25,
			MaxToolExecutions:   10,
			MaxIterations:       5,
			TimeoutSeconds:      300,
			Temperature:         0.5,
			ThinkingLevel:       "none",
			CacheControlEnabled: ptrFalse(),
			WorkloadProfile:     "CONVERSATIONAL",
			HistoryWindowSize:   20,
			GraphMemoryEnabled:  ptrFalse(),
			// Intentionally no tools — fast Q&A only.
			Tools: nil,
		},
	},
	{
		Preset:      loomv1.AgentPreset_AGENT_PRESET_COORDINATOR,
		DisplayName: "Coordinator",
		Description: "Decomposes goals into tasks and delegates all work to sub-agents. Does no work directly.",
		Icon:        "git-branch",
		Defaults: &loomv1.PresetDefaults{
			MaxTurns:               200,
			MaxToolExecutions:      500,
			MaxIterations:          0,
			TimeoutSeconds:         3600,
			Temperature:            0.3,
			ThinkingLevel:          "high",
			CacheControlEnabled:    ptrTrue(),
			WorkloadProfile:        "BALANCED",
			GraphMemoryEnabled:     ptrFalse(),
			OutputTokenCbThreshold: -1,
			// task_board is auto-injected when memory.task_board.enabled is set,
			// but we surface it here as documentation for what the coordinator
			// requires; the user enabling this preset still needs the per-agent
			// task_board config flag for tool surfacing.
			Tools: []string{
				"manage_ephemeral_agents",
				"agent_management",
			},
		},
	},
}

// presetByEnum is a build-time index over presetRegistry keyed by enum.
var presetByEnum = func() map[loomv1.AgentPreset]*loomv1.AgentPresetInfo {
	out := make(map[loomv1.AgentPreset]*loomv1.AgentPresetInfo, len(presetRegistry))
	for _, p := range presetRegistry {
		out[p.Preset] = p
	}
	return out
}()

// ListPresets returns every registered preset. The returned slice is the
// shared registry — callers must not mutate entries. Suitable for protobuf
// response marshaling since each entry is already a *loomv1.AgentPresetInfo.
func ListPresets() []*loomv1.AgentPresetInfo {
	return presetRegistry
}

// GetPreset returns the preset matching enum, or nil if the enum is
// UNSPECIFIED / unknown. Callers should treat nil as "no preset; do not
// apply defaults".
func GetPreset(p loomv1.AgentPreset) *loomv1.AgentPresetInfo {
	return presetByEnum[p]
}

// PresetEnumFromString maps a preset name (cloud-compatible snake_case) to
// the corresponding enum. Returns UNSPECIFIED for unknown strings so the
// caller can distinguish "valid but unknown" from "valid".
func PresetEnumFromString(s string) loomv1.AgentPreset {
	switch s {
	case "personal_assistant":
		return loomv1.AgentPreset_AGENT_PRESET_PERSONAL_ASSISTANT
	case "research_analyst":
		return loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST
	case "teradata_analyst":
		return loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST
	case "creative_writer":
		return loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER
	case "ui_specialist":
		return loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST
	case "task_automator":
		return loomv1.AgentPreset_AGENT_PRESET_TASK_AUTOMATOR
	case "quick_chat":
		return loomv1.AgentPreset_AGENT_PRESET_QUICK_CHAT
	case "coordinator":
		return loomv1.AgentPreset_AGENT_PRESET_COORDINATOR
	default:
		return loomv1.AgentPreset_AGENT_PRESET_UNSPECIFIED
	}
}

// PresetEnumToString is the inverse of PresetEnumFromString. Returns the
// empty string for UNSPECIFIED so callers can detect a missing preset.
func PresetEnumToString(p loomv1.AgentPreset) string {
	switch p {
	case loomv1.AgentPreset_AGENT_PRESET_PERSONAL_ASSISTANT:
		return "personal_assistant"
	case loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST:
		return "research_analyst"
	case loomv1.AgentPreset_AGENT_PRESET_TERADATA_ANALYST:
		return "teradata_analyst"
	case loomv1.AgentPreset_AGENT_PRESET_CREATIVE_WRITER:
		return "creative_writer"
	case loomv1.AgentPreset_AGENT_PRESET_UI_SPECIALIST:
		return "ui_specialist"
	case loomv1.AgentPreset_AGENT_PRESET_TASK_AUTOMATOR:
		return "task_automator"
	case loomv1.AgentPreset_AGENT_PRESET_QUICK_CHAT:
		return "quick_chat"
	case loomv1.AgentPreset_AGENT_PRESET_COORDINATOR:
		return "coordinator"
	default:
		return ""
	}
}

// AppliedPreset is the materialized result of merging preset defaults onto
// a user's CreateAgentRequest-like input. Callers convert this to whatever
// concrete request shape their RPC expects.
//
// Merge semantics (matching cloud):
//   - Numeric and string fields use zero-value detection: user-supplied
//     non-zero wins; otherwise preset default applies.
//   - Tools: if the user supplied any tools, those win entirely; otherwise
//     the preset's tool list applies.
//   - Boolean fields (proto3 optional in PresetDefaults): preset's
//     explicit setting wins when the user didn't override.
type AppliedPreset struct {
	Tools                         []string
	MaxTurns                      int32
	MaxToolExecutions             int32
	MaxIterations                 int32
	TimeoutSeconds                int32
	Temperature                   float32
	MaxTokens                     int32
	MaxContextTokens              int32
	ReservedOutputTokens          int32
	HistoryWindowSize             int32
	OutputTokenCbThreshold        int32
	QueryToolResultThresholdBytes int64
	MaxToolResults                int32
	GraphMemoryBudgetPercent      int32
	GraphMemoryMaxCandidates      int32

	ThinkingLevel   string
	WorkloadProfile string
	Rom             string
	BackendType     string

	// Booleans surface as concrete bools after merge — callers wanting
	// "was this set" semantics can compare against the preset directly.
	CacheControlEnabled bool
	GraphMemoryEnabled  bool
	AllowCodeExecution  bool
}

// ApplyPreset merges a preset's defaults onto a user-supplied AppliedPreset
// representing what the user explicitly requested. Returns a new struct;
// the input is not mutated. Numeric / string fields use zero-value
// detection; bool fields take the preset's explicit setting whenever the
// preset specifies one.
func ApplyPreset(p loomv1.AgentPreset, user AppliedPreset) AppliedPreset {
	info := GetPreset(p)
	if info == nil || info.Defaults == nil {
		return user
	}
	d := info.Defaults
	out := user

	if len(out.Tools) == 0 && len(d.Tools) > 0 {
		out.Tools = append([]string{}, d.Tools...)
	}
	if out.MaxTurns == 0 {
		out.MaxTurns = d.MaxTurns
	}
	if out.MaxToolExecutions == 0 {
		out.MaxToolExecutions = d.MaxToolExecutions
	}
	if out.MaxIterations == 0 {
		out.MaxIterations = d.MaxIterations
	}
	if out.TimeoutSeconds == 0 {
		out.TimeoutSeconds = d.TimeoutSeconds
	}
	if out.Temperature == 0 {
		out.Temperature = d.Temperature
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = d.MaxTokens
	}
	if out.MaxContextTokens == 0 {
		out.MaxContextTokens = d.MaxContextTokens
	}
	if out.ReservedOutputTokens == 0 {
		out.ReservedOutputTokens = d.ReservedOutputTokens
	}
	if out.HistoryWindowSize == 0 {
		out.HistoryWindowSize = d.HistoryWindowSize
	}
	if out.OutputTokenCbThreshold == 0 {
		out.OutputTokenCbThreshold = d.OutputTokenCbThreshold
	}
	if out.QueryToolResultThresholdBytes == 0 {
		out.QueryToolResultThresholdBytes = d.QueryToolResultThresholdBytes
	}
	if out.MaxToolResults == 0 {
		out.MaxToolResults = d.MaxToolResults
	}
	if out.GraphMemoryBudgetPercent == 0 {
		out.GraphMemoryBudgetPercent = d.GraphMemoryBudgetPercent
	}
	if out.GraphMemoryMaxCandidates == 0 {
		out.GraphMemoryMaxCandidates = d.GraphMemoryMaxCandidates
	}
	if out.ThinkingLevel == "" {
		out.ThinkingLevel = d.ThinkingLevel
	}
	if out.WorkloadProfile == "" {
		out.WorkloadProfile = d.WorkloadProfile
	}
	if out.Rom == "" {
		out.Rom = d.Rom
	}
	if out.BackendType == "" {
		out.BackendType = d.BackendType
	}

	// Boolean optionals: take the preset's explicit setting whenever it
	// declared one. The caller is expected to pre-populate `user` with
	// any explicit user overrides before calling ApplyPreset; we cannot
	// distinguish "user explicitly set false" from "user didn't set"
	// on a Go bool, so the cloud convention applies — the preset wins
	// unless the caller intentionally pre-merged a user override.
	if d.CacheControlEnabled != nil {
		out.CacheControlEnabled = *d.CacheControlEnabled
	}
	if d.GraphMemoryEnabled != nil {
		out.GraphMemoryEnabled = *d.GraphMemoryEnabled
	}
	if d.AllowCodeExecution != nil {
		out.AllowCodeExecution = *d.AllowCodeExecution
	}
	return out
}

// ptrTrue / ptrFalse are tiny helpers to write proto3 optional bool fields
// inline in the registry initializer.
func ptrTrue() *bool  { v := true; return &v }
func ptrFalse() *bool { v := false; return &v }
