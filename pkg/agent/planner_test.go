// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"fmt"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewExecutionPlanner(t *testing.T) {
	sessionID := "test-session-123"
	planner := NewExecutionPlanner(sessionID)

	if planner == nil {
		t.Fatal("expected non-nil planner")
	}

	if planner.sessionID != sessionID {
		t.Errorf("sessionID = %q, want %q", planner.sessionID, sessionID)
	}

	if planner.plans == nil {
		t.Error("expected initialized plans map")
	}
}

func TestCreatePlan(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	toolCalls := []types.ToolCall{
		{
			ID:   "call-1",
			Name: "execute_sql",
			Input: map[string]interface{}{
				"query": "SELECT * FROM users",
			},
		},
		{
			ID:   "call-2",
			Name: "file_write",
			Input: map[string]interface{}{
				"path":    "/tmp/results.csv",
				"content": "data",
			},
		},
	}

	plan, err := planner.CreatePlan("analyze users", toolCalls, "First query the database, then save results")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Verify plan fields
	if plan.PlanId == "" {
		t.Error("expected non-empty plan ID")
	}

	if plan.SessionId != "session-1" {
		t.Errorf("SessionId = %q, want %q", plan.SessionId, "session-1")
	}

	if plan.Query != "analyze users" {
		t.Errorf("Query = %q, want %q", plan.Query, "analyze users")
	}

	if plan.Reasoning != "First query the database, then save results" {
		t.Errorf("Reasoning = %q", plan.Reasoning)
	}

	if plan.Status != loomv1.PlanStatus_PLAN_STATUS_PENDING {
		t.Errorf("Status = %v, want PENDING", plan.Status)
	}

	if len(plan.Tools) != 2 {
		t.Fatalf("len(Tools) = %d, want 2", len(plan.Tools))
	}

	// Verify first tool
	tool1 := plan.Tools[0]
	if tool1.Step != 1 {
		t.Errorf("Step = %d, want 1", tool1.Step)
	}
	if tool1.ToolName != "execute_sql" {
		t.Errorf("ToolName = %q, want execute_sql", tool1.ToolName)
	}
	if tool1.Status != loomv1.PlannedToolExecution_STEP_STATUS_PENDING {
		t.Errorf("Status = %v, want PENDING", tool1.Status)
	}

	// Verify second tool
	tool2 := plan.Tools[1]
	if tool2.Step != 2 {
		t.Errorf("Step = %d, want 2", tool2.Step)
	}
	if tool2.ToolName != "file_write" {
		t.Errorf("ToolName = %q, want file_write", tool2.ToolName)
	}

	// Verify timestamps
	if plan.CreatedAt == 0 {
		t.Error("expected non-zero CreatedAt")
	}
	if plan.UpdatedAt == 0 {
		t.Error("expected non-zero UpdatedAt")
	}
}

func TestApprovePlan(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create a plan
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "test_tool", Input: map[string]interface{}{}},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Approve the plan
	approved, err := planner.ApprovePlan(plan.PlanId, true, "looks good")
	if err != nil {
		t.Fatalf("ApprovePlan failed: %v", err)
	}

	if approved.Status != loomv1.PlanStatus_PLAN_STATUS_APPROVED {
		t.Errorf("Status = %v, want APPROVED", approved.Status)
	}

	// Verify plan is updated in map
	retrieved, err := planner.GetPlan(plan.PlanId)
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}

	if retrieved.Status != loomv1.PlanStatus_PLAN_STATUS_APPROVED {
		t.Errorf("retrieved plan Status = %v, want APPROVED", retrieved.Status)
	}
}

func TestRejectPlan(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create a plan
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "test_tool", Input: map[string]interface{}{}},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Reject the plan
	rejected, err := planner.ApprovePlan(plan.PlanId, false, "not what I wanted")
	if err != nil {
		t.Fatalf("ApprovePlan failed: %v", err)
	}

	if rejected.Status != loomv1.PlanStatus_PLAN_STATUS_REJECTED {
		t.Errorf("Status = %v, want REJECTED", rejected.Status)
	}
}

