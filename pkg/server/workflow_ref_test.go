// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

const testDebateWorkflowYAML = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: architecture-debate
  description: Debate architecture decisions
spec:
  type: debate
  topic: "Should we use microservices or monolith?"
  agent_ids:
    - architect
    - pragmatist
  rounds: 3
  merge_strategy: consensus
  moderator_agent_id: senior-architect
`

func newWorkflowTestServer(t *testing.T) (*MultiAgentServer, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir: dir,
		DBPath:    filepath.Join(dir, "registry.db"),
		Logger:    zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	s := &MultiAgentServer{logger: zaptest.NewLogger(t)}
	s.SetAgentRegistry(reg)
	return s, dir
}

func writeTestWorkflow(t *testing.T, dir, name, yaml string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflows", name+".yaml"), []byte(yaml), 0o600))
}

func TestResolveWorkflowPattern(t *testing.T) {
	s, dir := newWorkflowTestServer(t)
	writeTestWorkflow(t, dir, "architecture-debate", testDebateWorkflowYAML)
	reg := s.registry

	t.Run("workflow_ref loads the saved workflow", func(t *testing.T) {
		p, err := s.resolveWorkflowPattern(&loomv1.ExecuteWorkflowRequest{WorkflowRef: "architecture-debate"}, reg)
		require.NoError(t, err)
		require.NotNil(t, p)
	})

	t.Run("inline pattern is returned as-is", func(t *testing.T) {
		inline := &loomv1.WorkflowPattern{}
		p, err := s.resolveWorkflowPattern(&loomv1.ExecuteWorkflowRequest{Pattern: inline}, reg)
		require.NoError(t, err)
		assert.Same(t, inline, p)
	})

	t.Run("neither pattern nor ref is an error", func(t *testing.T) {
		_, err := s.resolveWorkflowPattern(&loomv1.ExecuteWorkflowRequest{}, reg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workflow_ref is required")
	})

	t.Run("path traversal is rejected", func(t *testing.T) {
		for _, bad := range []string{"../secret", "sub/evil", `a\b`} {
			_, err := s.resolveWorkflowPattern(&loomv1.ExecuteWorkflowRequest{WorkflowRef: bad}, reg)
			require.Error(t, err, "ref %q must be rejected", bad)
			assert.Contains(t, err.Error(), "bare workflow name")
		}
	})

	t.Run("unknown ref is not-found", func(t *testing.T) {
		_, err := s.resolveWorkflowPattern(&loomv1.ExecuteWorkflowRequest{WorkflowRef: "does-not-exist"}, reg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestListWorkflows(t *testing.T) {
	s, dir := newWorkflowTestServer(t)
	writeTestWorkflow(t, dir, "architecture-debate", testDebateWorkflowYAML)
	// An unparseable file must be skipped, not fail the whole listing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflows", "broken.yaml"), []byte("not: [valid"), 0o600))

	resp, err := s.ListWorkflows(context.Background(), &loomv1.ListWorkflowsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetWorkflows(), 1, "broken.yaml should be skipped")
	w := resp.GetWorkflows()[0]
	assert.Equal(t, "architecture-debate", w.GetName())
	assert.Equal(t, "Debate architecture decisions", w.GetDescription())
	assert.Equal(t, "debate", w.GetType())
}
