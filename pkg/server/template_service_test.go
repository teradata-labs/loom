// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestTemplateService_ListAgentPresets exercises the read-only listing.
// Pure metadata RPC; we only check it returns the full registry.
func TestTemplateService_ListAgentPresets(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	res, err := srv.ListAgentPresets(context.Background(), &loomv1.ListAgentPresetsRequest{})
	require.NoError(t, err)
	require.NotNil(t, res)
	// 8 presets shipped — every enum value except UNSPECIFIED.
	assert.Len(t, res.Presets, 8, "registry must return all 8 presets")

	// Spot-check that the response carries the full preset shape, not
	// just an enum + name. The defaults block is what the weaver / UI
	// actually needs to render the preset card.
	var research *loomv1.AgentPresetInfo
	for _, p := range res.Presets {
		if p.Preset == loomv1.AgentPreset_AGENT_PRESET_RESEARCH_ANALYST {
			research = p
			break
		}
	}
	require.NotNil(t, research, "research_analyst must be in the response")
	require.NotNil(t, research.Defaults)
	assert.NotEmpty(t, research.Defaults.Tools)
	assert.NotEmpty(t, research.DisplayName)
}

// TestTemplateService_ListWorkflowTemplates parallels the preset test for
// the workflow template registry.
func TestTemplateService_ListWorkflowTemplates(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	res, err := srv.ListWorkflowTemplates(context.Background(), &loomv1.ListWorkflowTemplatesRequest{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Len(t, res.Templates, 6, "registry must return all 6 workflow templates")

	for _, tmpl := range res.Templates {
		require.NotNil(t, tmpl.DefaultWorkflowPattern,
			"template %v must carry a default pattern", tmpl.Template)
		assert.NotEmpty(t, tmpl.Agents, "template %v must list agent specs", tmpl.Template)
	}
}

// TestTemplateService_CreateWorkflowFromTemplate_Validation locks in the
// argument validation contract — both required fields must error with
// InvalidArgument, and unknown templates with NotFound. These are the
// surface errors the weaver / UI actually need to handle.
func TestTemplateService_CreateWorkflowFromTemplate_Validation(t *testing.T) {
	srv := newTestMultiAgentServer(t)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *loomv1.CreateWorkflowFromTemplateRequest
		want codes.Code
	}{
		{
			name: "missing template",
			req:  &loomv1.CreateWorkflowFromTemplateRequest{WorkflowName: "my-flow"},
			want: codes.InvalidArgument,
		},
		{
			name: "missing workflow_name",
			req: &loomv1.CreateWorkflowFromTemplateRequest{
				Template: loomv1.WorkflowTemplate_WORKFLOW_TEMPLATE_RESEARCH_REPORT,
			},
			want: codes.InvalidArgument,
		},
		{
			name: "unknown template enum",
			req: &loomv1.CreateWorkflowFromTemplateRequest{
				Template:     loomv1.WorkflowTemplate(99),
				WorkflowName: "x",
			},
			want: codes.NotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.CreateWorkflowFromTemplate(ctx, tc.req)
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "must be a gRPC status error")
			assert.Equal(t, tc.want, st.Code(), "unexpected status code: %v", err)
		})
	}
}
