// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

// Config contains scheduler configuration.
type Config struct {
	WorkflowDir  string
	DBPath       string
	Orchestrator *orchestration.Orchestrator
	Registry     *agent.Registry
	Tracer       observability.Tracer
	Logger       *zap.Logger
	HotReload    bool
}

// Scheduler manages cron-based workflow execution.
type Scheduler struct {
	mu               sync.RWMutex
	schedules        map[string]*loomv1.ScheduledWorkflow
	runningWorkflows map[string]string // schedule_id -> execution_id
	cronEngine       *cron.Cron
	cronEntries      map[string]cron.EntryID
	store            *Store
	orchestrator     *orchestration.Orchestrator
	registry         *agent.Registry
	tracer           observability.Tracer
	logger           *zap.Logger
	loader           *Loader
	stopCh           chan struct{}
	wg               sync.WaitGroup
	config           Config
}

// NewScheduler creates a new workflow scheduler.
func NewScheduler(ctx context.Context, config Config) (*Scheduler, error) {
	// Validate config
	if config.Orchestrator == nil {
		return nil, fmt.Errorf("orchestrator is required")
	}
	if config.Registry == nil {
		return nil, fmt.Errorf("registry is required")
	}
	if config.Logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if config.DBPath == "" {
		return nil, fmt.Errorf("db path is required")
	}

	// Create store
	store, err := NewStore(ctx, config.DBPath, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	// Create cron engine with standard 5-field cron format
	cronEngine := cron.New()

	s := &Scheduler{
		schedules:        make(map[string]*loomv1.ScheduledWorkflow),
		runningWorkflows: make(map[string]string),
		cronEngine:       cronEngine,
		cronEntries:      make(map[string]cron.EntryID),
		store:            store,
		orchestrator:     config.Orchestrator,
		registry:         config.Registry,
		tracer:           config.Tracer,
		logger:           config.Logger,
		stopCh:           make(chan struct{}),
		config:           config,
	}

	// Create loader if hot-reload enabled
	if config.HotReload && config.WorkflowDir != "" {
		s.loader = &Loader{
			workflowDir: config.WorkflowDir,
			scheduler:   s,
			logger:      config.Logger,
			fileHashes:  make(map[string]string),
		}
	}

	return s, nil
}

// Start initializes the scheduler and begins executing workflows.
func (s *Scheduler) Start(ctx context.Context) error {
	s.logger.Info("Starting workflow scheduler")

	// Load schedules from database
	schedules, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to load schedules: %w", err)
	}

	s.logger.Info("Loaded schedules from database", zap.Int("count", len(schedules)))

	// Add each schedule to the cron engine
	for _, schedule := range schedules {
		if err := s.addScheduleToCron(ctx, schedule); err != nil {
			s.logger.Error("Failed to add schedule to cron",
				zap.String("schedule_id", schedule.Id),
				zap.Error(err))
			continue
		}
	}

	// Start cron engine
	s.cronEngine.Start()
	s.logger.Info("Cron engine started")

	// Start hot-reload watcher if enabled
	if s.loader != nil {
		s.wg.Add(1)
		go s.watchYAMLFiles(ctx)
		s.logger.Info("Hot-reload watcher started", zap.String("workflow_dir", s.config.WorkflowDir))
	}

	return nil
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.logger.Info("Stopping workflow scheduler")

	// Signal shutdown
	close(s.stopCh)

	// Stop cron engine (stops accepting new executions)
	cronCtx := s.cronEngine.Stop()

	// Wait for hot-reload watcher
	s.wg.Wait()

	// Wait for cron entries to complete or context timeout
	select {
	case <-cronCtx.Done():
		s.logger.Info("All scheduled tasks completed")
	case <-ctx.Done():
		s.logger.Warn("Scheduler shutdown timeout, some tasks may still be running")
	}

	// Close store
	if err := s.store.Close(); err != nil {
		s.logger.Error("Failed to close store", zap.Error(err))
		return err
	}

	s.logger.Info("Workflow scheduler stopped")
	return nil
}

