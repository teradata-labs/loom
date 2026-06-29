// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package discovery

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/binding"
)

// capturingTracer embeds NoOpTracer (which already returns real, inspectable
// spans) and records each span it starts, keyed by name, so a test can assert
// on the attributes a function set on its span.
type capturingTracer struct {
	*observability.NoOpTracer
	mu    sync.Mutex
	spans map[string]*observability.Span
}

func newCapturingTracer() *capturingTracer {
	return &capturingTracer{
		NoOpTracer: observability.NewNoOpTracer(),
		spans:      map[string]*observability.Span{},
	}
}

func (c *capturingTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	ctx, sp := c.NoOpTracer.StartSpan(ctx, name, opts...)
	c.mu.Lock()
	c.spans[name] = sp
	c.mu.Unlock()
	return ctx, sp
}

func (c *capturingTracer) span(name string) *observability.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spans[name]
}

func TestRecordActivatedSkills_SetsAttributes(t *testing.T) {
	span := &observability.Span{}
	recordActivatedSkills(span, []*Candidate{
		{Skill: &skills.Skill{Name: "sql-helper"}, TriggerType: "router", Confidence: 0.85},
		{Skill: &skills.Skill{Name: "guardrails"}, TriggerType: "always", Confidence: 1.0},
	})

	assert.Equal(t, 2, span.Attributes["skills.activated.count"])
	assert.Equal(t, "sql-helper,guardrails", span.Attributes["skills.activated.names"])

	detail, ok := span.Attributes["skills.activated.detail"].(string)
	require.True(t, ok, "detail attribute should be a JSON string")
	var parsed []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(detail), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, "sql-helper", parsed[0]["name"])
	assert.Equal(t, "router", parsed[0]["trigger"])
}

func TestRecordActivatedSkills_NilSpanAndEmpty(t *testing.T) {
	// nil span must not panic (NoOpTracer/real tracer can yield nil spans).
	recordActivatedSkills(nil, []*Candidate{{Skill: &skills.Skill{Name: "x"}}})

	// Empty candidate set records a zero count and no name/detail keys.
	span := &observability.Span{}
	recordActivatedSkills(span, nil)
	assert.Equal(t, 0, span.Attributes["skills.activated.count"])
	_, hasNames := span.Attributes["skills.activated.names"]
	assert.False(t, hasNames)
}

// TestDiscovery_RecordsActivatedSkillsOnSpan proves the wiring end to end:
// Discover() stamps the skills it returns onto the skills.discovery.discover
// span, which is what the loom-cloud exporter later persists into
// llm_spans.attributes.
func TestDiscovery_RecordsActivatedSkillsOnSpan(t *testing.T) {
	a := mkSkill("a", "", []string{"/a"}, []string{"alpha"})
	b := mkSkill("b", "", []string{"/b"}, []string{"beta"})
	lib := libraryWith(t, a, b)
	tr := newCapturingTracer()
	d := New(lib, binding.NewResolver(lib), WithTracer(tr))

	cfg := &skills.SkillsConfig{
		Enabled: true,
		Bindings: []skills.SkillBinding{
			{Name: "a", Mode: skills.BindingLazy},
			{Name: "b", Mode: skills.BindingLazy},
		},
	}
	got, err := d.Discover(context.Background(), "s", "/a please", cfg)
	require.NoError(t, err)
	require.Len(t, got, 1)

	span := tr.span("skills.discovery.discover")
	require.NotNil(t, span, "discovery span should have been started")
	assert.Equal(t, 1, span.Attributes["skills.activated.count"])
	assert.Equal(t, "a", span.Attributes["skills.activated.names"])
}
