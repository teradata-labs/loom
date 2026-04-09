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

package server

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/task"
)

// TaskServiceImpl implements the loomv1.TaskServiceServer gRPC interface.
// It delegates to the task.Manager for business logic and subscribes to the
// message bus for streaming task updates to TUI/CLI clients.
type TaskServiceImpl struct {
	loomv1.UnimplementedTaskServiceServer
	manager *task.Manager
	bus     *communication.MessageBus
	logger  *zap.Logger
}

// SetBus sets the message bus for streaming task updates. Supports two-phase init.
func (s *TaskServiceImpl) SetBus(bus *communication.MessageBus) {
	s.bus = bus
}

// NewTaskServiceImpl creates a new TaskService gRPC handler.
func NewTaskServiceImpl(manager *task.Manager, bus *communication.MessageBus, logger *zap.Logger) *TaskServiceImpl {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TaskServiceImpl{manager: manager, bus: bus, logger: logger}
}

// =============================================================================
// Streaming — real-time task updates for TUI
// =============================================================================

// StreamTaskUpdates streams real-time task status changes by subscribing to
// the message bus topics: task.created, task.claimed, task.completed, etc.
func (s *TaskServiceImpl) StreamTaskUpdates(req *loomv1.StreamTaskUpdatesRequest, stream loomv1.TaskService_StreamTaskUpdatesServer) error {
	if s.bus == nil {
		return status.Error(codes.Unavailable, "message bus not configured")
	}

	// Subscribe to all task topics via wildcard.
	sub, err := s.bus.Subscribe(stream.Context(), "task-tui-stream", "task.*", nil, 100)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to subscribe: %v", err)
	}
	defer s.bus.Unsubscribe(stream.Context(), sub.ID) //nolint:errcheck

	s.logger.Info("TUI client subscribed to task updates",
		zap.String("board_id", req.BoardId),
		zap.String("agent_id", req.AgentId))

	for {
		select {
		case msg, ok := <-sub.Channel:
			if !ok {
				return nil
			}

			update := busMessageToTaskUpdate(msg)
			if update == nil {
				continue
			}

			// Apply filters.
			if req.BoardId != "" && update.Task != nil && update.Task.BoardId != req.BoardId {
				continue
			}
			if req.AgentId != "" && update.AgentId != req.AgentId {
				continue
			}

			if err := stream.Send(update); err != nil {
				return err
			}

		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// =============================================================================
// CRUD RPCs
// =============================================================================

func (s *TaskServiceImpl) CreateTask(ctx context.Context, req *loomv1.CreateTaskRequest) (*loomv1.CreateTaskResponse, error) {
	if req.Task == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}
	t := protoToTask(req.Task)
	created, err := s.manager.CreateTask(ctx, t)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create task: %v", err)
	}
	return &loomv1.CreateTaskResponse{Task: taskToProto(created)}, nil
}

func (s *TaskServiceImpl) GetTask(ctx context.Context, req *loomv1.GetTaskRequest) (*loomv1.GetTaskResponse, error) {
	t, err := s.manager.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}
	deps, _ := s.manager.Store().GetDependencies(ctx, req.TaskId)
	dependents, _ := s.manager.Store().GetDependents(ctx, req.TaskId)
	return &loomv1.GetTaskResponse{
		Task:         taskToProto(t),
		Dependencies: depsToProto(deps),
		Dependents:   depsToProto(dependents),
	}, nil
}

func (s *TaskServiceImpl) ListTasks(ctx context.Context, req *loomv1.ListTasksRequest) (*loomv1.ListTasksResponse, error) {
	tasks, total, err := s.manager.ListTasks(ctx, task.ListTasksOpts{
		BoardID:         req.BoardId,
		Status:          req.Status,
		Priority:        req.Priority,
		Category:        req.Category,
		AssigneeAgentID: req.AssigneeAgentId,
		ParentID:        req.ParentId,
		Query:           req.Query,
		Limit:           int(req.Limit),
		Offset:          int(req.Offset),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tasks: %v", err)
	}
	protoTasks := make([]*loomv1.Task, 0, len(tasks))
	for _, t := range tasks {
		protoTasks = append(protoTasks, taskToProto(t))
	}
	return &loomv1.ListTasksResponse{Tasks: protoTasks, TotalCount: int32(total)}, nil // #nosec G115
}

func (s *TaskServiceImpl) ClaimTask(ctx context.Context, req *loomv1.ClaimTaskRequest) (*loomv1.ClaimTaskResponse, error) {
	claimed, err := s.manager.ClaimTask(ctx, req.TaskId, req.AgentId, req.SessionId)
	if err != nil {
		return &loomv1.ClaimTaskResponse{Success: false, Error: err.Error()}, nil
	}
	return &loomv1.ClaimTaskResponse{Task: taskToProto(claimed), Success: true}, nil
}

func (s *TaskServiceImpl) CloseTask(ctx context.Context, req *loomv1.CloseTaskRequest) (*loomv1.CloseTaskResponse, error) {
	closed, err := s.manager.CloseTask(ctx, req.TaskId, req.CloseReason)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "close task: %v", err)
	}
	return &loomv1.CloseTaskResponse{Task: taskToProto(closed), ValidationPassed: true}, nil
}

