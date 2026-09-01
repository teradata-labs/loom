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

import (
	"context"
	"net"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// scheduleMockServer records the requests it received. The schedule wrappers
// mostly translate arguments into request messages, so what the server was
// asked is the behaviour worth asserting — a test that only checks the returned
// value would pass even if a field were dropped on the way out.
type scheduleMockServer struct {
	loomv1.UnimplementedLoomServiceServer

	gotList    *loomv1.ListScheduledWorkflowsRequest
	gotHistory *loomv1.GetScheduleHistoryRequest
	gotTrigger *loomv1.TriggerScheduledWorkflowRequest
	gotPause   *loomv1.PauseScheduleRequest
	gotResume  *loomv1.ResumeScheduleRequest
	gotDelete  *loomv1.DeleteScheduledWorkflowRequest
	gotExecs   *loomv1.ListWorkflowExecutionsRequest
	gotCreate  *loomv1.ScheduleWorkflowRequest
	gotUpdate  *loomv1.UpdateScheduledWorkflowRequest
	gotCancel  *loomv1.CancelWorkflowExecutionRequest

	// cancelResult is what the fake reports back; false models an execution
	// that had already finished.
	cancelResult bool
}

func (m *scheduleMockServer) CancelWorkflowExecution(_ context.Context, req *loomv1.CancelWorkflowExecutionRequest) (*loomv1.CancelWorkflowExecutionResponse, error) {
	m.gotCancel = req
	if !m.cancelResult {
		return &loomv1.CancelWorkflowExecutionResponse{
			Cancelled: false,
			Message:   "execution is not running",
		}, nil
	}
	return &loomv1.CancelWorkflowExecutionResponse{
		Cancelled: true,
		Message:   "cancellation signalled",
	}, nil
}

func (m *scheduleMockServer) ListScheduledWorkflows(_ context.Context, req *loomv1.ListScheduledWorkflowsRequest) (*loomv1.ListScheduledWorkflowsResponse, error) {
	m.gotList = req
	return &loomv1.ListScheduledWorkflowsResponse{
		Schedules: []*loomv1.ScheduledWorkflow{
			{Id: "sched-1", WorkflowName: "daily-report"},
			{Id: "sched-2", WorkflowName: "hourly-sync"},
		},
	}, nil
}

func (m *scheduleMockServer) GetScheduleHistory(_ context.Context, req *loomv1.GetScheduleHistoryRequest) (*loomv1.GetScheduleHistoryResponse, error) {
	m.gotHistory = req
	return &loomv1.GetScheduleHistoryResponse{
		Executions: []*loomv1.ScheduleExecution{
			{ExecutionId: "exec-1", Status: "success", DurationMs: 4200},
		},
	}, nil
}

func (m *scheduleMockServer) TriggerScheduledWorkflow(_ context.Context, req *loomv1.TriggerScheduledWorkflowRequest) (*loomv1.ExecuteWorkflowResponse, error) {
	m.gotTrigger = req
	return &loomv1.ExecuteWorkflowResponse{ExecutionId: "exec-triggered"}, nil
}

func (m *scheduleMockServer) PauseSchedule(_ context.Context, req *loomv1.PauseScheduleRequest) (*emptypb.Empty, error) {
	m.gotPause = req
	return &emptypb.Empty{}, nil
}

func (m *scheduleMockServer) ResumeSchedule(_ context.Context, req *loomv1.ResumeScheduleRequest) (*emptypb.Empty, error) {
	m.gotResume = req
	return &emptypb.Empty{}, nil
}

func (m *scheduleMockServer) DeleteScheduledWorkflow(_ context.Context, req *loomv1.DeleteScheduledWorkflowRequest) (*emptypb.Empty, error) {
	m.gotDelete = req
	return &emptypb.Empty{}, nil
}

func (m *scheduleMockServer) ListWorkflowExecutions(_ context.Context, req *loomv1.ListWorkflowExecutionsRequest) (*loomv1.ListWorkflowExecutionsResponse, error) {
	m.gotExecs = req
	return &loomv1.ListWorkflowExecutionsResponse{
		Executions:    []*loomv1.WorkflowExecution{{Id: "wf-1", Status: "failed"}},
		NextPageToken: "page-2",
	}, nil
}

func (m *scheduleMockServer) ScheduleWorkflow(_ context.Context, req *loomv1.ScheduleWorkflowRequest) (*loomv1.ScheduleWorkflowResponse, error) {
	m.gotCreate = req
	return &loomv1.ScheduleWorkflowResponse{
		ScheduleId: "sched-new",
		Schedule:   &loomv1.ScheduledWorkflow{Id: "sched-new", NextExecutionAt: 1700000000},
	}, nil
}

func (m *scheduleMockServer) UpdateScheduledWorkflow(_ context.Context, req *loomv1.UpdateScheduledWorkflowRequest) (*loomv1.ScheduleWorkflowResponse, error) {
	m.gotUpdate = req
	return &loomv1.ScheduleWorkflowResponse{
		ScheduleId: req.ScheduleId,
		Schedule:   &loomv1.ScheduledWorkflow{Id: req.ScheduleId},
	}, nil
}

// setupScheduleServer stands up the recording mock over bufconn and returns a
// client wired to it.
func setupScheduleServer(t *testing.T) (*Client, *scheduleMockServer) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	mock := &scheduleMockServer{}
	srv := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(srv, mock)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("server error: %v", err)
		}
	}()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &Client{
		conn:   conn,
		client: loomv1.NewLoomServiceClient(conn),
		addr:   "passthrough:///bufnet",
	}, mock
}

