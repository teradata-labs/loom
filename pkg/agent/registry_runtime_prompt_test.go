// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestBuildAgent_RuntimePromptMatchesConfig(t *testing.T) {
	const (
		systemPrompt = "Score each Mastermind guess and report the resulting pegs."
		description  = "Mastermind pilot agent"
	)
	tests := []struct {
		name        string
		behavior    *loomv1.BehaviorConfig
		rom         string
		metadata    map[string]string
		wantBaseROM bool
		wantTDROM   bool
	}{
		{name: "behavior block", behavior: &loomv1.BehaviorConfig{MaxTurns: 25}, wantBaseROM: true},
		{name: "no behavior block", wantBaseROM: true},
		{name: "no ROM", behavior: &loomv1.BehaviorConfig{}, rom: "none"},
		{name: "Teradata ROM", behavior: &loomv1.BehaviorConfig{}, rom: "TD", wantBaseROM: true, wantTDROM: true},
		{
			name:        "backend path auto-detects ROM",
			behavior:    &loomv1.BehaviorConfig{},
			metadata:    map[string]string{"backend_path": "backends/teradata-prod.yaml"},
			wantBaseROM: true,
			wantTDROM:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, _ := createTestRegistry(t)
			config := &loomv1.AgentConfig{
				Name:         "runtime-prompt-agent",
				Description:  description,
				SystemPrompt: systemPrompt,
				Rom:          tt.rom,
				Metadata:     tt.metadata,
				Llm:          &loomv1.LLMConfig{},
				Memory:       &loomv1.MemoryConfig{Type: "memory", MaxHistory: 50},
				Behavior:     tt.behavior,
			}
			registry.RegisterConfig(config)
			ag, err := registry.CreateAgent(context.Background(), config.Name)
			require.NoError(t, err)

			assert.Equal(t, description, ag.GetDescription())
			assert.Equal(t, DefaultConfig().MaxIterations, ag.GetConfig().MaxIterations)
			prompt := ag.getSystemPrompt(context.Background())
			assert.Contains(t, prompt, systemPrompt)
			if tt.wantBaseROM {
				assert.Contains(t, prompt, baseROM)
			} else {
				assert.NotContains(t, prompt, baseROM)
			}
			if tt.wantTDROM {
				assert.Contains(t, prompt, teradataROM)
			} else {
				assert.NotContains(t, prompt, teradataROM)
			}
			if tt.metadata != nil {
				tt.metadata["backend_path"] = "backends/other.yaml"
				assert.Contains(t, ag.getSystemPrompt(context.Background()), teradataROM,
					"the running agent must not share the caller's metadata map")
			}
		})
	}
}

func TestBuildAgent_BehaviorStillApplied(t *testing.T) {
	registry, _ := createTestRegistry(t)
	ag, err := registry.buildAgent(context.Background(), &loomv1.AgentConfig{
		Name:         "behavior-agent",
		SystemPrompt: "Summarize the ticket.",
		Llm:          &loomv1.LLMConfig{},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:          2,
			MaxToolExecutions:      7,
			MaxTurns:               3,
			OutputTokenCbThreshold: 4,
			Patterns: &loomv1.PatternConfig{
				Enabled:            true,
				MinConfidence:      0.9,
				MaxPatternsPerTurn: 2,
			},
		},
		Metadata: map[string]string{"_enable_self_healing": "false"},
	})
	require.NoError(t, err)

	cfg := ag.GetConfig()
	assert.Equal(t, "behavior-agent", cfg.Name)
	assert.Equal(t, "Summarize the ticket.", cfg.SystemPrompt)
	assert.Equal(t, 2, cfg.MaxIterations)
	assert.Equal(t, 7, cfg.MaxToolExecutions)
	assert.Equal(t, 3, cfg.MaxTurns)
	assert.Equal(t, 4, cfg.OutputTokenCBThreshold)
	assert.False(t, cfg.EnableSelfHealing)
	require.NotNil(t, cfg.PatternConfig)
	assert.InDelta(t, 0.9, cfg.PatternConfig.MinConfidence, 0.001)
	assert.Equal(t, 2, cfg.PatternConfig.MaxPatternsPerTurn)
}

func TestBuildAgent_RuntimeConfigKeepsDefaults(t *testing.T) {
	tests := []struct {
		name     string
		behavior *loomv1.BehaviorConfig
	}{
		{name: "behavior block", behavior: &loomv1.BehaviorConfig{MaxTurns: 3}},
		{name: "no behavior block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, _ := createTestRegistry(t)
			ag, err := registry.buildAgent(context.Background(), &loomv1.AgentConfig{
				Name:     "defaults-agent",
				Llm:      &loomv1.LLMConfig{},
				Behavior: tt.behavior,
				Metadata: map[string]string{
					"_enable_self_healing": "false",
					"_skills_config":       `{"Enabled":false,"DisabledSkills":["triage"]}`,
				},
			})
			require.NoError(t, err)

			cfg := ag.GetConfig()
			assert.Equal(t, DefaultConfig().Retry, cfg.Retry, "LLM retry must keep its defaults")
			assert.False(t, cfg.EnableSelfHealing, "_enable_self_healing must apply with or without a behavior block")
			require.NotNil(t, cfg.SkillsConfig, "the skills config must reach the runtime config")
			assert.Equal(t, []string{"triage"}, cfg.SkillsConfig.DisabledSkills)
		})
	}
}

func TestBuildAgent_ProtoSkillsConfigReachesRuntime(t *testing.T) {
	registry, _ := createTestRegistry(t)
	cfgProto := &loomv1.AgentConfig{
		Name:   "proto-skills-agent",
		Llm:    &loomv1.LLMConfig{},
		Skills: &loomv1.SkillsConfig{Enabled: true, DisabledSkills: []string{"triage"}, SkillsDir: t.TempDir()},
	}
	ag, err := registry.buildAgent(context.Background(), cfgProto)
	require.NoError(t, err)

	cfg := ag.GetConfig()
	require.NotNil(t, cfg.SkillsConfig)
	assert.True(t, cfg.SkillsConfig.Enabled)
	assert.Equal(t, []string{"triage"}, cfg.SkillsConfig.DisabledSkills)
}
