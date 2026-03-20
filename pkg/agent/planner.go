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
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
)

// ExecutionPlanner creates and manages execution plans in PLAN permission mode.
// When the agent is in PLAN mode, tool calls are collected into a plan rather
// than executed immediately. The plan is presented to the user for approval
// before execution proceeds.
type ExecutionPlanner struct {
	sessionID string
	plans     map[string]*loomv1.ExecutionPlan // planID -> plan
	mu        sync.RWMutex
}

// NewExecutionPlanner creates a new execution planner for a session.
func NewExecutionPlanner(sessionID string) *ExecutionPlanner {
	return &ExecutionPlanner{
		sessionID: sessionID,
		plans:     make(map[string]*loomv1.ExecutionPlan),
	}
}

// CreatePlan creates a new execution plan from LLM-generated tool calls.
// The plan includes the user's query, the LLM's reasoning, and the list of
// tool executions to perform. Returns the plan in PENDING status.
func (ep *ExecutionPlanner) CreatePlan(query string, toolCalls []types.ToolCall, reasoning string) (*loomv1.ExecutionPlan, error) {
	planID := uuid.New().String()

	// Convert tool calls to planned executions
	plannedTools := make([]*loomv1.PlannedToolExecution, 0, len(toolCalls))
	for i, tc := range toolCalls {
		// Serialize tool parameters to JSON
		paramsJSON, err := json.Marshal(tc.Input)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool params for %s: %w", tc.Name, err)
		}

		plannedTools = append(plannedTools, &loomv1.PlannedToolExecution{
			Step:       int32(i + 1), // 1-indexed steps
			ToolName:   tc.Name,
			ParamsJson: string(paramsJSON),
			Rationale:  fmt.Sprintf("Execute %s", tc.Name), // TODO: Extract from LLM reasoning
			DependsOn:  nil,                                // TODO: Analyze dependencies
			Status:     loomv1.PlannedToolExecution_STEP_STATUS_PENDING,
		})
	}

	plan := &loomv1.ExecutionPlan{
		PlanId:    planID,
		SessionId: ep.sessionID,
		Query:     query,
		Tools:     plannedTools,
		Reasoning: reasoning,
		Status:    loomv1.PlanStatus_PLAN_STATUS_PENDING,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	ep.mu.Lock()
	ep.plans[planID] = plan
	ep.mu.Unlock()

	return plan, nil
}

// ApprovePlan marks a plan as approved or rejected based on user decision.
// If approved, the plan status becomes APPROVED and is ready for execution.
// If rejected, the plan status becomes REJECTED and will not be executed.
func (ep *ExecutionPlanner) ApprovePlan(planID string, approved bool, feedback string) (*loomv1.ExecutionPlan, error) {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	plan, exists := ep.plans[planID]
	if !exists {
		return nil, fmt.Errorf("plan %s not found", planID)
	}

	// Can only approve/reject plans in PENDING status
	if plan.Status != loomv1.PlanStatus_PLAN_STATUS_PENDING {
		return nil, fmt.Errorf("plan %s is not pending (status: %v)", planID, plan.Status)
	}

	if approved {
		plan.Status = loomv1.PlanStatus_PLAN_STATUS_APPROVED
	} else {
		plan.Status = loomv1.PlanStatus_PLAN_STATUS_REJECTED
	}

	plan.UpdatedAt = time.Now().Unix()

	// TODO: Store feedback if provided
	// Could add a feedback field to ExecutionPlan proto

	return plan, nil
}

// GetPlan retrieves a specific execution plan by ID.
func (ep *ExecutionPlanner) GetPlan(planID string) (*loomv1.ExecutionPlan, error) {
	ep.mu.RLock()
	defer ep.mu.RUnlock()

	plan, exists := ep.plans[planID]
	if !exists {
		return nil, fmt.Errorf("plan %s not found", planID)
	}

	return plan, nil
}

// ListPlans returns all plans for this session, optionally filtered by status.
func (ep *ExecutionPlanner) ListPlans(statusFilter loomv1.PlanStatus) []*loomv1.ExecutionPlan {
	ep.mu.RLock()
	defer ep.mu.RUnlock()

	plans := make([]*loomv1.ExecutionPlan, 0, len(ep.plans))
	for _, plan := range ep.plans {
		// Filter by status if specified
		if statusFilter != loomv1.PlanStatus_PLAN_STATUS_UNSPECIFIED && plan.Status != statusFilter {
			continue
		}
		plans = append(plans, plan)
	}

	return plans
}

// ExecutePlan executes an approved plan by running each tool in sequence.
// Updates plan and step statuses as execution progresses.
// Returns error if plan is not approved or if any step fails.
func (ep *ExecutionPlanner) ExecutePlan(ctx context.Context, planID string, executor func(context.Context, string, map[string]interface{}) (string, error)) error {
	ep.mu.Lock()
	plan, exists := ep.plans[planID]
	if !exists {
		ep.mu.Unlock()
		return fmt.Errorf("plan %s not found", planID)
	}

	// Verify plan is approved
	if plan.Status != loomv1.PlanStatus_PLAN_STATUS_APPROVED {
		ep.mu.Unlock()
		return fmt.Errorf("plan %s is not approved (status: %v)", planID, plan.Status)
	}

	// Mark plan as executing
	plan.Status = loomv1.PlanStatus_PLAN_STATUS_EXECUTING
	plan.UpdatedAt = time.Now().Unix()
	ep.mu.Unlock()

	// Execute each tool in order
	// TODO: Respect dependencies (execute in topological order)
	// TODO: Support parallel execution for independent tools
	for _, step := range plan.Tools {
		// Parse tool parameters
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(step.ParamsJson), &params); err != nil {
			ep.markPlanFailed(planID, fmt.Sprintf("failed to parse params for step %d: %v", step.Step, err))
			return fmt.Errorf("failed to parse params for step %d: %w", step.Step, err)
		}

		// Mark step as executing
		step.Status = loomv1.PlannedToolExecution_STEP_STATUS_EXECUTING

		// Execute the tool
		result, err := executor(ctx, step.ToolName, params)
		if err != nil {
			// Mark step as failed
			step.Status = loomv1.PlannedToolExecution_STEP_STATUS_FAILED
			step.Error = err.Error()

			// Mark entire plan as failed
			ep.markPlanFailed(planID, fmt.Sprintf("step %d (%s) failed: %v", step.Step, step.ToolName, err))
			return fmt.Errorf("step %d (%s) failed: %w", step.Step, step.ToolName, err)
		}

		// Mark step as completed
		step.Status = loomv1.PlannedToolExecution_STEP_STATUS_COMPLETED
		step.Result = result
	}

	// Mark plan as completed
	ep.mu.Lock()
	plan.Status = loomv1.PlanStatus_PLAN_STATUS_COMPLETED
	plan.UpdatedAt = time.Now().Unix()
	ep.mu.Unlock()

	return nil
}

// markPlanFailed marks a plan as failed with an error message.
func (ep *ExecutionPlanner) markPlanFailed(planID string, errorMsg string) {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	if plan, exists := ep.plans[planID]; exists {
		plan.Status = loomv1.PlanStatus_PLAN_STATUS_FAILED
		plan.ErrorMessage = errorMsg
		plan.UpdatedAt = time.Now().Unix()
	}
}

// ClearPlans removes all plans for this session.
// Useful for cleanup when session ends.
func (ep *ExecutionPlanner) ClearPlans() {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	ep.plans = make(map[string]*loomv1.ExecutionPlan)
}
