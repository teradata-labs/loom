// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package main

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// hasTool reports whether ag has the named tool registered.
func hasTool(ag *agent.Agent, name string) bool {
	for _, n := range ag.ListTools() {
		if n == name {
			return true
		}
	}
	return false
}

// withViperMinimalMode sets tools.minimal / tools.none for the duration of the
// test and resets viper afterwards. Tests using this helper cannot run in
// parallel because viper is process-global.
func withViperMinimalMode(t *testing.T, minimal, none bool) {
	t.Helper()
	viper.Reset()
	viper.Set("tools.minimal", minimal)
	viper.Set("tools.none", none)
	t.Cleanup(func() { viper.Reset() })
}

func newCfg(builtin ...string) *loomv1.AgentConfig {
	return &loomv1.AgentConfig{
		Tools: &loomv1.ToolsConfig{
			Builtin: builtin,
		},
	}
}

// TestRegisterYAMLBuiltinTools_DefaultMode_DropsAutoRegistered pins the
// happy-path behaviour: when tools.minimal/none are both off, the auto
// registration path is going to register shell_execute / workspace /
// tool_search elsewhere, so registerYAMLBuiltinTools must skip the YAML
// duplicate. Non-auto tools (http_request) are registered as usual.
func TestRegisterYAMLBuiltinTools_DefaultMode_DropsAutoRegistered(t *testing.T) {
	withViperMinimalMode(t, false, false)
	ag := agent.NewAgent(nil, nil)
	cfg := newCfg("shell_execute", "http_request")

	registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "  ", "agent_management")

	assert.False(t, hasTool(ag, "shell_execute"),
		"shell_execute must be skipped under default mode; the auto-registration path handles it")
	assert.True(t, hasTool(ag, "http_request"),
		"http_request must be registered from the YAML path")
}

// TestRegisterYAMLBuiltinTools_MinimalMode_HonoursYAMLShellExecute is the
// regression test for the Copilot-flagged hot-reload bug: under
// tools.minimal=true, the auto-registration path does NOT add shell_execute,
// so a YAML declaration MUST be honoured instead. Before the helper
// extraction, the hot-reload path short-circuited on toolName=="shell_execute"
// unconditionally and silently dropped the YAML tool on reload.
func TestRegisterYAMLBuiltinTools_MinimalMode_HonoursYAMLShellExecute(t *testing.T) {
	withViperMinimalMode(t, true, false)
	ag := agent.NewAgent(nil, nil)
	cfg := newCfg("shell_execute", "http_request")

	registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "  ", "agent_management")

	assert.True(t, hasTool(ag, "shell_execute"),
		"YAML-declared shell_execute MUST be registered under tools.minimal — this is the hot-reload regression fix")
	assert.True(t, hasTool(ag, "http_request"),
		"non-auto-registered tool must always be honoured")
}

// TestRegisterYAMLBuiltinTools_NoneMode_HonoursYAMLAutoTools verifies the same
// invariant for tools.none. Also covers workspace and tool_search since they
// were missing from the old hot-reload skip set entirely.
func TestRegisterYAMLBuiltinTools_NoneMode_HonoursYAMLAutoTools(t *testing.T) {
	withViperMinimalMode(t, false, true)
	ag := agent.NewAgent(nil, nil)
	cfg := newCfg("shell_execute", "workspace", "tool_search", "http_request")

	registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "  ", "agent_management")

	// tool_search and workspace cannot be constructed via builtin.ByName
	// (they need toolRegistry / artifactStore), so they fall through the
	// helper and emit "Unknown builtin tool" warnings — that's the correct
	// behaviour because the agent shouldn't get a half-wired tool. The
	// auto-registration path is the right surface for them when the
	// dependencies are available.
	assert.True(t, hasTool(ag, "shell_execute"),
		"YAML-declared shell_execute must be registered under tools.none")
	assert.True(t, hasTool(ag, "http_request"),
		"non-auto-registered tool must be honoured")
}

// TestRegisterYAMLBuiltinTools_OtherMechanismTools_AlwaysSkipped pins the
// (B) semantic for tools.none: tools wired through their own subsystems
// (memory/swap, communication, presentation, etc.) cannot be constructed by
// builtin.ByName and remain gated by subsystem availability. YAML mentions of
// them are metadata only and must be skipped regardless of tools.minimal/none.
func TestRegisterYAMLBuiltinTools_OtherMechanismTools_AlwaysSkipped(t *testing.T) {
	subsystemWired := []string{
		"recall_conversation",
		"send_message",
		"shared_memory_read",
		"shared_memory_write",
		"delegate_to_agent",
		"contact_human",
		"top_n_query",
		"create_ui_app",
	}
	for _, name := range subsystemWired {
		name := name
		t.Run(name, func(t *testing.T) {
			withViperMinimalMode(t, false, true)
			ag := agent.NewAgent(nil, nil)
			cfg := newCfg(name)

			registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "  ", "agent_management")

			assert.False(t, hasTool(ag, name),
				"%s must NOT be registered from the YAML path; it requires subsystem wiring", name)
		})
	}
}

// TestRegisterYAMLBuiltinTools_EmptyConfig_NoOp guards against nil-deref on a
// brand-new agent with no Tools section in the YAML.
func TestRegisterYAMLBuiltinTools_EmptyConfig_NoOp(t *testing.T) {
	withViperMinimalMode(t, false, false)
	ag := agent.NewAgent(nil, nil)

	// nil Tools
	registerYAMLBuiltinTools(ag, &loomv1.AgentConfig{}, nil, zap.NewNop(), "  ", "agent_management")
	assert.Empty(t, ag.ListTools(), "no Tools section in YAML means no tools registered from the helper")

	// empty Builtin slice
	registerYAMLBuiltinTools(ag, newCfg(), nil, zap.NewNop(), "  ", "agent_management")
	assert.Empty(t, ag.ListTools(), "empty Builtin slice is a no-op")
}

// TestRegisterYAMLBuiltinTools_UnknownTool_NotRegistered verifies that a
// genuinely unknown tool name in YAML produces a warning and no registration,
// matching the cold-start behaviour before the refactor.
func TestRegisterYAMLBuiltinTools_UnknownTool_NotRegistered(t *testing.T) {
	withViperMinimalMode(t, false, false)
	ag := agent.NewAgent(nil, nil)
	cfg := newCfg("not_a_real_tool")

	registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "  ", "agent_management")

	assert.False(t, hasTool(ag, "not_a_real_tool"))
}
