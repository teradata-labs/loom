// Copyright (c) 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package scheduler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

func TestLoader_SessionMode(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		expectedMode loomv1.ScheduledSessionMode
	}{
		{
			name: "resume",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-resume-workflow
spec:
  type: pipeline
  initial_prompt: "hello"
  stages:
    - agent_id: agent1
      prompt_template: "do work"
schedule:
  cron: "0 * * * *"
  enabled: true
  session_mode: resume
`,
			expectedMode: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_RESUME,
		},
		{
			name: "new",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-new-workflow
spec:
  type: pipeline
  initial_prompt: "hello"
  stages:
    - agent_id: agent1
      prompt_template: "do work"
schedule:
  cron: "0 * * * *"
  enabled: true
  session_mode: new
`,
			expectedMode: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_NEW,
		},
		{
			name: "default (empty)",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-default-workflow
spec:
  type: pipeline
  initial_prompt: "hello"
  stages:
    - agent_id: agent1
      prompt_template: "do work"
schedule:
  cron: "0 * * * *"
  enabled: true
`,
			expectedMode: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_UNSPECIFIED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Write YAML to temp file
			tmpDir := t.TempDir()
			yamlPath := filepath.Join(tmpDir, "workflow.yaml")
			err := os.WriteFile(yamlPath, []byte(tc.yaml), 0600)
			require.NoError(t, err)

			// Load the workflow config
			config, err := orchestration.LoadWorkflowConfigFromYAML(yamlPath)
			require.NoError(t, err)
			require.NotNil(t, config.Schedule, "expected schedule section to be present")

			// Convert session_mode string to proto enum
			mode := parseSessionMode(config.Schedule.SessionMode)
			assert.Equal(t, tc.expectedMode, mode)
		})
	}
}
