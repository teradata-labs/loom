// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/patterns"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// createPatternTestServer builds a MultiAgentServer with a test agent whose
// patterns directory contains the supplied YAML files.
// Returns the server and the temp patterns directory path.
func createPatternTestServer(t *testing.T, patternFiles map[string]string) *MultiAgentServer {
	t.Helper()

	tmpDir := t.TempDir()

	// Write pattern YAML files to the temp directory
	for name, content := range patternFiles {
		err := os.WriteFile(filepath.Join(tmpDir, name+".yaml"), []byte(content), 0600)
		require.NoError(t, err)
	}

	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}

	ag := agent.NewAgent(backend, llm, agent.WithConfig(&agent.Config{
		Name:        "test-agent",
		Description: "Test agent for pattern RPCs",
		PatternsDir: tmpDir,
	}))

	agents := map[string]*agent.Agent{
		"test-agent": ag,
	}
	srv := NewMultiAgentServer(agents, nil)
	srv.SetLogger(zaptest.NewLogger(t))

	return srv
}

// samplePatternYAML returns a valid pattern YAML for testing.
func samplePatternYAML(name, category, backendType string) string {
	return `name: ` + name + `
title: ` + name + ` Title
description: A test pattern for ` + name + `
category: ` + category + `
difficulty: beginner
backend_type: ` + backendType + `
use_cases:
  - testing
  - unit tests
parameters:
  - name: table_name
    type: string
    required: true
    description: The table to query
    example: my_table
examples:
  - name: basic usage
    description: Basic example of ` + name + `
    expected_result: "SELECT * FROM my_table"
templates:
  default:
    description: Default template
    content: "SELECT * FROM {{table_name}}"
`
}

// --- LoadPatterns Tests ---

func TestLoadPatterns(t *testing.T) {
	tests := []struct {
		name         string
		patterns     map[string]string
		req          *loomv1.LoadPatternsRequest
		wantErr      bool
		wantCode     codes.Code
		wantMinCount int32
	}{
		{
			name: "load patterns for specific agent by source",
			patterns: map[string]string{
				"analytics_query": samplePatternYAML("analytics_query", "analytics", "sql"),
				"ml_train":        samplePatternYAML("ml_train", "ml", "sql"),
			},
			req: &loomv1.LoadPatternsRequest{
				AgentId: "test-agent",
			},
			wantMinCount: 2,
		},
		{
			name:     "missing source and agent_id returns error",
			patterns: map[string]string{},
			req:      &loomv1.LoadPatternsRequest{},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "load patterns with force reload",
			patterns: map[string]string{
				"reload_test": samplePatternYAML("reload_test", "testing", "sql"),
			},
			req: &loomv1.LoadPatternsRequest{
				AgentId:     "test-agent",
				ForceReload: true,
			},
			wantMinCount: 1,
		},
		{
			name: "load patterns with domain filter",
			patterns: map[string]string{
				"sql_pattern":  samplePatternYAML("sql_pattern", "analytics", "sql"),
				"rest_pattern": samplePatternYAML("rest_pattern", "api", "rest"),
			},
			req: &loomv1.LoadPatternsRequest{
				AgentId: "test-agent",
				Domains: []string{"sql"},
			},
			wantMinCount: 1,
		},
		{
			name: "load patterns for nonexistent agent returns error",
			patterns: map[string]string{
				"some_pattern": samplePatternYAML("some_pattern", "test", "sql"),
			},
			req: &loomv1.LoadPatternsRequest{
				AgentId: "nonexistent-agent-id",
			},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name: "load patterns for all agents with source",
			patterns: map[string]string{
				"global_pattern": samplePatternYAML("global_pattern", "analytics", "sql"),
			},
			req: &loomv1.LoadPatternsRequest{
				Source: "/tmp/some-dir", // source provided, no agent_id = load all
			},
			// This may have errors since /tmp/some-dir might not exist for the agent,
			// but the agent will fall back to its own patterns dir.
			// We mainly verify it does not panic and returns a valid response.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := createPatternTestServer(t, tt.patterns)
			resp, err := srv.LoadPatterns(context.Background(), tt.req)

			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.GreaterOrEqual(t, resp.PatternsLoaded, tt.wantMinCount,
				"expected at least %d patterns loaded, got %d", tt.wantMinCount, resp.PatternsLoaded)
		})
	}
}