func (s *TaskServiceImpl) GetReadyFront(ctx context.Context, req *loomv1.GetReadyFrontRequest) (*loomv1.GetReadyFrontResponse, error) {
	tasks, err := s.manager.GetReadyFront(ctx, req.BoardId, task.ReadyFrontOpts{
		AgentID:     req.AgentId,
		MinPriority: req.MinPriority,
		MaxResults:  int(req.MaxResults),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get ready front: %v", err)
	}
	protoTasks := make([]*loomv1.Task, 0, len(tasks))
	for _, t := range tasks {
		protoTasks = append(protoTasks, taskToProto(t))
	}
	return &loomv1.GetReadyFrontResponse{ReadyTasks: protoTasks}, nil
}

func (s *TaskServiceImpl) CreateBoard(ctx context.Context, req *loomv1.CreateBoardRequest) (*loomv1.CreateBoardResponse, error) {
	if req.Board == nil {
		return nil, status.Error(codes.InvalidArgument, "board is required")
	}
	board := protoToBoard(req.Board)
	created, err := s.manager.CreateBoard(ctx, board)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create board: %v", err)
	}
	return &loomv1.CreateBoardResponse{Board: boardToProto(created)}, nil
}

func (s *TaskServiceImpl) GetBoard(ctx context.Context, req *loomv1.GetBoardRequest) (*loomv1.GetBoardResponse, error) {
	board, err := s.manager.GetBoard(ctx, req.BoardId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "board not found: %v", err)
	}
	// Compute stats.
	allTasks, total, _ := s.manager.ListTasks(ctx, task.ListTasksOpts{BoardID: req.BoardId, Limit: 1000})
	stats := &loomv1.TaskBoardStats{Total: int32(total)} // #nosec G115
	for _, t := range allTasks {
		switch t.Status {
		case loomv1.TaskStatus_TASK_STATUS_OPEN:
			stats.Open++
		case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
			stats.InProgress++
		case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
			stats.Blocked++
		case loomv1.TaskStatus_TASK_STATUS_DONE:
			stats.Done++
		case loomv1.TaskStatus_TASK_STATUS_DEFERRED:
			stats.Deferred++
		case loomv1.TaskStatus_TASK_STATUS_CANCELLED:
			stats.Cancelled++
		}
	}
	return &loomv1.GetBoardResponse{Board: boardToProto(board), Stats: stats}, nil
}

func (s *TaskServiceImpl) ListBoards(ctx context.Context, _ *loomv1.ListBoardsRequest) (*loomv1.ListBoardsResponse, error) {
	boards, err := s.manager.ListBoards(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list boards: %v", err)
	}
	protoBoards := make([]*loomv1.TaskBoard, 0, len(boards))
	for _, b := range boards {
		protoBoards = append(protoBoards, boardToProto(b))
	}
	return &loomv1.ListBoardsResponse{Boards: protoBoards}, nil
}

// =============================================================================
// Converters: proto ↔ domain
// =============================================================================

func busMessageToTaskUpdate(msg *loomv1.BusMessage) *loomv1.TaskUpdate {
	if msg == nil {
		return nil
	}
	update := &loomv1.TaskUpdate{
		Action:    msg.Topic,
		AgentId:   msg.FromAgent,
		Timestamp: msg.Timestamp,
	}
	// Try to unmarshal the task from the payload.
	if msg.Payload != nil {
		if val, ok := msg.Payload.Data.(*loomv1.MessagePayload_Value); ok && val.Value != nil {
			var t task.Task
			if json.Unmarshal(val.Value, &t) == nil {
				update.Task = taskToProto(&t)
			}
		}
	}
	return update
}

