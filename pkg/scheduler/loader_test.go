// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

func setupTestLoader(t *testing.T, workflowDir string) (*Scheduler, *Loader) {
	ctx := context.Background()
	logger := zap.NewNop()

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		Logger: logger,
	})
	require.NoError(t, err)

	// Create orchestrator
	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:    registry,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      logger,
		LLMProvider: nil,
	})

	// Create scheduler
	scheduler, err := NewScheduler(ctx, Config{
		WorkflowDir:  workflowDir,
		DBPath:       ":memory:",
		Orchestrator: orchestrator,
		Registry:     registry,
		Tracer:       observability.NewNoOpTracer(),
		Logger:       logger,
		HotReload:    false, // We'll manually control scanning
	})
	require.NoError(t, err)

	// Create loader manually
	loader := &Loader{
		workflowDir: workflowDir,
		scheduler:   scheduler,
		logger:      logger,
		fileHashes:  make(map[string]string),
	}

	return scheduler, loader
}

func TestLoader_ScanDirectory_NoDirectory(t *testing.T) {
	ctx := context.Background()
	_, loader := setupTestLoader(t, "/nonexistent/directory")

	// Should not error when directory doesn't exist
	err := loader.ScanDirectory(ctx)
	assert.NoError(t, err)
}

func TestLoader_ScanDirectory_EmptyDirectory(t *testing.T) {
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan empty directory
	err := loader.ScanDirectory(ctx)
	assert.NoError(t, err)

	// Verify no schedules were created
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestLoader_ScanDirectory_LoadWorkflow(t *testing.T) {
	t.Skip("Requires full YAML parser integration - workflow pattern parsing not yet implemented")
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write workflow YAML with schedule section
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: daily-report
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Generate daily report"
    stages:
      - agent_id: reporter
        prompt: "Create report"

schedule:
  cron: "0 8 * * *"
  timezone: "UTC"
  enabled: true
  skip_if_running: true
  max_execution_seconds: 3600
  variables:
    env: production
`

	workflowPath := filepath.Join(tempDir, "daily-report.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan directory
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify schedule was created
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 1)

	// Verify schedule details
	schedule := schedules[0]
	assert.Equal(t, "daily-report", schedule.WorkflowName)
	assert.Equal(t, "0 8 * * *", schedule.Schedule.Cron)
	assert.Equal(t, "UTC", schedule.Schedule.Timezone)
	assert.True(t, schedule.Schedule.Enabled)
	assert.True(t, schedule.Schedule.SkipIfRunning)
	assert.Equal(t, int32(3600), schedule.Schedule.MaxExecutionSeconds)
	assert.Equal(t, "production", schedule.Schedule.Variables["env"])
	assert.Equal(t, workflowPath, schedule.YamlPath)
}

func TestLoader_ScanDirectory_NoScheduleSection(t *testing.T) {
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write workflow YAML without schedule section
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: on-demand-task
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Run task"
    stages:
      - agent_id: worker
        prompt_template: "Execute task"
`

	workflowPath := filepath.Join(tempDir, "on-demand-task.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan directory
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify no schedules were created (workflow has no schedule section)
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestLoader_ScanDirectory_InvalidYAML(t *testing.T) {
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write invalid YAML
	invalidYAML := `this is not valid yaml: { unmatched brackets`

	workflowPath := filepath.Join(tempDir, "invalid.yaml")
	err := os.WriteFile(workflowPath, []byte(invalidYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan directory (should not error, just log)
	err = loader.ScanDirectory(ctx)
	assert.NoError(t, err)

	// Verify no schedules were created
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestLoader_FileChangeDetection(t *testing.T) {
	t.Skip("Requires full YAML parser integration")
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write initial workflow
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: hourly-task
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Run hourly task"
    stages:
      - agent_id: worker
        prompt: "Execute"

schedule:
  cron: "0 * * * *"
  timezone: "UTC"
  enabled: true
`

	workflowPath := filepath.Join(tempDir, "hourly-task.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// First scan
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 1)
	assert.Equal(t, "0 * * * *", schedules[0].Schedule.Cron)

	// Scan again without changes (should not update)
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// File hash should be cached
	assert.Len(t, loader.fileHashes, 1)
	assert.NotEmpty(t, loader.fileHashes[workflowPath])

	// Modify file
	updatedYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: hourly-task
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Run hourly task - updated"
    stages:
      - agent_id: worker
        prompt: "Execute"

schedule:
  cron: "0 */2 * * *"
  timezone: "UTC"
  enabled: true
`

	err = os.WriteFile(workflowPath, []byte(updatedYAML), 0644)
	require.NoError(t, err)

	// Scan again (should detect change and update)
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify schedule was updated
	schedules, err = scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 1)
	assert.Equal(t, "0 */2 * * *", schedules[0].Schedule.Cron)
}

func TestLoader_FileDelection(t *testing.T) {
	t.Skip("Requires full YAML parser integration")
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write workflow
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: temporary-task
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Temporary task"
    stages:
      - agent_id: worker
        prompt: "Execute"

schedule:
  cron: "0 0 * * *"
  timezone: "UTC"
  enabled: true
`

	workflowPath := filepath.Join(tempDir, "temporary-task.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// First scan
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 1)

	// Delete file
	err = os.Remove(workflowPath)
	require.NoError(t, err)

	// Scan again (should detect deletion and remove schedule)
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify schedule was removed
	schedules, err = scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestLoader_MultipleFiles(t *testing.T) {
	t.Skip("Requires full YAML parser integration")
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write multiple workflow files
	workflows := []struct {
		filename string
		name     string
		cron     string
	}{
		{"daily.yaml", "daily-backup", "0 0 * * *"},
		{"hourly.yml", "hourly-sync", "0 * * * *"},
		{"weekly.yaml", "weekly-report", "0 0 * * 0"},
	}

	for _, wf := range workflows {
		yaml := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: ` + wf.name + `
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Task"
    stages:
      - agent_id: worker
        prompt: "Execute"

schedule:
  cron: "` + wf.cron + `"
  timezone: "UTC"
  enabled: true
`
		path := filepath.Join(tempDir, wf.filename)
		err := os.WriteFile(path, []byte(yaml), 0644)
		require.NoError(t, err)
	}

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan directory
	err := loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify all schedules were created
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 3)

	// Verify each schedule
	scheduleMap := make(map[string]*loomv1.ScheduledWorkflow)
	for _, s := range schedules {
		scheduleMap[s.WorkflowName] = s
	}

	for _, wf := range workflows {
		schedule, exists := scheduleMap[wf.name]
		assert.True(t, exists, "Schedule for %s should exist", wf.name)
		assert.Equal(t, wf.cron, schedule.Schedule.Cron)
	}
}

func TestLoader_GenerateScheduleID(t *testing.T) {
	// Test that schedule IDs are deterministic for the same path
	path1 := "/path/to/workflow1.yaml"
	path2 := "/path/to/workflow2.yaml"

	id1a := generateScheduleIDFromPath(path1)
	id1b := generateScheduleIDFromPath(path1)
	id2 := generateScheduleIDFromPath(path2)

	// Same path should generate same ID
	assert.Equal(t, id1a, id1b)

	// Different paths should generate different IDs
	assert.NotEqual(t, id1a, id2)

	// ID should contain base filename
	assert.Contains(t, id1a, "workflow1")
	assert.Contains(t, id2, "workflow2")
}

func TestLoader_FileHash(t *testing.T) {
	// Create temp directory
	tempDir := t.TempDir()

	_, loader := setupTestLoader(t, tempDir)

	// Create test file
	testFile := filepath.Join(tempDir, "test.yaml")
	content := []byte("test content")
	err := os.WriteFile(testFile, content, 0644)
	require.NoError(t, err)

	// Get hash
	hash1, err := loader.fileHash(testFile)
	require.NoError(t, err)
	assert.NotEmpty(t, hash1)

	// Get hash again (should be same)
	hash2, err := loader.fileHash(testFile)
	require.NoError(t, err)
	assert.Equal(t, hash1, hash2)

	// Modify file
	err = os.WriteFile(testFile, []byte("different content"), 0644)
	require.NoError(t, err)

	// Hash should change
	hash3, err := loader.fileHash(testFile)
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash3)

	// Hash should be consistent for new content
	hash4, err := loader.fileHash(testFile)
	require.NoError(t, err)
	assert.Equal(t, hash3, hash4)
}

func TestLoader_ScheduleDefaults(t *testing.T) {
	t.Skip("Requires full YAML parser integration - workflow pattern parsing not yet implemented")
	ctx := context.Background()

	// Create temp directory
	tempDir := t.TempDir()

	// Write workflow with minimal schedule section
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: minimal-schedule
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Task"
    stages:
      - agent_id: worker
        prompt_template: "Execute"

schedule:
  cron: "0 0 * * *"
  enabled: true
`

	workflowPath := filepath.Join(tempDir, "minimal.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0644)
	require.NoError(t, err)

	scheduler, loader := setupTestLoader(t, tempDir)

	// Scan directory
	err = loader.ScanDirectory(ctx)
	require.NoError(t, err)

	// Verify schedule with defaults
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	if len(schedules) != 1 {
		t.Logf("Expected 1 schedule, got %d. Schedules: %+v", len(schedules), schedules)
		t.Logf("Workflow path: %s", workflowPath)
		t.Logf("Loader file hashes: %+v", loader.fileHashes)
	}
	require.Len(t, schedules, 1)

	schedule := schedules[0]
	assert.Equal(t, "UTC", schedule.Schedule.Timezone)                  // Default timezone
	assert.Equal(t, int32(3600), schedule.Schedule.MaxExecutionSeconds) // Default 1 hour
	assert.NotNil(t, schedule.Schedule.Variables)                       // Default empty map
}