// --- ListPatterns Tests ---

func TestListPatterns(t *testing.T) {
	tests := []struct {
		name         string
		patterns     map[string]string
		req          *loomv1.ListPatternsRequest
		wantMinCount int32
		wantMaxCount int32
	}{
		{
			name: "list all patterns with no filters",
			patterns: map[string]string{
				"pattern_a": samplePatternYAML("pattern_a", "analytics", "sql"),
				"pattern_b": samplePatternYAML("pattern_b", "ml", "sql"),
				"pattern_c": samplePatternYAML("pattern_c", "etl", "rest"),
			},
			req:          &loomv1.ListPatternsRequest{},
			wantMinCount: 3,
		},
		{
			name: "filter by category",
			patterns: map[string]string{
				"analytics_1": samplePatternYAML("analytics_1", "analytics", "sql"),
				"analytics_2": samplePatternYAML("analytics_2", "analytics", "sql"),
				"ml_1":        samplePatternYAML("ml_1", "ml", "sql"),
			},
			req: &loomv1.ListPatternsRequest{
				Category: "analytics",
			},
			wantMinCount: 2,
			wantMaxCount: 2,
		},
		{
			name: "filter by domain (backend_type)",
			patterns: map[string]string{
				"sql_1":  samplePatternYAML("sql_1", "analytics", "sql"),
				"rest_1": samplePatternYAML("rest_1", "api", "rest"),
			},
			req: &loomv1.ListPatternsRequest{
				Domain: "rest",
			},
			wantMinCount: 1,
			wantMaxCount: 1,
		},
		{
			name: "search by keyword",
			patterns: map[string]string{
				"search_target": samplePatternYAML("search_target", "analytics", "sql"),
				"other_pattern": samplePatternYAML("other_pattern", "ml", "rest"),
			},
			req: &loomv1.ListPatternsRequest{
				Search: "search_target",
			},
			wantMinCount: 1,
		},
		{
			name:     "empty patterns returns empty list",
			patterns: map[string]string{},
			req:      &loomv1.ListPatternsRequest{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := createPatternTestServer(t, tt.patterns)
			resp, err := srv.ListPatterns(context.Background(), tt.req)

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, int32(len(resp.Patterns)), resp.TotalCount,
				"TotalCount should match number of returned patterns")
			assert.GreaterOrEqual(t, resp.TotalCount, tt.wantMinCount,
				"expected at least %d patterns, got %d", tt.wantMinCount, resp.TotalCount)

			if tt.wantMaxCount > 0 {
				assert.LessOrEqual(t, resp.TotalCount, tt.wantMaxCount,
					"expected at most %d patterns, got %d", tt.wantMaxCount, resp.TotalCount)
			}

			// Verify pattern proto conversion has required fields populated
			for _, p := range resp.Patterns {
				assert.NotEmpty(t, p.Name, "pattern name should not be empty")
			}
		})
	}
}

// --- GetPattern Tests ---

func TestGetPattern(t *testing.T) {
	tests := []struct {
		name     string
		patterns map[string]string
		req      *loomv1.GetPatternRequest
		wantErr  bool
		wantCode codes.Code
		wantName string
	}{
		{
			name: "get existing pattern by name",
			patterns: map[string]string{
				"my_pattern": samplePatternYAML("my_pattern", "analytics", "sql"),
			},
			req:      &loomv1.GetPatternRequest{Name: "my_pattern"},
			wantName: "my_pattern",
		},
		{
			name: "get nonexistent pattern returns NotFound",
			patterns: map[string]string{
				"existing": samplePatternYAML("existing", "analytics", "sql"),
			},
			req:      &loomv1.GetPatternRequest{Name: "nonexistent_pattern"},
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name:     "empty name returns InvalidArgument",
			patterns: map[string]string{},
			req:      &loomv1.GetPatternRequest{Name: ""},
			wantErr:  true,
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := createPatternTestServer(t, tt.patterns)
			resp, err := srv.GetPattern(context.Background(), tt.req)

			if tt.wantErr {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.wantName, resp.Name)
			assert.NotEmpty(t, resp.Description)
			assert.NotEmpty(t, resp.Category)
		})
	}
}