func TestListSchedules(t *testing.T) {
	c, mock := setupScheduleServer(t)

	for _, enabledOnly := range []bool{true, false} {
		schedules, err := c.ListSchedules(context.Background(), enabledOnly)
		if err != nil {
			t.Fatalf("ListSchedules(%v): %v", enabledOnly, err)
		}
		if len(schedules) != 2 {
			t.Errorf("got %d schedules, want 2", len(schedules))
		}
		if mock.gotList.EnabledOnly != enabledOnly {
			t.Errorf("server saw EnabledOnly=%v, want %v", mock.gotList.EnabledOnly, enabledOnly)
		}
	}
}

// A limit of 0 must become the documented default rather than reaching the
// server as 0, which some servers read as "none".
func TestGetScheduleHistoryLimitDefaulting(t *testing.T) {
	tests := []struct {
		name     string
		limit    int32
		wantSent int32
	}{
		{"zero becomes the default", 0, defaultScheduleHistoryLimit},
		{"negative becomes the default", -5, defaultScheduleHistoryLimit},
		{"explicit limit is passed through", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, mock := setupScheduleServer(t)

			execs, err := c.GetScheduleHistory(context.Background(), "sched-1", tt.limit)
			if err != nil {
				t.Fatalf("GetScheduleHistory: %v", err)
			}
			if len(execs) != 1 {
				t.Fatalf("got %d executions, want 1", len(execs))
			}
			if mock.gotHistory.Limit != tt.wantSent {
				t.Errorf("server saw Limit=%d, want %d", mock.gotHistory.Limit, tt.wantSent)
			}
			if mock.gotHistory.ScheduleId != "sched-1" {
				t.Errorf("server saw ScheduleId=%q, want %q", mock.gotHistory.ScheduleId, "sched-1")
			}
		})
	}
}

// Trigger is the "run once now" path; dropping skip_if_running or the variables
// would silently change what the user asked to run.
func TestTriggerSchedule(t *testing.T) {
	c, mock := setupScheduleServer(t)

	vars := map[string]string{"region": "emea"}
	resp, err := c.TriggerSchedule(context.Background(), "sched-1", true, vars)
	if err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	if resp.ExecutionId != "exec-triggered" {
		t.Errorf("ExecutionId = %q, want %q", resp.ExecutionId, "exec-triggered")
	}
	if mock.gotTrigger.ScheduleId != "sched-1" {
		t.Errorf("ScheduleId = %q, want sched-1", mock.gotTrigger.ScheduleId)
	}
	if !mock.gotTrigger.SkipIfRunning {
		t.Error("SkipIfRunning was not forwarded")
	}
	if mock.gotTrigger.Variables["region"] != "emea" {
		t.Errorf("Variables = %v, want region=emea", mock.gotTrigger.Variables)
	}
}

func TestPauseResumeDeleteSchedule(t *testing.T) {
	c, mock := setupScheduleServer(t)
	ctx := context.Background()

	if err := c.PauseSchedule(ctx, "sched-1"); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}
	if mock.gotPause.ScheduleId != "sched-1" {
		t.Errorf("pause saw %q", mock.gotPause.ScheduleId)
	}

	if err := c.ResumeSchedule(ctx, "sched-2"); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}
	if mock.gotResume.ScheduleId != "sched-2" {
		t.Errorf("resume saw %q", mock.gotResume.ScheduleId)
	}

	if err := c.DeleteSchedule(ctx, "sched-3"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if mock.gotDelete.ScheduleId != "sched-3" {
		t.Errorf("delete saw %q", mock.gotDelete.ScheduleId)
	}
}

