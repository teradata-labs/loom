// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"fmt"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// WorkflowStore persists workflow execution history in memory.
// In production, this could be backed by a database.
type WorkflowStore struct {
	mu         sync.RWMutex
	executions map[string]*WorkflowExecution
}

// WorkflowExecution represents a single workflow execution with its metadata and results.
type WorkflowExecution struct {
	ExecutionID string
	Pattern     *loomv1.WorkflowPattern
	Result      *loomv1.WorkflowResult
	StartTime   time.Time
	EndTime     time.Time
	Status      WorkflowStatus
	Error       string // Error message if failed
}

// WorkflowStatus represents the execution state.
type WorkflowStatus string

const (
	WorkflowStatusRunning   WorkflowStatus = "running"
	WorkflowStatusCompleted WorkflowStatus = "completed"
	WorkflowStatusFailed    WorkflowStatus = "failed"
	WorkflowStatusCanceled  WorkflowStatus = "canceled"
)

// NewWorkflowStore creates a new workflow execution store.
func NewWorkflowStore() *WorkflowStore {
	return &WorkflowStore{
		executions: make(map[string]*WorkflowExecution),
	}
}

// Store persists a workflow execution record.
func (s *WorkflowStore) Store(exec *WorkflowExecution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executions[exec.ExecutionID] = exec
}

// Get retrieves a workflow execution by ID.
func (s *WorkflowStore) Get(executionID string) (*WorkflowExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	exec, exists := s.executions[executionID]
	if !exists {
		return nil, fmt.Errorf("workflow execution not found: %s", executionID)
	}

	return exec, nil
}

// List returns all workflow executions, optionally filtered by status.
func (s *WorkflowStore) List(status WorkflowStatus) []*WorkflowExecution {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*WorkflowExecution
	for _, exec := range s.executions {
		if status == "" || exec.Status == status {
			result = append(result, exec)
		}
	}

	return result
}

// UpdateStatus updates the status of a workflow execution.
func (s *WorkflowStore) UpdateStatus(executionID string, status WorkflowStatus, errorMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	exec, exists := s.executions[executionID]
	if !exists {
		return fmt.Errorf("workflow execution not found: %s", executionID)
	}

	exec.Status = status
	if errorMsg != "" {
		exec.Error = errorMsg
	}
	if status == WorkflowStatusCompleted || status == WorkflowStatusFailed || status == WorkflowStatusCanceled {
		exec.EndTime = time.Now()
	}

	return nil
}

// StoreResult updates the result of a completed workflow execution.
func (s *WorkflowStore) StoreResult(executionID string, result *loomv1.WorkflowResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	exec, exists := s.executions[executionID]
	if !exists {
		return fmt.Errorf("workflow execution not found: %s", executionID)
	}

	exec.Result = result
	exec.Status = WorkflowStatusCompleted
	exec.EndTime = time.Now()

	return nil
}

// Delete removes a workflow execution from the store.
func (s *WorkflowStore) Delete(executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.executions[executionID]; !exists {
		return fmt.Errorf("workflow execution not found: %s", executionID)
	}

	delete(s.executions, executionID)
	return nil
}

// Count returns the total number of workflow executions.
func (s *WorkflowStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.executions)
}