// --- Conversion Tests ---

func TestInternalPatternToProto(t *testing.T) {
	tests := []struct {
		name    string
		pattern *patternTestInput
		checks  func(t *testing.T, proto *loomv1.Pattern)
	}{
		{
			name: "full pattern conversion",
			pattern: &patternTestInput{
				name:            "full_pattern",
				category:        "analytics",
				backendType:     "sql",
				description:     "A full test pattern",
				backendFunction: "NPATH",
				difficulty:      "advanced",
				useCases:        []string{"session analysis", "clickstream"},
				paramCount:      2,
				exampleCount:    1,
			},
			checks: func(t *testing.T, proto *loomv1.Pattern) {
				assert.Equal(t, "full_pattern", proto.Name)
				assert.Equal(t, "sql", proto.Domain)
				assert.Equal(t, "analytics", proto.Category)
				assert.Equal(t, "A full test pattern", proto.Description)
				assert.Len(t, proto.Parameters, 2)
				assert.Len(t, proto.Examples, 1)
				assert.Equal(t, "NPATH", proto.BackendHints["backend_function"])
				assert.Equal(t, "advanced", proto.BackendHints["difficulty"])
				assert.Contains(t, proto.Tags, "session analysis")
				assert.Contains(t, proto.Tags, "clickstream")
			},
		},
		{
			name: "minimal pattern conversion",
			pattern: &patternTestInput{
				name:     "minimal",
				category: "test",
			},
			checks: func(t *testing.T, proto *loomv1.Pattern) {
				assert.Equal(t, "minimal", proto.Name)
				assert.Equal(t, "test", proto.Category)
				assert.Nil(t, proto.Parameters)
				assert.Nil(t, proto.Examples)
				assert.Nil(t, proto.BackendHints)
				assert.Nil(t, proto.Tags)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			internal := buildInternalPattern(tt.pattern)
			proto := internalPatternToProto(internal)
			tt.checks(t, proto)
		})
	}
}

func TestSummaryToProto(t *testing.T) {
	tests := []struct {
		name    string
		summary *patternSummaryTestInput
		checks  func(t *testing.T, proto *loomv1.Pattern)
	}{
		{
			name: "summary with all fields",
			summary: &patternSummaryTestInput{
				name:            "summary_full",
				category:        "ml",
				backendType:     "sql",
				description:     "ML summary",
				backendFunction: "DecisionForest",
				difficulty:      "intermediate",
				useCases:        []string{"classification"},
			},
			checks: func(t *testing.T, proto *loomv1.Pattern) {
				assert.Equal(t, "summary_full", proto.Name)
				assert.Equal(t, "sql", proto.Domain)
				assert.Equal(t, "ml", proto.Category)
				assert.Equal(t, "ML summary", proto.Description)
				assert.Equal(t, "DecisionForest", proto.BackendHints["backend_function"])
				assert.Contains(t, proto.Tags, "classification")
			},
		},
		{
			name: "summary with minimal fields",
			summary: &patternSummaryTestInput{
				name: "summary_min",
			},
			checks: func(t *testing.T, proto *loomv1.Pattern) {
				assert.Equal(t, "summary_min", proto.Name)
				assert.Nil(t, proto.BackendHints)
				assert.Nil(t, proto.Tags)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := buildPatternSummary(tt.summary)
			proto := summaryToProto(summary)
			tt.checks(t, proto)
		})
	}
}

// --- GetPattern with full field validation ---

