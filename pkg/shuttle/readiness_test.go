// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// readinessMockTool is a mockTool that also reports readiness.
type readinessMockTool struct {
	mockTool
	readiness shuttle.ToolReadiness
}

func (m *readinessMockTool) Readiness(_ context.Context) shuttle.ToolReadiness {
	return m.readiness
}

func TestReadinessOf(t *testing.T) {
	plain := &mockTool{name: "plain"}
	_, ok := shuttle.ReadinessOf(context.Background(), plain)
	assert.False(t, ok, "a tool without Readiness reports nothing")

	reporting := &readinessMockTool{
		mockTool:  mockTool{name: "reporting"},
		readiness: shuttle.ToolReadiness{State: shuttle.ReadinessDegraded, Summary: "limited"},
	}
	readiness, ok := shuttle.ReadinessOf(context.Background(), reporting)
	require.True(t, ok)
	assert.Equal(t, shuttle.ReadinessDegraded, readiness.State)
	assert.Equal(t, "limited", readiness.Summary)
}

func TestPromptAwareTool_AppendsReadinessToRegistryDescription(t *testing.T) {
	registry := &mockRegistry{
		getFunc: func(_ context.Context, _ string, _ map[string]interface{}) (string, error) {
			return "Static prompt description.\n", nil
		},
	}

	tests := []struct {
		name      string
		tool      shuttle.Tool
		wantDesc  string
		wantState shuttle.ReadinessState
	}{
		{
			name: "reporting tool gets its summary appended",
			tool: &readinessMockTool{
				mockTool:  mockTool{name: "reporting", description: "native"},
				readiness: shuttle.ToolReadiness{State: shuttle.ReadinessDegraded, Summary: "Provider status: limited."},
			},
			wantDesc:  "Static prompt description.\n\nProvider status: limited.",
			wantState: shuttle.ReadinessDegraded,
		},
		{
			name:      "plain tool keeps the registry description unchanged",
			tool:      &mockTool{name: "plain", description: "native"},
			wantDesc:  "Static prompt description.\n",
			wantState: shuttle.ReadinessReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := shuttle.NewPromptAwareTool(tt.tool, registry, "tools.test")
			assert.Equal(t, tt.wantDesc, wrapped.Description())

			// Wrapping must not hide readiness from callers such as the
			// registration-time warning.
			readiness, ok := shuttle.ReadinessOf(context.Background(), wrapped)
			require.True(t, ok)
			assert.Equal(t, tt.wantState, readiness.State)
		})
	}
}
