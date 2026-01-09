// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewWorkflowStore(t *testing.T) {
	store := NewWorkflowStore()
	if store == nil {
		t.Fatal("Expected non-nil store")
	}

	if store.Count() != 0 {
		t.Errorf("Expected empty store, got count %d", store.Count())
	}
}

func TestWorkflowStore_Store(t *testing.T) {
	store := NewWorkflowStore()

	exec := &WorkflowExecution{
		ExecutionID: "test-1",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Test",
				},
			},
		},
		StartTime: time.Now(),
		Status:    WorkflowStatusRunning,
	}

	store.Store(exec)

	if store.Count() != 1 {
		t.Errorf("Expected count 1, got %d", store.Count())
	}

	// Verify we can retrieve it
	retrieved, err := store.Get("test-1")
	if err != nil {
		t.Fatalf("Failed to retrieve stored execution: %v", err)
	}

	if retrieved.ExecutionID != "test-1" {
		t.Errorf("Expected execution ID 'test-1', got '%s'", retrieved.ExecutionID)
	}
}

func TestWorkflowStore_Get(t *testing.T) {
	store := NewWorkflowStore()

	// Store an execution
	exec := &WorkflowExecution{
		ExecutionID: "test-get-1",
		Status:      WorkflowStatusRunning,
	}
	store.Store(exec)

	tests := []struct {
		name        string
		executionID string
		expectError bool
	}{
		{
			name:        "existing execution",
			executionID: "test-get-1",
			expectError: false,
		},
		{
			name:        "non-existent execution",
			executionID: "does-not-exist",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.Get(tt.executionID)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				if result != nil {
					t.Error("Expected nil result for non-existent execution")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if result == nil {
					t.Error("Expected non-nil result")
					return
				}
				if result.ExecutionID != tt.executionID {
					t.Errorf("Expected execution ID '%s', got '%s'", tt.executionID, result.ExecutionID)
				}
			}
		})
	}
}

func TestWorkflowStore_List(t *testing.T) {
	store := NewWorkflowStore()

	// Store multiple executions with different statuses
	executions := []*WorkflowExecution{
		{ExecutionID: "exec-1", Status: WorkflowStatusRunning},
		{ExecutionID: "exec-2", Status: WorkflowStatusCompleted},
		{ExecutionID: "exec-3", Status: WorkflowStatusRunning},
		{ExecutionID: "exec-4", Status: WorkflowStatusFailed},
		{ExecutionID: "exec-5", Status: WorkflowStatusCanceled},
	}

	for _, exec := range executions {
		store.Store(exec)
	}

	tests := []struct {
		name          string
		filterStatus  WorkflowStatus
		expectedCount int
	}{
		{
			name:          "list all",
			filterStatus:  "",
			expectedCount: 5,
		},
		{
			name:          "list running",
			filterStatus:  WorkflowStatusRunning,
			expectedCount: 2,
		},
		{
			name:          "list completed",
			filterStatus:  WorkflowStatusCompleted,
			expectedCount: 1,
		},
		{
			name:          "list failed",
			filterStatus:  WorkflowStatusFailed,
			expectedCount: 1,
		},
		{
			name:          "list canceled",
			filterStatus:  WorkflowStatusCanceled,
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := store.List(tt.filterStatus)

			if len(results) != tt.expectedCount {
				t.Errorf("Expected %d results, got %d", tt.expectedCount, len(results))
			}

			// Verify all results match the filter
			if tt.filterStatus != "" {
				for _, result := range results {
					if result.Status != tt.filterStatus {
						t.Errorf("Expected status '%s', got '%s'", tt.filterStatus, result.Status)
					}
				}
			}
		})
	}
}

