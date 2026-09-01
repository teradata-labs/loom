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
package client

// Scheduled workflows — the routines a Loom server runs on a cron.
//
// These are the client-side half of LoomService's schedule RPCs. Every surface
// that shows routines goes through here rather than holding its own gRPC stub,
// so the TUI, the desktop app, and loom-standalone cannot drift in what a
// routine means or how one is triggered.
//
// The server answers these only when it was built with a scheduler; without one
// it reports FailedPrecondition. Callers should surface that verbatim rather
// than presenting an empty list, since "no scheduler" and "no routines" mean
// very different things to a user.

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// defaultScheduleHistoryLimit matches the server's own default so a caller that
// does not care about paging gets the same window everywhere.
const defaultScheduleHistoryLimit = 50

// ListSchedules returns the server's scheduled workflows.
//
// enabledOnly filters out paused schedules. Pass false when rendering a
// management view: a paused routine that is invisible looks deleted, and the
// user has no way to resume what they cannot see.
func (c *Client) ListSchedules(ctx context.Context, enabledOnly bool) ([]*loomv1.ScheduledWorkflow, error) {
	req := &loomv1.ListScheduledWorkflowsRequest{
		EnabledOnly: enabledOnly,
	}

	resp, err := c.client.ListScheduledWorkflows(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Schedules, nil
}

// GetSchedule retrieves one scheduled workflow by ID.
func (c *Client) GetSchedule(ctx context.Context, scheduleID string) (*loomv1.ScheduledWorkflow, error) {
	req := &loomv1.GetScheduledWorkflowRequest{
		ScheduleId: scheduleID,
	}

	return c.client.GetScheduledWorkflow(ctx, req)
}

// GetScheduleHistory returns a schedule's past executions, most recent first.
//
// A limit of 0 requests the server default. History is what makes a routine
// trustworthy — a run count and a success rate say more about whether to rely
// on a routine than its definition does — so surfaces should prefer showing
// history over showing configuration.
func (c *Client) GetScheduleHistory(ctx context.Context, scheduleID string, limit int32) ([]*loomv1.ScheduleExecution, error) {
	if limit <= 0 {
		limit = defaultScheduleHistoryLimit
	}

	req := &loomv1.GetScheduleHistoryRequest{
		ScheduleId: scheduleID,
		Limit:      limit,
	}

	resp, err := c.client.GetScheduleHistory(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Executions, nil
}

// CreateSchedule registers a new scheduled workflow and returns it as stored.
//
// The returned ScheduledWorkflow carries the server-assigned ID and the
// computed next_execution_at, which is the only honest confirmation that a
// cron expression parsed the way the user intended. Show it back to them.
func (c *Client) CreateSchedule(ctx context.Context, workflowName string, pattern *loomv1.WorkflowPattern, schedule *loomv1.ScheduleConfig, metadata map[string]string) (*loomv1.ScheduledWorkflow, error) {
	req := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: workflowName,
		Pattern:      pattern,
		Schedule:     schedule,
		Metadata:     metadata,
	}

	resp, err := c.client.ScheduleWorkflow(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Schedule, nil
}

// UpdateSchedule changes an existing schedule's pattern, timing, or both.
//
// A nil pattern or schedule leaves that part untouched, so a caller changing
// only the cron does not have to round-trip the pattern and risk writing back
// a stale copy of it.
func (c *Client) UpdateSchedule(ctx context.Context, scheduleID string, pattern *loomv1.WorkflowPattern, schedule *loomv1.ScheduleConfig) (*loomv1.ScheduledWorkflow, error) {
	req := &loomv1.UpdateScheduledWorkflowRequest{
		ScheduleId: scheduleID,
		Pattern:    pattern,
		Schedule:   schedule,
	}

	resp, err := c.client.UpdateScheduledWorkflow(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Schedule, nil
}

// DeleteSchedule removes a scheduled workflow.
func (c *Client) DeleteSchedule(ctx context.Context, scheduleID string) error {
	req := &loomv1.DeleteScheduledWorkflowRequest{
		ScheduleId: scheduleID,
	}

	_, err := c.client.DeleteScheduledWorkflow(ctx, req)
	return err
}

// TriggerSchedule runs a schedule immediately, outside its cron.
//
// This is the "run once now" path: it lets someone watch a routine work before
// trusting it to fire unattended, which is the cheapest way to earn that trust.
//
// skipIfRunning=true declines to start a second run while one is in flight.
// Prefer that default; overlapping runs of the same routine are usually a bug
// in the making, not an intent.
func (c *Client) TriggerSchedule(ctx context.Context, scheduleID string, skipIfRunning bool, variables map[string]string) (*loomv1.ExecuteWorkflowResponse, error) {
	req := &loomv1.TriggerScheduledWorkflowRequest{
		ScheduleId:    scheduleID,
		SkipIfRunning: skipIfRunning,
		Variables:     variables,
	}

	return c.client.TriggerScheduledWorkflow(ctx, req)
}

// PauseSchedule stops a schedule from firing without deleting it.
func (c *Client) PauseSchedule(ctx context.Context, scheduleID string) error {
	req := &loomv1.PauseScheduleRequest{
		ScheduleId: scheduleID,
	}

	_, err := c.client.PauseSchedule(ctx, req)
	return err
}

// ResumeSchedule re-enables a paused schedule.
func (c *Client) ResumeSchedule(ctx context.Context, scheduleID string) error {
	req := &loomv1.ResumeScheduleRequest{
		ScheduleId: scheduleID,
	}

	_, err := c.client.ResumeSchedule(ctx, req)
	return err
}

// ListWorkflowExecutions returns workflow executions known to the server.
//
// The server filters by status and pattern type, not by workflow ID — pass
// empty strings for either to leave it unfiltered. pageSize of 0 lets the
// server choose. The next page token is returned so a caller that pages does
// not have to re-derive it.
func (c *Client) ListWorkflowExecutions(ctx context.Context, statusFilter, patternTypeFilter string, pageSize int32, pageToken string) ([]*loomv1.WorkflowExecution, string, error) {
	req := &loomv1.ListWorkflowExecutionsRequest{
		StatusFilter:      statusFilter,
		PatternTypeFilter: patternTypeFilter,
		PageSize:          pageSize,
		PageToken:         pageToken,
	}

	resp, err := c.client.ListWorkflowExecutions(ctx, req)
	if err != nil {
		return nil, "", err
	}

	return resp.Executions, resp.NextPageToken, nil
}

// GetWorkflowExecution retrieves one workflow execution by ID.
func (c *Client) GetWorkflowExecution(ctx context.Context, executionID string) (*loomv1.WorkflowExecution, error) {
	req := &loomv1.GetWorkflowExecutionRequest{
		ExecutionId: executionID,
	}

	return c.client.GetWorkflowExecution(ctx, req)
}
