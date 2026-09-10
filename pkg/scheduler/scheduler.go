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
	mu        sync.RWMutex
	schedules map[string]*loomv1.ScheduledWorkflow
	// runs holds every in-flight execution, keyed by execution ID.
	//
	// This is deliberately one map rather than parallel maps for the cancel
	// func, the reason and the running marker. Those three facts have to change
	// together — an execution must become cancellable at the same instant it is
	// advertised as running, and stop being cancellable at the same instant it
	// reads its verdict — and keeping them in one value makes that invariant
	// structural instead of something every future edit has to remember.
	runs         map[string]*runState
	cronEngine   *cron.Cron
	cronEntries  map[string]cron.EntryID
	store        *Store
	orchestrator *orchestration.Orchestrator
	registry     *agent.Registry
	tracer       observability.Tracer
	logger       *zap.Logger
	loader       *Loader
	stopCh       chan struct{}
	wg           sync.WaitGroup
	// inFlight counts executeWorkflow bodies that have been launched and have
	// not yet finished writing. Stop waits on it so the store is never closed
	// underneath a run's bookkeeping tail, which would turn four SQLite writes
	// — including the history entry an operator is waiting to see — into errors
	// on a goroutine nobody is watching.
	//
	// It is distinct from runs: runs answers "can this execution still be
	// canceled or does it still occupy its schedule's slot", inFlight answers
	// "is the store still in use". A run leaves runs before its last write.
	inFlight sync.WaitGroup
	// stopped is set once by Stop, under mu. beginRun refuses to launch further
	// runs after that, which is what makes inFlight.Wait sound: every Add
	// happens under the same lock that sets this flag, so no Add can race the
	// Wait that follows it.
	stopped bool
	config  Config
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
		schedules:    make(map[string]*loomv1.ScheduledWorkflow),
		runs:         make(map[string]*runState),
		cronEngine:   cronEngine,
		cronEntries:  make(map[string]cron.EntryID),
		store:        store,
		orchestrator: config.Orchestrator,
		registry:     config.Registry,
		tracer:       config.Tracer,
		logger:       config.Logger,
		stopCh:       make(chan struct{}),
		config:       config,
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

// shutdownCancelReason is the reason recorded against runs that Stop had to
// signal because its grace period ran out. It reads as an operator-visible
// explanation in the history entry, which is the whole point of recording a
// reason at all.
const shutdownCancelReason = "scheduler shutting down"

// beginRun reserves a slot for one execution and reports whether the caller may
// launch it. Every launch site must go through it, and must call
// s.inFlight.Done() once the execution's last store write has happened.
//
// The Add is done under the same lock Stop uses to set stopped, so a run can
// never join after Stop has decided what it is waiting for — the happens-before
// that makes the subsequent inFlight.Wait sound. Refusing the launch matters as
// much as counting it: after Stop the store is closed, and a run that started
// anyway would spend its whole life writing to a closed database.
func (s *Scheduler) beginRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return false
	}
	s.inFlight.Add(1)
	return true
}

// cancelAllRunning signals every cancellable execution and reports how many it
// signaled. Used by Stop when the shutdown deadline expires.
func (s *Scheduler) cancelAllRunning(reason string) int {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.runs))
	for _, run := range s.runs {
		if run.settled || run.canceled {
			continue
		}
		run.canceled = true
		run.reason = reason
		cancels = append(cancels, run.cancel)
	}
	s.mu.Unlock()

	// Outside the lock, for the same reason CancelExecution does it: an
	// unwinding run may take the scheduler lock on its way out.
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// signalShutdown asks every still-cancellable run to unwind because the
// shutdown grace period is over. It signals only; Stop does the waiting.
func (s *Scheduler) signalShutdown() {
	signaled := s.cancelAllRunning(shutdownCancelReason)
	s.logger.Warn("Scheduler shutdown deadline passed; canceled in-flight executions",
		zap.Int("canceled", signaled))
}