func TestApprovePlan_NotPending(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create and approve a plan
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "test_tool", Input: map[string]interface{}{}},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	_, err = planner.ApprovePlan(plan.PlanId, true, "")
	if err != nil {
		t.Fatalf("first ApprovePlan failed: %v", err)
	}

	// Try to approve again (should fail)
	_, err = planner.ApprovePlan(plan.PlanId, true, "")
	if err == nil {
		t.Error("expected error when approving non-pending plan")
	}
}

func TestApprovePlan_NotFound(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	_, err := planner.ApprovePlan("nonexistent-plan-id", true, "")
	if err == nil {
		t.Error("expected error for nonexistent plan")
	}
}

func TestGetPlan(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create a plan
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "test_tool", Input: map[string]interface{}{}},
	}
	created, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Retrieve it
	retrieved, err := planner.GetPlan(created.PlanId)
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}

	if retrieved.PlanId != created.PlanId {
		t.Errorf("PlanId = %q, want %q", retrieved.PlanId, created.PlanId)
	}
}

func TestGetPlan_NotFound(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	_, err := planner.GetPlan("nonexistent-plan-id")
	if err == nil {
		t.Error("expected error for nonexistent plan")
	}
}

func TestListPlans(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create multiple plans with different statuses
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "test_tool", Input: map[string]interface{}{}},
	}

	// Create pending plan
	plan1, _ := planner.CreatePlan("query1", toolCalls, "reasoning1")

	// Create and approve plan
	plan2, _ := planner.CreatePlan("query2", toolCalls, "reasoning2")
	planner.ApprovePlan(plan2.PlanId, true, "")

	// Create and reject plan
	plan3, _ := planner.CreatePlan("query3", toolCalls, "reasoning3")
	planner.ApprovePlan(plan3.PlanId, false, "")

	// List all plans
	allPlans := planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_UNSPECIFIED)
	if len(allPlans) != 3 {
		t.Errorf("len(allPlans) = %d, want 3", len(allPlans))
	}

	// List only pending plans
	pendingPlans := planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_PENDING)
	if len(pendingPlans) != 1 {
		t.Errorf("len(pendingPlans) = %d, want 1", len(pendingPlans))
	}
	if pendingPlans[0].PlanId != plan1.PlanId {
		t.Error("expected pending plan to be plan1")
	}

	// List only approved plans
	approvedPlans := planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_APPROVED)
	if len(approvedPlans) != 1 {
		t.Errorf("len(approvedPlans) = %d, want 1", len(approvedPlans))
	}

	// List only rejected plans
	rejectedPlans := planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_REJECTED)
	if len(rejectedPlans) != 1 {
		t.Errorf("len(rejectedPlans) = %d, want 1", len(rejectedPlans))
	}
}

func TestExecutePlan_Success(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create and approve a plan
	toolCalls := []types.ToolCall{
		{
			ID:   "call-1",
			Name: "tool1",
			Input: map[string]interface{}{
				"arg": "value1",
			},
		},
		{
			ID:   "call-2",
			Name: "tool2",
			Input: map[string]interface{}{
				"arg": "value2",
			},
		},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	_, err = planner.ApprovePlan(plan.PlanId, true, "")
	if err != nil {
		t.Fatalf("ApprovePlan failed: %v", err)
	}

	// Mock executor
	executedTools := []string{}
	executor := func(ctx context.Context, toolName string, params map[string]interface{}) (string, error) {
		executedTools = append(executedTools, toolName)
		return fmt.Sprintf("result from %s", toolName), nil
	}

	// Execute the plan
	ctx := context.Background()
	err = planner.ExecutePlan(ctx, plan.PlanId, executor)
	if err != nil {
		t.Fatalf("ExecutePlan failed: %v", err)
	}

	// Verify tools were executed in order
	if len(executedTools) != 2 {
		t.Fatalf("len(executedTools) = %d, want 2", len(executedTools))
	}
	if executedTools[0] != "tool1" || executedTools[1] != "tool2" {
		t.Errorf("executedTools = %v, want [tool1, tool2]", executedTools)
	}

	// Verify plan status
	completed, err := planner.GetPlan(plan.PlanId)
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}

	if completed.Status != loomv1.PlanStatus_PLAN_STATUS_COMPLETED {
		t.Errorf("Status = %v, want COMPLETED", completed.Status)
	}

	// Verify all steps completed
	for i, step := range completed.Tools {
		if step.Status != loomv1.PlannedToolExecution_STEP_STATUS_COMPLETED {
			t.Errorf("step %d Status = %v, want COMPLETED", i, step.Status)
		}
		if step.Result == "" {
			t.Errorf("step %d has empty result", i)
		}
	}
}

