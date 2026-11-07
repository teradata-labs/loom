// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

type servedToolsLLM struct {
	tools []string
}

func (p *servedToolsLLM) Name() string  { return "test" }
func (p *servedToolsLLM) Model() string { return "test" }
func (p *servedToolsLLM) Chat(_ context.Context, _ []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	p.tools = nil
	for _, tool := range tools {
		p.tools = append(p.tools, tool.Name())
	}
	return &types.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
}

func TestNewServedAgent_LoadAndReloadToolPolicy(t *testing.T) {
	withViperMinimalMode(t, false, true)
	path := filepath.Join(t.TempDir(), "agent.yaml")
	var previous *agent.Agent

	// Both serve loading paths use newServedAgent. Reloading from the same YAML
	// must honor changes in either direction, independently of the old instance.
	for _, step := range []struct {
		name     string
		tools    string
		disabled bool
	}{
		{"initial explicit tools", "builtin: [http_request]", false},
		{"reload disabled", "disabled: true", true},
		{"reload enabled", "disabled: false\n    builtin: [http_request]", false},
	} {
		t.Run(step.name, func(t *testing.T) {
			yaml := "agent:\n  name: served-test\n  llm:\n    provider: anthropic\n    model: test\n  tools:\n    " + step.tools + "\n"
			require.NoError(t, os.WriteFile(path, []byte(yaml), 0600))
			cfg, err := agent.LoadAgentConfig(path)
			require.NoError(t, err)
			provider := &servedToolsLLM{}
			ag := newServedAgent(cfg, nil, provider, agent.WithName(cfg.Name))
			if previous != nil {
				assert.NotSame(t, previous, ag)
			}
			registerYAMLBuiltinTools(ag, cfg, nil, zap.NewNop(), "", "test")

			// Server wiring after construction must not restore tools on the
			// disabled replacement. Ordinary agents still accept that wiring.
			ag.RegisterTool(builtin.ByName("http_request"))
			ag.RegisterLazyTools([]shuttle.Tool{builtin.ByName("shell_execute")}, func(string) bool { return true })
			_, err = ag.Chat(context.Background(), "served-tools-session", "hello")
			require.NoError(t, err)
			if step.disabled {
				assert.Empty(t, ag.ListTools())
				assert.Empty(t, provider.tools, "disabled agents must send no tools to the provider")
			} else {
				assert.Contains(t, provider.tools, "http_request", "server tools.none preserves explicit tools")
			}
			previous = ag
		})
	}
}

func TestNewServedAgent_DisabledAtInitialLoad(t *testing.T) {
	withViperMinimalMode(t, false, false)
	path := filepath.Join(t.TempDir(), "disabled.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  name: disabled\n  tools:\n    disabled: true\n"), 0600))
	cfg, err := agent.LoadAgentConfig(path)
	require.NoError(t, err)
	ag := newServedAgent(cfg, nil, nil)
	assert.Empty(t, ag.ListTools(), "the default server mode must honor per-agent disabling")

	ordinary := newServedAgent(nil, nil, nil)
	assert.NotEmpty(t, ordinary.ListTools(), "omitting per-agent config must keep default tools")
}