func TestWorkflowStore_UpdateStatus(t *testing.T) {
	store := NewWorkflowStore()

	exec := &WorkflowExecution{
		ExecutionID: "test-update",
		Status:      WorkflowStatusRunning,
		StartTime:   time.Now(),
	}
	store.Store(exec)

	tests := []struct {
		name          string
		executionID   string
		newStatus     WorkflowStatus
		errorMsg      string
		expectError   bool
		expectEndTime bool
	}{
		{
			name:          "update to completed",
			executionID:   "test-update",
			newStatus:     WorkflowStatusCompleted,
			errorMsg:      "",
			expectError:   false,
			expectEndTime: true,
		},
		{
			name:          "update to failed with error",
			executionID:   "test-update",
			newStatus:     WorkflowStatusFailed,
			errorMsg:      "something went wrong",
			expectError:   false,
			expectEndTime: true,
		},
		{
			name:          "update to canceled",
			executionID:   "test-update",
			newStatus:     WorkflowStatusCanceled,
			errorMsg:      "",
			expectError:   false,
			expectEndTime: true,
		},
		{
			name:        "update non-existent execution",
			executionID: "does-not-exist",
			newStatus:   WorkflowStatusCompleted,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.UpdateStatus(tt.executionID, tt.newStatus, tt.errorMsg)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected no error, got %v", err)
				return
			}

			// Verify the update
			result, _ := store.Get(tt.executionID)
			if result.Status != tt.newStatus {
				t.Errorf("Expected status '%s', got '%s'", tt.newStatus, result.Status)
			}

			if tt.errorMsg != "" && result.Error != tt.errorMsg {
				t.Errorf("Expected error message '%s', got '%s'", tt.errorMsg, result.Error)
			}

			if tt.expectEndTime && result.EndTime.IsZero() {
				t.Error("Expected non-zero EndTime for terminal status")
			}
		})
	}
}

func TestWorkflowStore_StoreResult(t *testing.T) {
	store := NewWorkflowStore()

	exec := &WorkflowExecution{
		ExecutionID: "test-result",
		Status:      WorkflowStatusRunning,
		StartTime:   time.Now(),
	}
	store.Store(exec)

	result := &loomv1.WorkflowResult{
		PatternType:  "pipeline",
		MergedOutput: "Workflow completed successfully",
		AgentResults: []*loomv1.AgentResult{
			{AgentId: "agent-1", Output: "Result 1"},
		},
	}

	// Store the result
	err := store.StoreResult("test-result", result)
	if err != nil {
		t.Fatalf("Failed to store result: %v", err)
	}

	// Verify the result was stored
	updated, _ := store.Get("test-result")
	if updated.Result == nil {
		t.Fatal("Expected non-nil result")
	}

	if updated.Status != WorkflowStatusCompleted {
		t.Errorf("Expected status '%s', got '%s'", WorkflowStatusCompleted, updated.Status)
	}

	if updated.EndTime.IsZero() {
		t.Error("Expected non-zero EndTime after storing result")
	}

	if updated.Result.MergedOutput != result.MergedOutput {
		t.Errorf("Expected merged output '%s', got '%s'", result.MergedOutput, updated.Result.MergedOutput)
	}

	// Test storing result for non-existent execution
	err = store.StoreResult("does-not-exist", result)
	if err == nil {
		t.Error("Expected error for non-existent execution")
	}
}

func TestWorkflowStore_Delete(t *testing.T) {
	store := NewWorkflowStore()

	// Store an execution
	exec := &WorkflowExecution{
		ExecutionID: "test-delete",
		Status:      WorkflowStatusCompleted,
	}
	store.Store(exec)

	// Verify it exists
	if store.Count() != 1 {
		t.Errorf("Expected count 1, got %d", store.Count())
	}

	// Delete it
	err := store.Delete("test-delete")
	if err != nil {
		t.Errorf("Failed to delete execution: %v", err)
	}

	// Verify it's gone
	if store.Count() != 0 {
		t.Errorf("Expected count 0 after delete, got %d", store.Count())
	}

	// Verify we can't retrieve it
	_, err = store.Get("test-delete")
	if err == nil {
		t.Error("Expected error when getting deleted execution")
	}

	// Test deleting non-existent execution
	err = store.Delete("does-not-exist")
	if err == nil {
		t.Error("Expected error when deleting non-existent execution")
	}
}