// AddSchedule adds a new scheduled workflow.
func (s *Scheduler) AddSchedule(ctx context.Context, schedule *loomv1.ScheduledWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate schedule
	if err := s.validateSchedule(schedule); err != nil {
		return err
	}

	// Calculate next execution
	nextExec, err := s.calculateNextExecution(schedule.Schedule)
	if err != nil {
		return fmt.Errorf("failed to calculate next execution: %w", err)
	}
	schedule.NextExecutionAt = nextExec

	// Initialize stats if nil
	if schedule.Stats == nil {
		schedule.Stats = &loomv1.ScheduleStats{}
	}

	// Set timestamps if not already set
	now := time.Now().Unix()
	if schedule.CreatedAt == 0 {
		schedule.CreatedAt = now
	}
	if schedule.UpdatedAt == 0 {
		schedule.UpdatedAt = now
	}

	// Store in database
	if err := s.store.Create(ctx, schedule); err != nil {
		return fmt.Errorf("failed to store schedule: %w", err)
	}

	// Add to cron engine
	if err := s.addScheduleToCron(ctx, schedule); err != nil {
		return fmt.Errorf("failed to add to cron: %w", err)
	}

	s.logger.Info("Added schedule",
		zap.String("schedule_id", schedule.Id),
		zap.String("workflow_name", schedule.WorkflowName),
		zap.String("cron", schedule.Schedule.Cron))

	return nil
}

// UpdateSchedule updates an existing schedule.
func (s *Scheduler) UpdateSchedule(ctx context.Context, schedule *loomv1.ScheduledWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate schedule
	if err := s.validateSchedule(schedule); err != nil {
		return err
	}

	// Remove old cron entry
	if entryID, exists := s.cronEntries[schedule.Id]; exists {
		s.cronEngine.Remove(entryID)
		delete(s.cronEntries, schedule.Id)
	}

	// Calculate new next execution
	nextExec, err := s.calculateNextExecution(schedule.Schedule)
	if err != nil {
		return fmt.Errorf("failed to calculate next execution: %w", err)
	}
	schedule.NextExecutionAt = nextExec

	// Update in database
	if err := s.store.Update(ctx, schedule); err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}

	// Add new cron entry
	if err := s.addScheduleToCron(ctx, schedule); err != nil {
		return fmt.Errorf("failed to add to cron: %w", err)
	}

	s.logger.Info("Updated schedule",
		zap.String("schedule_id", schedule.Id),
		zap.String("workflow_name", schedule.WorkflowName))

	return nil
}

// RemoveSchedule removes a schedule from the scheduler.
func (s *Scheduler) RemoveSchedule(ctx context.Context, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove cron entry
	if entryID, exists := s.cronEntries[scheduleID]; exists {
		s.cronEngine.Remove(entryID)
		delete(s.cronEntries, scheduleID)
	}

	// Remove from in-memory map
	delete(s.schedules, scheduleID)

	// Remove from database
	if err := s.store.Delete(ctx, scheduleID); err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}

	s.logger.Info("Removed schedule", zap.String("schedule_id", scheduleID))
	return nil
}

// PauseSchedule disables a schedule without removing it.
func (s *Scheduler) PauseSchedule(ctx context.Context, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedule, exists := s.schedules[scheduleID]
	if !exists {
		return fmt.Errorf("schedule not found: %s", scheduleID)
	}

	// Remove from cron engine
	if entryID, exists := s.cronEntries[scheduleID]; exists {
		s.cronEngine.Remove(entryID)
		delete(s.cronEntries, scheduleID)
	}

	// Update enabled flag
	schedule.Schedule.Enabled = false
	if err := s.store.Update(ctx, schedule); err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}

	s.logger.Info("Paused schedule", zap.String("schedule_id", scheduleID))
	return nil
}

// ResumeSchedule re-enables a paused schedule.
func (s *Scheduler) ResumeSchedule(ctx context.Context, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedule, exists := s.schedules[scheduleID]
	if !exists {
		return fmt.Errorf("schedule not found: %s", scheduleID)
	}

	// Update enabled flag
	schedule.Schedule.Enabled = true

	// Calculate next execution
	nextExec, err := s.calculateNextExecution(schedule.Schedule)
	if err != nil {
		return fmt.Errorf("failed to calculate next execution: %w", err)
	}
	schedule.NextExecutionAt = nextExec

	// Update in database
	if err := s.store.Update(ctx, schedule); err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}

	// Add back to cron engine
	if err := s.addScheduleToCron(ctx, schedule); err != nil {
		return fmt.Errorf("failed to add to cron: %w", err)
	}

	s.logger.Info("Resumed schedule", zap.String("schedule_id", scheduleID))
	return nil
}