func TestExecutePlan_ToolFailure(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create and approve a plan
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "tool1", Input: map[string]interface{}{}},
		{ID: "call-2", Name: "tool2", Input: map[string]interface{}{}},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	_, err = planner.ApprovePlan(plan.PlanId, true, "")
	if err != nil {
		t.Fatalf("ApprovePlan failed: %v", err)
	}

	// Mock executor that fails on second tool
	executedTools := []string{}
	executor := func(ctx context.Context, toolName string, params map[string]interface{}) (string, error) {
		executedTools = append(executedTools, toolName)
		if toolName == "tool2" {
			return "", fmt.Errorf("tool2 failed")
		}
		return "success", nil
	}

	// Execute the plan (should fail)
	ctx := context.Background()
	err = planner.ExecutePlan(ctx, plan.PlanId, executor)
	if err == nil {
		t.Fatal("expected ExecutePlan to fail")
	}

	// Verify plan is marked as failed
	failed, err := planner.GetPlan(plan.PlanId)
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}

	if failed.Status != loomv1.PlanStatus_PLAN_STATUS_FAILED {
		t.Errorf("Status = %v, want FAILED", failed.Status)
	}

	if failed.ErrorMessage == "" {
		t.Error("expected non-empty error message")
	}

	// Verify first step succeeded, second failed
	if failed.Tools[0].Status != loomv1.PlannedToolExecution_STEP_STATUS_COMPLETED {
		t.Errorf("step 1 Status = %v, want COMPLETED", failed.Tools[0].Status)
	}
	if failed.Tools[1].Status != loomv1.PlannedToolExecution_STEP_STATUS_FAILED {
		t.Errorf("step 2 Status = %v, want FAILED", failed.Tools[1].Status)
	}
	if failed.Tools[1].Error == "" {
		t.Error("expected non-empty error for failed step")
	}
}

func TestExecutePlan_NotApproved(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create a plan but don't approve it
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "tool1", Input: map[string]interface{}{}},
	}
	plan, err := planner.CreatePlan("test query", toolCalls, "test reasoning")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	// Try to execute unapproved plan
	executor := func(ctx context.Context, toolName string, params map[string]interface{}) (string, error) {
		return "success", nil
	}

	ctx := context.Background()
	err = planner.ExecutePlan(ctx, plan.PlanId, executor)
	if err == nil {
		t.Error("expected error when executing unapproved plan")
	}
}

func TestClearPlans(t *testing.T) {
	planner := NewExecutionPlanner("session-1")

	// Create some plans
	toolCalls := []types.ToolCall{
		{ID: "call-1", Name: "tool1", Input: map[string]interface{}{}},
	}
	planner.CreatePlan("query1", toolCalls, "reasoning1")
	planner.CreatePlan("query2", toolCalls, "reasoning2")

	// Verify plans exist
	allPlans := planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_UNSPECIFIED)
	if len(allPlans) != 2 {
		t.Fatalf("len(allPlans) = %d, want 2", len(allPlans))
	}

	// Clear plans
	planner.ClearPlans()

	// Verify plans are gone
	allPlans = planner.ListPlans(loomv1.PlanStatus_PLAN_STATUS_UNSPECIFIED)
	if len(allPlans) != 0 {
		t.Errorf("len(allPlans) = %d, want 0 after ClearPlans", len(allPlans))
	}
}
