// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

func TestLoopFingerprint_Deterministic(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	h1, fields1 := LoopFingerprint(cfg)
	h2, _ := LoopFingerprint(cfg)
	assert.Equal(t, h1, h2, "same config must produce identical fingerprints")
	assert.NotEmpty(t, fields1)

	// A separately-constructed config with identical effective values must
	// hash identically (defaulting-path stability).
	manual := &Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		MaxIterations:     10,
		SystemPromptKey:   "different-key-not-in-fingerprint",
		EnableTracing:     true,
		EnableSelfHealing: true,
		Retry: RetryConfig{
			Enabled:      true,
			MaxRetries:   3,
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     5 * time.Second,
			Multiplier:   2.0,
		},
	}
	h3, _ := LoopFingerprint(manual)
	assert.Equal(t, h1, h3, "identical loop-shape values must hash identically regardless of construction path")
}

func TestLoopFingerprint_FieldSensitivity(t *testing.T) {
	t.Parallel()

	base, _ := LoopFingerprint(DefaultConfig())

	mutations := []struct {
		name   string
		mutate func(*Config)
	}{
		{"max_turns", func(c *Config) { c.MaxTurns = 26 }},
		{"max_tool_executions", func(c *Config) { c.MaxToolExecutions = 51 }},
		{"max_iterations", func(c *Config) { c.MaxIterations = 11 }},
		{"output_token_cb_threshold", func(c *Config) { c.OutputTokenCBThreshold = -1 }},
		{"max_context_tokens", func(c *Config) { c.MaxContextTokens = 100000 }},
		{"reserved_output_tokens", func(c *Config) { c.ReservedOutputTokens = 2048 }},
		{"enable_self_healing", func(c *Config) { c.EnableSelfHealing = false }},
		{"retry_enabled", func(c *Config) { c.Retry.Enabled = false }},
		{"retry_max_retries", func(c *Config) { c.Retry.MaxRetries = 7 }},
		{"retry_initial_delay", func(c *Config) { c.Retry.InitialDelay = 250 * time.Millisecond }},
		{"retry_max_delay", func(c *Config) { c.Retry.MaxDelay = 9 * time.Second }},
		{"retry_multiplier", func(c *Config) { c.Retry.Multiplier = 3.5 }},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			m.mutate(cfg)
			h, _ := LoopFingerprint(cfg)
			assert.NotEqual(t, base, h, "changing %s must change the fingerprint", m.name)
		})
	}

	// Non-loop-shape fields must NOT change the fingerprint.
	cfg := DefaultConfig()
	cfg.Name = "renamed"
	cfg.SystemPrompt = "different prompt"
	cfg.Description = "different description"
	h, _ := LoopFingerprint(cfg)
	assert.Equal(t, base, h, "identity/prompt fields are not loop shape")
}

func TestLoopFingerprint_VersionPrefixAndNil(t *testing.T) {
	t.Parallel()

	h, fields := LoopFingerprint(DefaultConfig())
	assert.True(t, strings.HasPrefix(h, "v1:"), "fingerprint must carry the algorithm version, got %q", h)
	assert.Len(t, strings.TrimPrefix(h, "v1:"), 64, "sha256 hex body")
	assert.Contains(t, fields, "max_turns")

	nilHash, nilFields := LoopFingerprint(nil)
	assert.Empty(t, nilHash)
	assert.Nil(t, nilFields)
}

// attrCapturingTracer records every span it starts so tests can inspect the
// attributes set on them.
type attrCapturingTracer struct {
	mu    sync.Mutex
	spans []*observability.Span
}

func (m *attrCapturingTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{Name: name, Attributes: map[string]interface{}{}}
	m.mu.Lock()
	m.spans = append(m.spans, span)
	m.mu.Unlock()
	return ctx, span
}

func (m *attrCapturingTracer) EndSpan(span *observability.Span)                            {}
func (m *attrCapturingTracer) RecordMetric(string, float64, map[string]string)             {}
func (m *attrCapturingTracer) RecordEvent(context.Context, string, map[string]interface{}) {}
func (m *attrCapturingTracer) Flush(context.Context) error                                 { return nil }

func (m *attrCapturingTracer) fingerprintAttrs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.spans {
		if v, ok := s.Attributes["config.loop_fingerprint"].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestChatEmitsStableLoopFingerprint(t *testing.T) {
	t.Parallel()

	tracer := &attrCapturingTracer{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithTracer(tracer))

	ctx := context.Background()
	_, err := ag.Chat(ctx, "s1", "hello")
	require.NoError(t, err)
	_, err = ag.Chat(ctx, "s1", "again")
	require.NoError(t, err)

	fps := tracer.fingerprintAttrs()
	require.GreaterOrEqual(t, len(fps), 2, "root span of each chat must carry config.loop_fingerprint")
	assert.True(t, strings.HasPrefix(fps[0], "v1:"))
	assert.Equal(t, fps[0], fps[1], "fingerprint must be stable across chats on the same agent")

	want, _ := LoopFingerprint(cfg)
	assert.Equal(t, want, fps[0], "emitted fingerprint matches direct computation")
}