// TriggerNow manually triggers a scheduled workflow immediately.
func (s *Scheduler) TriggerNow(ctx context.Context, scheduleID string, skipIfRunning bool, variables map[string]string) (string, error) {
	s.mu.RLock()
	schedule, exists := s.schedules[scheduleID]
	s.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("schedule not found: %s", scheduleID)
	}

	// Check skip-if-running
	if skipIfRunning {
		s.mu.RLock()
		executionID := s.runningWorkflows[scheduleID]
		s.mu.RUnlock()

		if executionID != "" {
			return "", fmt.Errorf("previous execution still running: %s", executionID)
		}
	}

	// Merge variables
	mergedVars := make(map[string]string)
	if schedule.Schedule.Variables != nil {
		for k, v := range schedule.Schedule.Variables {
			mergedVars[k] = v
		}
	}
	for k, v := range variables {
		mergedVars[k] = v
	}

	// Execute workflow
	executionID := uuid.New().String()
	go s.executeWorkflow(context.Background(), schedule, executionID, mergedVars)

	return executionID, nil
}

// GetSchedule retrieves a schedule by ID.
func (s *Scheduler) GetSchedule(ctx context.Context, scheduleID string) (*loomv1.ScheduledWorkflow, error) {
	return s.store.Get(ctx, scheduleID)
}

// ListSchedules returns all schedules.
func (s *Scheduler) ListSchedules(ctx context.Context) ([]*loomv1.ScheduledWorkflow, error) {
	return s.store.List(ctx)
}

// GetHistory retrieves execution history for a schedule.
func (s *Scheduler) GetHistory(ctx context.Context, scheduleID string, limit int) ([]*loomv1.ScheduleExecution, error) {
	return s.store.GetExecutionHistory(ctx, scheduleID, limit)
}

// addScheduleToCron adds a schedule to the cron engine.
func (s *Scheduler) addScheduleToCron(ctx context.Context, schedule *loomv1.ScheduledWorkflow) error {
	if !schedule.Schedule.Enabled {
		s.schedules[schedule.Id] = schedule
		return nil
	}

	// Validate cron expression
	if _, err := cron.ParseStandard(schedule.Schedule.Cron); err != nil {
		return fmt.Errorf("failed to parse cron expression: %w", err)
	}

	// Create job function
	jobFunc := func() {
		execCtx := context.Background()
		executionID := uuid.New().String()

		// Use schedule variables
		variables := schedule.Schedule.Variables
		if variables == nil {
			variables = make(map[string]string)
		}

		s.executeWorkflow(execCtx, schedule, executionID, variables)
	}

	// Add to cron engine
	entryID, err := s.cronEngine.AddFunc(schedule.Schedule.Cron, jobFunc)
	if err != nil {
		return fmt.Errorf("failed to add cron job: %w", err)
	}

	s.cronEntries[schedule.Id] = entryID
	s.schedules[schedule.Id] = schedule

	return nil
}