func TestWorkflowStore_Count(t *testing.T) {
	store := NewWorkflowStore()

	// Start with empty store
	if store.Count() != 0 {
		t.Errorf("Expected count 0, got %d", store.Count())
	}

	// Add executions
	for i := 1; i <= 5; i++ {
		exec := &WorkflowExecution{
			ExecutionID: fmt.Sprintf("exec-%d", i),
			Status:      WorkflowStatusRunning,
		}
		store.Store(exec)

		expectedCount := i
		if store.Count() != expectedCount {
			t.Errorf("After adding %d executions, expected count %d, got %d", i, expectedCount, store.Count())
		}
	}

	// Delete some
	_ = store.Delete("exec-2")
	_ = store.Delete("exec-4")

	if store.Count() != 3 {
		t.Errorf("After deleting 2 executions, expected count 3, got %d", store.Count())
	}
}

func TestWorkflowStore_ConcurrentAccess(t *testing.T) {
	store := NewWorkflowStore()

	// Number of concurrent goroutines
	numGoroutines := 50
	numOpsPerGoroutine := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOpsPerGoroutine; j++ {
				executionID := fmt.Sprintf("worker-%d-exec-%d", workerID, j)

				// Store
				exec := &WorkflowExecution{
					ExecutionID: executionID,
					Status:      WorkflowStatusRunning,
					StartTime:   time.Now(),
				}
				store.Store(exec)

				// Get
				_, err := store.Get(executionID)
				if err != nil {
					t.Errorf("Failed to get execution %s: %v", executionID, err)
				}

				// Update status
				err = store.UpdateStatus(executionID, WorkflowStatusCompleted, "")
				if err != nil {
					t.Errorf("Failed to update status for %s: %v", executionID, err)
				}

				// List
				_ = store.List("")

				// Count
				_ = store.Count()
			}
		}(i)
	}

	wg.Wait()

	// Verify final count
	expectedCount := numGoroutines * numOpsPerGoroutine
	if store.Count() != expectedCount {
		t.Errorf("Expected final count %d, got %d", expectedCount, store.Count())
	}

	// Verify all executions are completed
	completedList := store.List(WorkflowStatusCompleted)
	if len(completedList) != expectedCount {
		t.Errorf("Expected %d completed executions, got %d", expectedCount, len(completedList))
	}
}

func TestWorkflowStore_ConcurrentUpdates(t *testing.T) {
	store := NewWorkflowStore()

	// Store a single execution
	executionID := "concurrent-update-test"
	exec := &WorkflowExecution{
		ExecutionID: executionID,
		Status:      WorkflowStatusRunning,
		StartTime:   time.Now(),
	}
	store.Store(exec)

	// Multiple goroutines updating the same execution
	numUpdaters := 10
	var wg sync.WaitGroup
	wg.Add(numUpdaters)

	for i := 0; i < numUpdaters; i++ {
		go func(updaterID int) {
			defer wg.Done()

			// Each updater tries to update the status
			status := WorkflowStatusCompleted
			if updaterID%2 == 0 {
				status = WorkflowStatusFailed
			}

			err := store.UpdateStatus(executionID, status, fmt.Sprintf("updater-%d", updaterID))
			if err != nil {
				t.Errorf("Updater %d failed: %v", updaterID, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify the execution exists and has a valid status
	result, err := store.Get(executionID)
	if err != nil {
		t.Fatalf("Failed to get execution after concurrent updates: %v", err)
	}

	// Status should be either completed or failed (race condition is fine here,
	// we're just verifying no panics or data corruption)
	if result.Status != WorkflowStatusCompleted && result.Status != WorkflowStatusFailed {
		t.Errorf("Expected status to be completed or failed, got '%s'", result.Status)
	}
}