func taskToProto(t *task.Task) *loomv1.Task {
	if t == nil {
		return nil
	}
	p := &loomv1.Task{
		Id:                 t.ID,
		Title:              t.Title,
		Description:        t.Description,
		Objective:          t.Objective,
		Approach:           t.Approach,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Notes:              t.Notes,
		Status:             t.Status,
		Priority:           t.Priority,
		Category:           t.Category,
		Tags:               t.Tags,
		OwnerAgentId:       t.OwnerAgentID,
		AssigneeAgentId:    t.AssigneeAgentID,
		ClaimedBySession:   t.ClaimedBySession,
		CloseReason:        t.CloseReason,
		ParentId:           t.ParentID,
		ChildIds:           t.ChildIDs,
		EntityIds:          t.EntityIDs,
		BoardId:            t.BoardID,
		CompactionLevel:    int32(t.CompactionLevel), // #nosec G115
		CompactedSummary:   t.CompactedSummary,
		EstimatedEffort:    t.EstimatedEffort,
		CreatedAt:          t.CreatedAt.UnixMilli(),
		UpdatedAt:          t.UpdatedAt.UnixMilli(),
	}
	if t.ClaimedAt != nil {
		p.ClaimedAt = t.ClaimedAt.UnixMilli()
	}
	if t.ClosedAt != nil {
		p.ClosedAt = t.ClosedAt.UnixMilli()
	}
	if t.Metadata != nil {
		p.Metadata = t.Metadata
	}
	return p
}

func protoToTask(p *loomv1.Task) *task.Task {
	if p == nil {
		return nil
	}
	return &task.Task{
		ID:                 p.Id,
		Title:              p.Title,
		Description:        p.Description,
		Objective:          p.Objective,
		Approach:           p.Approach,
		AcceptanceCriteria: p.AcceptanceCriteria,
		Notes:              p.Notes,
		Status:             p.Status,
		Priority:           p.Priority,
		Category:           p.Category,
		Tags:               p.Tags,
		OwnerAgentID:       p.OwnerAgentId,
		AssigneeAgentID:    p.AssigneeAgentId,
		ClaimedBySession:   p.ClaimedBySession,
		CloseReason:        p.CloseReason,
		ParentID:           p.ParentId,
		EntityIDs:          p.EntityIds,
		Metadata:           p.Metadata,
		BoardID:            p.BoardId,
		EstimatedEffort:    p.EstimatedEffort,
	}
}

func depsToProto(deps []*task.TaskDependency) []*loomv1.TaskDependency {
	result := make([]*loomv1.TaskDependency, 0, len(deps))
	for _, d := range deps {
		result = append(result, &loomv1.TaskDependency{
			FromTaskId: d.FromTaskID,
			ToTaskId:   d.ToTaskID,
			Type:       d.Type,
			CreatedAt:  d.CreatedAt.UnixMilli(),
			CreatedBy:  d.CreatedBy,
		})
	}
	return result
}

func boardToProto(b *task.TaskBoard) *loomv1.TaskBoard {
	if b == nil {
		return nil
	}
	lanes := make([]*loomv1.TaskLane, 0, len(b.Lanes))
	for _, l := range b.Lanes {
		lanes = append(lanes, &loomv1.TaskLane{
			Name:     l.Name,
			Status:   l.Status,
			TaskIds:  l.TaskIDs,
			WipLimit: int32(l.WIPLimit), // #nosec G115
		})
	}
	return &loomv1.TaskBoard{
		Id:         b.ID,
		Name:       b.Name,
		WorkflowId: b.WorkflowID,
		Lanes:      lanes,
		Metadata:   b.Metadata,
		CreatedAt:  b.CreatedAt.UnixMilli(),
	}
}

func protoToBoard(p *loomv1.TaskBoard) *task.TaskBoard {
	if p == nil {
		return nil
	}
	lanes := make([]task.TaskLane, 0, len(p.Lanes))
	for _, l := range p.Lanes {
		lanes = append(lanes, task.TaskLane{
			Name:     l.Name,
			Status:   l.Status,
			TaskIDs:  l.TaskIds,
			WIPLimit: int(l.WipLimit),
		})
	}
	return &task.TaskBoard{
		Name:       p.Name,
		WorkflowID: p.WorkflowId,
		Lanes:      lanes,
		Metadata:   p.Metadata,
	}
}