// executeWorkflow executes a scheduled workflow.
func (s *Scheduler) executeWorkflow(ctx context.Context, schedule *loomv1.ScheduledWorkflow, executionID string, variables map[string]string) {
	startTime := time.Now()

	s.logger.Info("Executing scheduled workflow",
		zap.String("schedule_id", schedule.Id),
		zap.String("execution_id", executionID),
		zap.String("workflow_name", schedule.WorkflowName))

	// Check skip-if-running
	if schedule.Schedule.SkipIfRunning {
		s.mu.RLock()
		currentExecID := s.runningWorkflows[schedule.Id]
		s.mu.RUnlock()

		if currentExecID != "" {
			s.logger.Info("Skipping execution, previous still running",
				zap.String("schedule_id", schedule.Id),
				zap.String("current_execution_id", currentExecID))

			if err := s.store.IncrementSkipped(ctx, schedule.Id); err != nil {
				s.logger.Error("Failed to increment skipped count", zap.Error(err))
			}
			return
		}
	}

	// Mark as running
	s.mu.Lock()
	s.runningWorkflows[schedule.Id] = executionID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.runningWorkflows, schedule.Id)
		s.mu.Unlock()

		if err := s.store.UpdateCurrentExecution(ctx, schedule.Id, ""); err != nil {
			s.logger.Error("Failed to clear current execution", zap.Error(err))
		}
	}()

	// Update current execution ID
	if err := s.store.UpdateCurrentExecution(ctx, schedule.Id, executionID); err != nil {
		s.logger.Error("Failed to update current execution", zap.Error(err))
	}

	// Set execution timeout
	timeout := time.Duration(schedule.Schedule.MaxExecutionSeconds) * time.Second
	if timeout == 0 {
		timeout = 1 * time.Hour // Default timeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute workflow via orchestrator
	// TODO: Add variable interpolation support
	_, err := s.orchestrator.ExecutePattern(execCtx, schedule.Pattern)

	duration := time.Since(startTime)

	// Record execution in history
	execution := &loomv1.ScheduleExecution{
		ExecutionId: executionID,
		StartedAt:   startTime.Unix(),
		CompletedAt: time.Now().Unix(),
		DurationMs:  duration.Milliseconds(),
	}

	if err != nil {
		execution.Status = "failed"
		execution.Error = err.Error()

		s.logger.Error("Workflow execution failed",
			zap.String("schedule_id", schedule.Id),
			zap.String("execution_id", executionID),
			zap.Error(err))

		// Record failure
		if err := s.store.RecordFailure(ctx, schedule.Id, err.Error()); err != nil {
			s.logger.Error("Failed to record failure", zap.Error(err))
		}
	} else {
		execution.Status = "success"

		s.logger.Info("Workflow execution succeeded",
			zap.String("schedule_id", schedule.Id),
			zap.String("execution_id", executionID),
			zap.Int64("duration_ms", duration.Milliseconds()))

		// Record success
		if err := s.store.RecordSuccess(ctx, schedule.Id); err != nil {
			s.logger.Error("Failed to record success", zap.Error(err))
		}
	}

	// Store execution history
	if err := s.store.RecordExecution(ctx, execution, schedule.Id); err != nil {
		s.logger.Error("Failed to record execution", zap.Error(err))
	}

	// Calculate and update next execution time
	nextExec, err := s.calculateNextExecution(schedule.Schedule)
	if err != nil {
		s.logger.Error("Failed to calculate next execution", zap.Error(err))
		return
	}

	if err := s.store.UpdateNextExecution(ctx, schedule.Id, nextExec); err != nil {
		s.logger.Error("Failed to update next execution", zap.Error(err))
	}
}

// calculateNextExecution calculates the next execution time for a schedule.
func (s *Scheduler) calculateNextExecution(schedule *loomv1.ScheduleConfig) (int64, error) {
	// Parse cron expression
	cronSchedule, err := cron.ParseStandard(schedule.Cron)
	if err != nil {
		return 0, fmt.Errorf("failed to parse cron: %w", err)
	}

	// Load timezone
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		location = time.UTC
	}

	// Calculate next execution
	now := time.Now().In(location)
	next := cronSchedule.Next(now)

	return next.Unix(), nil
}

// validateSchedule validates a schedule configuration.
func (s *Scheduler) validateSchedule(schedule *loomv1.ScheduledWorkflow) error {
	if schedule.Id == "" {
		return fmt.Errorf("schedule ID is required")
	}
	if schedule.WorkflowName == "" {
		return fmt.Errorf("workflow name is required")
	}
	if schedule.Pattern == nil {
		return fmt.Errorf("pattern is required")
	}
	if schedule.Schedule == nil {
		return fmt.Errorf("schedule config is required")
	}
	if schedule.Schedule.Cron == "" {
		return fmt.Errorf("cron expression is required")
	}

	// Validate cron expression
	if _, err := cron.ParseStandard(schedule.Schedule.Cron); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	// Validate timezone
	if schedule.Schedule.Timezone == "" {
		schedule.Schedule.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(schedule.Schedule.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}

	return nil
}

// watchYAMLFiles watches for YAML file changes and hot-reloads schedules.
func (s *Scheduler) watchYAMLFiles(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.loader.ScanDirectory(ctx); err != nil {
				s.logger.Error("Failed to scan workflow directory", zap.Error(err))
			}
		case <-s.stopCh:
			s.logger.Info("Stopping YAML file watcher")
			return
		}
	}
}