// CreateSchedule must return the stored schedule, not just its ID: the computed
// next_execution_at is the only confirmation that a cron parsed as intended.
func TestCreateSchedule(t *testing.T) {
	c, mock := setupScheduleServer(t)

	cfg := &loomv1.ScheduleConfig{Cron: "0 8 * * *", Timezone: "America/New_York", Enabled: true}
	sched, err := c.CreateSchedule(context.Background(), "daily-report", &loomv1.WorkflowPattern{}, cfg, map[string]string{"owner": "analyst"})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sched.NextExecutionAt == 0 {
		t.Error("returned schedule has no NextExecutionAt; callers rely on it to confirm the cron")
	}
	if mock.gotCreate.WorkflowName != "daily-report" {
		t.Errorf("WorkflowName = %q", mock.gotCreate.WorkflowName)
	}
	if mock.gotCreate.Schedule.GetCron() != "0 8 * * *" {
		t.Errorf("Cron = %q", mock.gotCreate.Schedule.GetCron())
	}
	if mock.gotCreate.Metadata["owner"] != "analyst" {
		t.Errorf("Metadata = %v", mock.gotCreate.Metadata)
	}
}

// A nil pattern or schedule means "leave that part alone", so it must reach the
// server as nil rather than as a zero-valued message that would overwrite.
func TestUpdateScheduleLeavesNilPartsAlone(t *testing.T) {
	c, mock := setupScheduleServer(t)

	cfg := &loomv1.ScheduleConfig{Cron: "*/15 * * * *"}
	if _, err := c.UpdateSchedule(context.Background(), "sched-1", nil, cfg); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if mock.gotUpdate.Pattern != nil {
		t.Error("nil pattern reached the server as a non-nil message; it would overwrite the stored pattern")
	}
	if mock.gotUpdate.Schedule.GetCron() != "*/15 * * * *" {
		t.Errorf("Cron = %q", mock.gotUpdate.Schedule.GetCron())
	}
}

func TestListWorkflowExecutions(t *testing.T) {
	c, mock := setupScheduleServer(t)

	execs, next, err := c.ListWorkflowExecutions(context.Background(), "failed", "pipeline", 25, "")
	if err != nil {
		t.Fatalf("ListWorkflowExecutions: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("got %d executions, want 1", len(execs))
	}
	if next != "page-2" {
		t.Errorf("next page token = %q, want page-2", next)
	}
	if mock.gotExecs.StatusFilter != "failed" || mock.gotExecs.PatternTypeFilter != "pipeline" {
		t.Errorf("filters not forwarded: status=%q pattern=%q", mock.gotExecs.StatusFilter, mock.gotExecs.PatternTypeFilter)
	}
	if mock.gotExecs.PageSize != 25 {
		t.Errorf("PageSize = %d, want 25", mock.gotExecs.PageSize)
	}
}

// Wrap borrows the caller's connection. Close must not tear it down, or a
// surface that shares one conn between its own stubs and the SDK would kill
// its own bindings by closing the SDK.
func TestWrapBorrowsConnection(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(srv, &scheduleMockServer{})
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("server error: %v", err)
		}
	}()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("create conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sdk := Wrap(conn)
	if sdk == nil {
		t.Fatal("Wrap returned nil for a non-nil conn")
	}

	if _, err := sdk.ListSchedules(context.Background(), false); err != nil {
		t.Fatalf("ListSchedules through a wrapped conn: %v", err)
	}

	if err := sdk.Close(); err != nil {
		t.Fatalf("Close on a borrowed conn should be a no-op: %v", err)
	}

	// The conn must still work after the SDK was closed.
	if _, err := sdk.ListSchedules(context.Background(), false); err != nil {
		t.Errorf("conn was closed by the borrower: %v", err)
	}
}

func TestWrapNilConn(t *testing.T) {
	if got := Wrap(nil); got != nil {
		t.Errorf("Wrap(nil) = %v, want nil", got)
	}
}

// Cancel must forward the reason — the history entry depends on it to read as an
// operator stop rather than a crash.
func TestCancelExecution(t *testing.T) {
	c, mock := setupScheduleServer(t)
	mock.cancelResult = true

	cancelled, msg, err := c.CancelExecution(context.Background(), "exec-1", "operator stop")
	if err != nil {
		t.Fatalf("CancelExecution: %v", err)
	}
	if !cancelled {
		t.Error("cancelled = false, want true")
	}
	if msg == "" {
		t.Error("message is empty; callers show it directly")
	}
	if mock.gotCancel.ExecutionId != "exec-1" {
		t.Errorf("ExecutionId = %q", mock.gotCancel.ExecutionId)
	}
	if mock.gotCancel.Reason != "operator stop" {
		t.Errorf("Reason = %q, want it forwarded", mock.gotCancel.Reason)
	}
}

// An already-finished execution comes back as cancelled=false with no error.
// Treating that as a failure would have the UI apologise for a race it won.
func TestCancelExecutionAlreadyFinished(t *testing.T) {
	c, mock := setupScheduleServer(t)
	mock.cancelResult = false

	cancelled, msg, err := c.CancelExecution(context.Background(), "exec-done", "")
	if err != nil {
		t.Fatalf("an already-finished execution must not error: %v", err)
	}
	if cancelled {
		t.Error("cancelled = true for a finished execution")
	}
	if msg == "" {
		t.Error("message is empty; it is the only thing distinguishing this case")
	}
}