// Stop shuts the scheduler down and waits for in-flight executions to finish
// writing before the store is closed.
//
// ctx bounds the grace period, not the shutdown: when it expires with work
// still running, Stop signals those runs and then waits for them anyway. Giving
// up instead would close the database while a run is recording its outcome, so
// the last thing an operator sees of a workflow — its history entry — would be
// the thing lost.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.logger.Info("Stopping workflow scheduler")

	// Refuse new runs first. Paired with beginRun's Add under the same lock,
	// this fixes the set of executions the wait below has to cover.
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	// Signal shutdown
	close(s.stopCh)

	// Stop the cron engine so nothing further is scheduled. Jobs already
	// running are counted in inFlight, so they are waited for below rather
	// than through the context this returns.
	s.cronEngine.Stop()

	// Wait for hot-reload watcher
	s.wg.Wait()

	drained := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(drained)
	}()

	// A caller whose deadline has already passed gets no grace period; anyone
	// else gets until ctx expires for runs to finish on their own.
	if ctx.Err() != nil {
		s.signalShutdown()
	} else {
		select {
		case <-drained:
			s.logger.Info("All scheduled tasks completed")
		case <-ctx.Done():
			s.signalShutdown()
		}
	}

	// This final wait has no deadline on purpose. The store cannot be closed
	// while a run is writing its record, and every remaining run has had its
	// context canceled, so the wait is bounded in practice by the executor
	// honoring that context and by the store's SQLite busy timeout — not by
	// max_execution_seconds.
	<-drained

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
		executionID := s.runningExecutionFor(scheduleID)

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

	// Reserve the run before spawning it. A trigger that arrives during
	// shutdown is refused rather than started, because the store it would
	// write to is about to close.
	if !s.beginRun() {
		return "", fmt.Errorf("scheduler is stopped")
	}

	// Execute workflow
	executionID := uuid.New().String()
	go func() { // #nosec G118 -- intentional: background worker goroutine that must outlive the request context
		// Done only once executeWorkflow has returned, so it covers the
		// bookkeeping tail as well as the workflow itself.
		defer s.inFlight.Done()
		s.executeWorkflow(context.Background(), schedule, executionID, mergedVars)
	}()

	return executionID, nil
}

// CancelOutcome is what a cancellation request actually did.
//
// The three values are kept distinct because collapsing them loses the one
// distinction an operator cares about: whether the thing they were trying to
// stop was ever stoppable in the first place.
type CancelOutcome int

const (
	// CancelOutcomeNotFound means no execution with that ID exists in this
	// scheduler, neither in flight nor in the recorded history. The usual cause
	// is an ID from the ExecuteWorkflow/StreamWorkflow namespace, which this
	// scheduler does not own.
	CancelOutcomeNotFound CancelOutcome = iota

	// CancelOutcomeAlreadyFinished means the execution is one this scheduler
	// ran, but it had already reached its verdict before the request arrived.
	// Nothing was signaled and the recorded outcome stands.
	CancelOutcomeAlreadyFinished

	// CancelOutcomeSignaled means the execution was in flight and its context
	// was canceled. The run unwinds at its next context check and is then
	// recorded as canceled.
	CancelOutcomeSignaled
)

// String makes the outcome readable in logs and test failures.
func (o CancelOutcome) String() string {
	switch o {
	case CancelOutcomeNotFound:
		return "not_found"
	case CancelOutcomeAlreadyFinished:
		return "already_finished"
	case CancelOutcomeSignaled:
		return "signaled"
	default:
		return fmt.Sprintf("CancelOutcome(%d)", int(o))
	}
}

// runState is one in-flight execution.
//
// settled is the pivot. It is set at the single point where executeWorkflow
// reads its verdict, and from that instant the run is no longer cancellable
// even though its bookkeeping tail — four SQLite writes — is still running.
// Without that pivot a cancel arriving during the tail would be accepted and
// would overwrite the verdict the run had already reached, discarding a real
// success or a real failure and leaving the counters behind.
//
// The entry itself outlives settled, because skip_if_running must keep blocking
// new runs for this schedule until the tail has finished writing.
type runState struct {
	scheduleID string
	cancel     context.CancelFunc
	reason     string
	canceled   bool
	settled    bool
}

