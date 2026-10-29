// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// recordingBuilder is a ProviderBuilder that records every config it is asked
// to build and returns a fresh scripted provider.
type recordingBuilder struct {
	mu    sync.Mutex
	calls []*loomv1.LLMConfig
}

func (b *recordingBuilder) build(cfg *loomv1.LLMConfig) (LLMProvider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, cfg)
	return &scriptedStopReasonLLM{}, nil
}

func (b *recordingBuilder) providers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, c := range b.calls {
		out = append(out, c.Provider+"/"+c.Model)
	}
	return out
}

func newBuilderRegistry(t *testing.T, builder ProviderBuilder, def LLMProvider) *Registry {
	t.Helper()
	dir := t.TempDir()
	reg, err := NewRegistry(RegistryConfig{
		ConfigDir:       dir,
		DBPath:          filepath.Join(dir, "registry.db"),
		Logger:          zap.NewNop(),
		LLMProvider:     def,
		ProviderBuilder: builder,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

// An agent created at runtime with its own llm block (and role LLMs) gets its
// providers from the host's builder — the path POST /v1/agents takes.
func TestRegistry_CreateAgentUsesProviderBuilder(t *testing.T) {
	builder := &recordingBuilder{}
	reg := newBuilderRegistry(t, builder.build, &scriptedStopReasonLLM{})

	reg.RegisterConfig(&loomv1.AgentConfig{
		Name: "runtime-agent",
		Llm: &loomv1.LLMConfig{
			Provider: "bedrock",
			Model:    "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		},
		JudgeLlm: &loomv1.LLMConfig{
			Provider: "bedrock",
			Model:    "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		},
	})
	_, err := reg.CreateAgent(context.Background(), "runtime-agent")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"bedrock/us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0",
	}, builder.providers(), "main and judge LLMs are both built by the host builder")
}

// The builder only replaces the environment-only fallback: a pool entry and
// the registry default keep precedence.
func TestRegistry_ProviderBuilderPrecedence(t *testing.T) {
	builder := &recordingBuilder{}
	def := &scriptedStopReasonLLM{}
	reg := newBuilderRegistry(t, builder.build, def)
	pooled := &scriptedStopReasonLLM{}
	reg.SetProviderPool(map[string]LLMProvider{"fast": pooled})

	got, err := reg.createLLMProvider(&loomv1.LLMConfig{Provider: "fast"})
	require.NoError(t, err)
	assert.Same(t, pooled, got, "a pool entry wins over the builder")

	got, err = reg.createLLMProvider(&loomv1.LLMConfig{Model: "ignored"})
	require.NoError(t, err)
	assert.Same(t, def, got, "no provider named: the registry default is used")

	assert.Empty(t, builder.providers(), "neither case reaches the builder")
}

// Without a builder, a Bedrock config with no AWS_PROFILE must not be forced
// onto profile "default", which fails on hosts without ~/.aws/config
// ("failed to get shared config profile, default").
func TestRegistry_BedrockFallbackDoesNotForceDefaultProfile(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(empty, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(empty, "credentials"))
	t.Setenv("AWS_REGION", "us-west-2")

	reg := newBuilderRegistry(t, nil, nil)
	provider, err := reg.createLLMProvider(&loomv1.LLMConfig{
		Provider: "bedrock",
		Model:    "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	})
	require.NoError(t, err)
	require.NotNil(t, provider)
}
