// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

// Loader loads workflow YAML files with schedule sections and manages hot-reload.
type Loader struct {
	workflowDir string
	scheduler   *Scheduler
	logger      *zap.Logger
	fileHashes  map[string]string // path -> SHA256 hash for change detection
}

// ScanDirectory scans the workflow directory for YAML files with schedule sections.
// Loads new schedules and reloads changed ones.
func (l *Loader) ScanDirectory(ctx context.Context) error {
	if l.workflowDir == "" {
		return nil
	}

	// Check if directory exists
	if _, err := os.Stat(l.workflowDir); os.IsNotExist(err) {
		l.logger.Debug("Workflow directory does not exist", zap.String("dir", l.workflowDir))
		return nil
	}

	// Find all YAML files
	files, err := filepath.Glob(filepath.Join(l.workflowDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to glob yaml files: %w", err)
	}

	yamlFiles, err := filepath.Glob(filepath.Join(l.workflowDir, "*.yml"))
	if err != nil {
		return fmt.Errorf("failed to glob yml files: %w", err)
	}

	files = append(files, yamlFiles...)

	// Track which files we've seen this scan
	seenFiles := make(map[string]bool)

	for _, path := range files {
		seenFiles[path] = true

		// Check if file changed
		hash, err := l.fileHash(path)
		if err != nil {
			l.logger.Error("Failed to hash file", zap.String("path", path), zap.Error(err))
			continue
		}

		oldHash, exists := l.fileHashes[path]
		if exists && oldHash == hash {
			// File hasn't changed
			continue
		}

		// File is new or changed, load it
		l.logger.Info("Loading workflow file", zap.String("path", path), zap.Bool("new", !exists))

		if err := l.loadWorkflowFile(ctx, path); err != nil {
			l.logger.Error("Failed to load workflow file",
				zap.String("path", path),
				zap.Error(err))
			continue
		}

		l.fileHashes[path] = hash
	}

	// Remove schedules for deleted files
	for path := range l.fileHashes {
		if !seenFiles[path] {
			scheduleID := generateScheduleIDFromPath(path)
			l.logger.Info("Workflow file deleted, removing schedule",
				zap.String("path", path),
				zap.String("schedule_id", scheduleID))

			if err := l.scheduler.RemoveSchedule(ctx, scheduleID); err != nil {
				l.logger.Error("Failed to remove schedule for deleted file",
					zap.String("path", path),
					zap.String("schedule_id", scheduleID),
					zap.Error(err))
			}

			delete(l.fileHashes, path)
		}
	}

	return nil
}

// loadWorkflowFile loads a workflow YAML file and creates/updates its schedule.
func (l *Loader) loadWorkflowFile(ctx context.Context, path string) error {
	// Parse workflow YAML
	config, err := orchestration.LoadWorkflowConfigFromYAML(path)
	if err != nil {
		return fmt.Errorf("failed to parse workflow YAML: %w", err)
	}

	// Check if it has a schedule section
	if config.Schedule == nil {
		l.logger.Debug("Workflow has no schedule section", zap.String("path", path))
		return nil
	}

	// Convert to proto
	pattern, err := orchestration.ConvertConfigToProto(config)
	if err != nil {
		return fmt.Errorf("failed to convert config to proto: %w", err)
	}

	// Create schedule proto
	scheduleID := generateScheduleIDFromPath(path)
	schedule := &loomv1.ScheduledWorkflow{
		Id:           scheduleID,
		WorkflowName: config.Metadata.Name,
		YamlPath:     path,
		Pattern:      pattern,
		Schedule: &loomv1.ScheduleConfig{
			Cron:                config.Schedule.Cron,
			Timezone:            config.Schedule.Timezone,
			Enabled:             config.Schedule.Enabled,
			SkipIfRunning:       config.Schedule.SkipIfRunning,
			MaxExecutionSeconds: config.Schedule.MaxExecutionSeconds,
			Variables:           config.Schedule.Variables,
		},
	}

	// Set defaults
	if schedule.Schedule.Timezone == "" {
		schedule.Schedule.Timezone = "UTC"
	}
	if schedule.Schedule.MaxExecutionSeconds == 0 {
		schedule.Schedule.MaxExecutionSeconds = 3600
	}
	if schedule.Schedule.Variables == nil {
		schedule.Schedule.Variables = make(map[string]string)
	}

	// Check if schedule already exists
	existing, err := l.scheduler.store.Get(ctx, scheduleID)
	if err == nil {
		// Schedule exists, update it
		schedule.Stats = existing.Stats
		schedule.CreatedAt = existing.CreatedAt
		schedule.UpdatedAt = existing.UpdatedAt

		if err := l.scheduler.UpdateSchedule(ctx, schedule); err != nil {
			return fmt.Errorf("failed to update schedule: %w", err)
		}

		l.logger.Info("Updated schedule from YAML",
			zap.String("schedule_id", scheduleID),
			zap.String("path", path))
	} else {
		// New schedule, create it
		schedule.CreatedAt = 0 // Will be set by scheduler
		schedule.UpdatedAt = 0

		if err := l.scheduler.AddSchedule(ctx, schedule); err != nil {
			return fmt.Errorf("failed to add schedule: %w", err)
		}

		l.logger.Info("Created schedule from YAML",
			zap.String("schedule_id", scheduleID),
			zap.String("path", path))
	}

	return nil
}

// fileHash computes SHA256 hash of a file for change detection.
func (l *Loader) fileHash(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// generateScheduleIDFromPath generates a stable schedule ID from a file path.
func generateScheduleIDFromPath(path string) string {
	// Use filename without extension as base
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	// Hash the full path for uniqueness
	h := sha256.New()
	h.Write([]byte(path))
	hash := fmt.Sprintf("%x", h.Sum(nil))[:8]

	return fmt.Sprintf("%s-%s", base, hash)
}