// CancelExecution stops a scheduled execution that is in flight.
//
// The three outcomes are deliberate. CancelOutcomeAlreadyFinished is not an
// error: someone stopping a run that completed a moment earlier got the state
// they asked for, and turning that race into a failure would make the UI
// apologise for winning it. CancelOutcomeNotFound is reported separately
// because saying "already finished" about an ID this scheduler never minted is
// a lie an operator would act on — most often it means an execution ID from
// ExecuteWorkflow or StreamWorkflow, which belongs to another namespace.
//
// Cancellation is cooperative. The execution's context is canceled, which
// unwinds the orchestrator at its next context check; work already committed by
// earlier stages is not rolled back. The reason is recorded first, so
// executeWorkflow cannot observe a canceled context before it can see why.
func (s *Scheduler) CancelExecution(ctx context.Context, executionID, reason string) (CancelOutcome, error) {
	if executionID == "" {
		return CancelOutcomeNotFound, fmt.Errorf("execution_id is required")
	}

	s.mu.Lock()
	run, tracked := s.runs[executionID]
	cancellable := tracked && !run.settled
	var cancel context.CancelFunc
	if cancellable {
		cancel = run.cancel
		// Written before the signal so executeWorkflow's classification cannot
		// see a canceled context before it can see the reason. Only the first
		// cancel wins: two operators racing should leave the history naming the
		// one whose signal actually stopped the run, not whichever arrived last.
		if !run.canceled {
			run.canceled = true
			run.reason = reason
		}
	}
	s.mu.Unlock()

	if cancellable {
		s.logger.Info("Canceling workflow execution",
			zap.String("schedule_id", run.scheduleID),
			zap.String("execution_id", executionID),
			zap.String("reason", reason))

		// Called outside s.mu: a cancel func whose unwinding path takes the
		// scheduler lock would otherwise self-deadlock.
		cancel()
		return CancelOutcomeSignaled, nil
	}

	if tracked {
		// Known, in the map, but past its linearization point.
		return CancelOutcomeAlreadyFinished, nil
	}

	// Not in flight. Distinguish a run this scheduler finished from an ID it has
	// never seen, so the caller is not told a live workflow "already finished".
	known, err := s.store.ExecutionExists(ctx, executionID)
	if err != nil {
		return CancelOutcomeNotFound, fmt.Errorf("failed to look up execution: %w", err)
	}
	if known {
		return CancelOutcomeAlreadyFinished, nil
	}

	return CancelOutcomeNotFound, nil
}

// RunningExecutions returns the executions that can still be canceled, keyed by
// schedule ID.
//
// Settled runs are excluded on purpose. This accessor exists so an operations
// view can offer a stop button only where there is something to stop, so
// anything it reports must still be cancellable when the operator clicks —
// listing a run whose cancel would come back "already finished" is the exact
// mismatch this is meant to avoid.
func (s *Scheduler) RunningExecutions() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(s.runs))
	for execID, run := range s.runs {
		if !run.settled {
			out[run.scheduleID] = execID
		}
	}
	return out
}