func TestGetPattern_FullFieldConversion(t *testing.T) {
	patternYAML := `name: field_test
title: Field Conversion Test
description: Tests that all fields convert correctly to proto
category: analytics
difficulty: advanced
backend_type: sql
backend_function: NPATH
use_cases:
  - session analysis
  - path detection
related_patterns:
  - sessionize
parameters:
  - name: input_table
    type: string
    required: true
    description: Source table name
    example: clickstream
    default: events
  - name: pattern_expr
    type: string
    required: true
    description: Pattern expression
examples:
  - name: basic npath
    description: Simple path analysis
    expected_result: "NPATH output"
best_practices: "Always partition by session_id"
templates:
  default:
    description: Default npath template
    content: "SELECT * FROM NPATH(...)"
`

	srv := createPatternTestServer(t, map[string]string{
		"field_test": patternYAML,
	})

	resp, err := srv.GetPattern(context.Background(), &loomv1.GetPatternRequest{
		Name: "field_test",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify all proto fields
	assert.Equal(t, "field_test", resp.Name)
	assert.Equal(t, "sql", resp.Domain)
	assert.Equal(t, "analytics", resp.Category)
	assert.Equal(t, "Tests that all fields convert correctly to proto", resp.Description)

	// Parameters
	require.Len(t, resp.Parameters, 2)
	assert.Equal(t, "input_table", resp.Parameters[0].Name)
	assert.Equal(t, "string", resp.Parameters[0].Type)
	assert.True(t, resp.Parameters[0].Required)
	assert.Equal(t, "Source table name", resp.Parameters[0].Description)
	assert.Equal(t, "events", resp.Parameters[0].DefaultValue)

	assert.Equal(t, "pattern_expr", resp.Parameters[1].Name)

	// Examples
	require.Len(t, resp.Examples, 1)
	assert.Equal(t, "basic npath", resp.Examples[0].Input) // Name maps to Input
	assert.Equal(t, "NPATH output", resp.Examples[0].Output)
	assert.Equal(t, "Simple path analysis", resp.Examples[0].Description)

	// Backend hints
	require.NotNil(t, resp.BackendHints)
	assert.Equal(t, "NPATH", resp.BackendHints["backend_function"])
	assert.Equal(t, "advanced", resp.BackendHints["difficulty"])
	assert.Equal(t, "Always partition by session_id", resp.BackendHints["best_practices"])

	// Tags (use_cases + related_patterns)
	assert.Contains(t, resp.Tags, "session analysis")
	assert.Contains(t, resp.Tags, "path detection")
	assert.Contains(t, resp.Tags, "sessionize")
}

// --- Helper types for table-driven conversion tests ---

type patternTestInput struct {
	name            string
	category        string
	backendType     string
	description     string
	backendFunction string
	difficulty      string
	useCases        []string
	paramCount      int
	exampleCount    int
}

type patternSummaryTestInput struct {
	name            string
	category        string
	backendType     string
	description     string
	backendFunction string
	difficulty      string
	useCases        []string
}

func buildInternalPattern(input *patternTestInput) *patterns.Pattern {
	p := &patterns.Pattern{
		Name:            input.name,
		Category:        input.category,
		BackendType:     input.backendType,
		Description:     input.description,
		BackendFunction: input.backendFunction,
		Difficulty:      input.difficulty,
		UseCases:        input.useCases,
	}

	for i := 0; i < input.paramCount; i++ {
		p.Parameters = append(p.Parameters, patterns.Parameter{
			Name:        fmt.Sprintf("param_%d", i),
			Type:        "string",
			Required:    i == 0,
			Description: fmt.Sprintf("Parameter %d", i),
		})
	}

	for i := 0; i < input.exampleCount; i++ {
		p.Examples = append(p.Examples, patterns.Example{
			Name:           fmt.Sprintf("example_%d", i),
			Description:    fmt.Sprintf("Example %d", i),
			ExpectedResult: fmt.Sprintf("result_%d", i),
		})
	}

	return p
}

func buildPatternSummary(input *patternSummaryTestInput) *patterns.PatternSummary {
	return &patterns.PatternSummary{
		Name:            input.name,
		Category:        input.category,
		BackendType:     input.backendType,
		Description:     input.description,
		BackendFunction: input.backendFunction,
		Difficulty:      input.difficulty,
		UseCases:        input.useCases,
	}
}