// runningExecutionFor returns the execution currently occupying a schedule's
// slot, or "" if the schedule is idle.
//
// This deliberately counts settled runs too. skip_if_running has to keep
// blocking until the previous run's bookkeeping tail has finished writing:
// letting a new run start mid-tail would have the old run's teardown clear the
// new run's current_execution_id and drop its entry.
func (s *Scheduler) runningExecutionFor(scheduleID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for execID, run := range s.runs {
		if run.scheduleID == scheduleID {
			return execID
		}
	}
	return ""
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
		// Same reservation as TriggerNow: a tick that fires while Stop is
		// draining must not open a new run against a closing store.
		if !s.beginRun() {
			return
		}
		defer s.inFlight.Done()

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
		currentExecID := s.runningExecutionFor(schedule.Id)

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

	// Set execution timeout. Built before the run is advertised so that the
	// cancel func exists to register in the same breath.
	timeout := time.Duration(schedule.Schedule.MaxExecutionSeconds) * time.Second
	if timeout == 0 {
		timeout = 1 * time.Hour // Default timeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Mark as running and publish the cancel func in one critical section, so
	// there is no window in which the run is advertised as running — including
	// via current_execution_id, the field callers are told to pass to
	// CancelScheduledExecution — but a cancel would come back "not running".
	s.mu.Lock()
	s.runs[executionID] = &runState{scheduleID: schedule.Id, cancel: cancel}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.runs, executionID)
		s.mu.Unlock()

		if err := s.store.UpdateCurrentExecution(ctx, schedule.Id, ""); err != nil {
			s.logger.Error("Failed to clear current execution", zap.Error(err))
		}
	}()

	// Update current execution ID
	if err := s.store.UpdateCurrentExecution(ctx, schedule.Id, executionID); err != nil {
		s.logger.Error("Failed to update current execution", zap.Error(err))
	}

	// Set workflow_id for session continuity in RESUME mode
	if schedule.Schedule.SessionMode == loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_RESUME {
		// Use schedule ID as stable workflow_id so agent sessions are deterministic
		schedule.Pattern.WorkflowId = schedule.Id
		s.logger.Info("RESUME mode: using stable workflow_id for session continuity",
			zap.String("schedule_id", schedule.Id),
			zap.String("workflow_id", schedule.Id))
	}
	// For NEW or UNSPECIFIED: leave WorkflowId empty → random UUID in executor

	// Execute workflow via orchestrator
	// TODO: Add variable interpolation support
	_, err := s.orchestrator.ExecutePattern(execCtx, schedule.Pattern)

	// Linearization point. Reading the cancel state and closing the run to
	// further cancellation happen in one critical section, immediately on
	// return, so the verdict below is decided against exactly one state.
	//
	// The whole bookkeeping tail that follows — UpdateLastWorkflowID, the
	// Record* calls, RecordExecution, UpdateNextExecution — is four SQLite
	// writes serialised on the store lock, not a few instructions. Leaving the
	// run cancellable across it meant a cancel arriving in that window was
	// accepted, relabelled a completed or failed run as canceled, threw away
	// its real error, and skipped its counters entirely.
	s.mu.Lock()
	var wasCanceled bool
	var cancelReason string
	if run, ok := s.runs[executionID]; ok {
		wasCanceled = run.canceled
		cancelReason = run.reason
		run.settled = true
	} else {
		// Unreachable: the entry is registered before ExecutePattern and removed
		// only by this function's own deferred teardown. Logged rather than
		// dereferenced because this runs on a background goroutine, where a nil
		// deref would take the whole server down.
		s.logger.Error("Execution missing from run map at classification",
			zap.String("schedule_id", schedule.Id),
			zap.String("execution_id", executionID))
	}
	s.mu.Unlock()

	duration := time.Since(startTime)

	// Track which workflow_id was used
	workflowID := schedule.Pattern.WorkflowId
	if workflowID == "" {
		workflowID = executionID // for NEW mode, track the execution ID
	}

	// Update last_workflow_id for tracking
	if storeErr := s.store.UpdateLastWorkflowID(ctx, schedule.Id, workflowID); storeErr != nil {
		s.logger.Error("Failed to update last workflow ID", zap.Error(storeErr))
	}

	// Record execution in history
	execution := &loomv1.ScheduleExecution{
		ExecutionId: executionID,
		StartedAt:   startTime.Unix(),
		CompletedAt: time.Now().Unix(),
		DurationMs:  duration.Milliseconds(),
		WorkflowId:  workflowID,
	}

	// A canceled run is not a failed run. Counting an operator's stop as a
	// failure would corrupt the success rate the UI uses to convey trust, and
	// would make a deliberately stopped routine look broken.
	//
	// The err != nil conjunct matters: cancellation is cooperative, so a run can
	// be signaled and still finish on its own before it next checks its
	// context. That run reached a real verdict and must be recorded as such —
	// its counters move like any other. Only a run the signal actually stopped
	// is recorded as canceled.
	switch {
	case wasCanceled && err != nil:
		execution.Status = "canceled"
		if cancelReason != "" {
			execution.Error = cancelReason
		} else {
			execution.Error = "canceled by operator"
		}

		s.logger.Info("Workflow execution canceled",
			zap.String("schedule_id", schedule.Id),
			zap.String("execution_id", executionID),
			zap.String("reason", cancelReason),
			zap.Int64("duration_ms", duration.Milliseconds()))

		// Move last_status off the previous run's outcome. Without this a
		// stopped routine reports itself as having last succeeded.
		if storeErr := s.store.RecordCanceled(ctx, schedule.Id, execution.Error); storeErr != nil {
			s.logger.Error("Failed to record cancellation", zap.Error(storeErr))
		}

	case err != nil:
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

	default:
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

	// Warn if RESUME mode is used without skip_if_running
	if schedule.Schedule.SessionMode == loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_RESUME &&
		!schedule.Schedule.SkipIfRunning {
		s.logger.Warn("RESUME session mode without skip_if_running may cause concurrent access to the same sessions",
			zap.String("schedule_id", schedule.Id),
			zap.String("workflow_name", schedule.WorkflowName))
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
