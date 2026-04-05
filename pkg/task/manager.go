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

package task

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Bus topics for task lifecycle events.
const (
	TopicTaskCreated   = "task.created"
	TopicTaskClaimed   = "task.claimed"
	TopicTaskReleased  = "task.released"
	TopicTaskCompleted = "task.completed"
	TopicTaskBlocked   = "task.blocked"
	TopicTaskUpdated   = "task.updated"
	TopicTaskDeleted   = "task.deleted"
	TopicBoardUpdated  = "board.updated"
)

// Manager provides business logic on top of TaskStore.
// It handles cycle detection, auto-status propagation, event publishing,
// WIP limit enforcement, and task compaction.
type Manager struct {
	store       TaskStore
	bus         *communication.MessageBus // optional, nil = no events
	graphMemory memory.GraphMemoryStore   // optional, nil = no memory integration
	tracer      observability.Tracer
	logger      *zap.Logger
}

// NewManager creates a new task manager.
func NewManager(store TaskStore, bus *communication.MessageBus, tracer observability.Tracer, logger *zap.Logger) *Manager {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Manager{store: store, bus: bus, tracer: tracer, logger: logger}
}

// Store returns the underlying TaskStore for direct access when needed.
func (m *Manager) Store() TaskStore {
	return m.store
}

// SetBus sets the message bus for event publishing. This supports two-phase
// initialization where the Manager is created before the bus is available.
func (m *Manager) SetBus(bus *communication.MessageBus) {
	m.bus = bus
}

// SetGraphMemory sets the graph memory store for auto-creating memories
// when tasks are completed. Supports two-phase initialization.
func (m *Manager) SetGraphMemory(store memory.GraphMemoryStore) {
	m.graphMemory = store
}

// =============================================================================
// Task CRUD (delegates to store, adds events + history)
// =============================================================================

// CreateTask creates a task and publishes a task.created event.
func (m *Manager) CreateTask(ctx context.Context, t *Task) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.create")
	defer m.tracer.EndSpan(span)

	created, err := m.store.CreateTask(ctx, t)
	if err != nil {
		return nil, err
	}

	m.recordHistory(ctx, created.ID, "created", "", StatusName(created.Status), t.OwnerAgentID, "")
	m.publishEvent(ctx, TopicTaskCreated, created, t.OwnerAgentID)

	return created, nil
}

// GetTask retrieves a task by ID and populates ChildIDs.
func (m *Manager) GetTask(ctx context.Context, id string) (*Task, error) {
	t, err := m.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	m.populateChildIDs(ctx, t)
	return t, nil
}

// UpdateTask updates a task and publishes a task.updated event.
func (m *Manager) UpdateTask(ctx context.Context, t *Task, fields []string) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.update")
	defer m.tracer.EndSpan(span)

	updated, err := m.store.UpdateTask(ctx, t, fields)
	if err != nil {
		return nil, err
	}

	m.publishEvent(ctx, TopicTaskUpdated, updated, "")
	return updated, nil
}

// DeleteTask soft-deletes a task and records history + event.
func (m *Manager) DeleteTask(ctx context.Context, id string) error {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.delete")
	defer m.tracer.EndSpan(span)

	// Capture status before delete for history.
	existing, _ := m.store.GetTask(ctx, id)

	if err := m.store.DeleteTask(ctx, id); err != nil {
		return err
	}

	oldStatus := ""
	if existing != nil {
		oldStatus = StatusName(existing.Status)
	}
	m.recordHistory(ctx, id, "deleted", oldStatus, "", "", "")
	m.publishEvent(ctx, TopicTaskDeleted, existing, "")
	return nil
}

// ListTasks lists tasks with filtering and pagination.
func (m *Manager) ListTasks(ctx context.Context, opts ListTasksOpts) ([]*Task, int, error) {
	return m.store.ListTasks(ctx, opts)
}

// =============================================================================
// Workflow Operations (claim, release, close, transition)
// =============================================================================

// ClaimTask atomically claims a task for an agent session.
// Enforces WIP limits if the task's board has a lane with a WIP limit set.
func (m *Manager) ClaimTask(ctx context.Context, taskID, agentID, sessionID string) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.claim")
	defer m.tracer.EndSpan(span)

	// Check WIP limits before claiming.
	if err := m.checkWIPLimit(ctx, taskID); err != nil {
		return nil, err
	}

	claimed, err := m.store.ClaimTask(ctx, taskID, agentID, sessionID)
	if err != nil {
		return nil, err
	}

	m.recordHistory(ctx, taskID, "claimed", StatusName(loomv1.TaskStatus_TASK_STATUS_OPEN), StatusName(claimed.Status), agentID, sessionID)
	m.publishEvent(ctx, TopicTaskClaimed, claimed, agentID)

	return claimed, nil
}

// ReleaseTask releases a claim, returning the task to OPEN status.
func (m *Manager) ReleaseTask(ctx context.Context, taskID, sessionID string) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.release")
	defer m.tracer.EndSpan(span)

	released, err := m.store.ReleaseTask(ctx, taskID, sessionID)
	if err != nil {
		return nil, err
	}

	m.recordHistory(ctx, taskID, "released", StatusName(loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS), StatusName(released.Status), "", sessionID)
	m.publishEvent(ctx, TopicTaskReleased, released, "")

	return released, nil
}

// CloseTask marks a task as DONE and auto-completes the parent if all siblings are done.
func (m *Manager) CloseTask(ctx context.Context, taskID, reason string) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.close")
	defer m.tracer.EndSpan(span)

	// Capture old status before closing for history.
	existing, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	oldStatus := StatusName(existing.Status)

	closed, err := m.store.CloseTask(ctx, taskID, reason)
	if err != nil {
		return nil, err
	}

	m.recordHistory(ctx, taskID, "closed", oldStatus, StatusName(closed.Status), existing.AssigneeAgentID, existing.ClaimedBySession)
	m.publishEvent(ctx, TopicTaskCompleted, closed, "")

	// Auto-create graph memory summarizing the completed task.
	m.rememberTaskCompletion(ctx, closed)

	// Auto-complete parent if all children are done.
	if closed.ParentID != "" {
		m.tryAutoCompleteParent(ctx, closed.ParentID)
	}

	return closed, nil
}

// TransitionTask changes task status with validation.
func (m *Manager) TransitionTask(ctx context.Context, taskID string, newStatus loomv1.TaskStatus) (*Task, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.transition")
	defer m.tracer.EndSpan(span)

	existing, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	oldStatus := StatusName(existing.Status)

	transitioned, err := m.store.TransitionTask(ctx, taskID, newStatus)
	if err != nil {
		return nil, err
	}

	m.recordHistory(ctx, taskID, "transitioned", oldStatus, StatusName(transitioned.Status), "", "")

	if newStatus == loomv1.TaskStatus_TASK_STATUS_BLOCKED {
		m.publishEvent(ctx, TopicTaskBlocked, transitioned, "")
	} else {
		m.publishEvent(ctx, TopicTaskUpdated, transitioned, "")
	}

	return transitioned, nil
}

// =============================================================================
// Dependencies (with cycle detection)
// =============================================================================

// AddDependency adds a dependency edge after checking for cycles.
// If the dependency type is BLOCKS and the blocker is not finished,
// the from_task is automatically transitioned to BLOCKED (from OPEN or IN_PROGRESS).
func (m *Manager) AddDependency(ctx context.Context, dep *TaskDependency) error {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.add_dependency")
	defer m.tracer.EndSpan(span)

	// Cycle detection: check if adding from→to would create a cycle.
	// Edge semantics: from_task depends on to_task (to_task blocks from_task).
	if err := m.detectCycle(ctx, dep.FromTaskID, dep.ToTaskID); err != nil {
		return err
	}

	if err := m.store.AddDependency(ctx, dep); err != nil {
		return err
	}

	// Auto-blocking: if this is a BLOCKS dependency and the blocker isn't done,
	// transition the dependent task to BLOCKED (from OPEN or IN_PROGRESS).
	if dep.Type == loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS {
		blocker, err := m.store.GetTask(ctx, dep.ToTaskID)
		if err == nil && !IsTerminal(blocker.Status) {
			dependent, err := m.store.GetTask(ctx, dep.FromTaskID)
			if err == nil && (dependent.Status == loomv1.TaskStatus_TASK_STATUS_OPEN ||
				dependent.Status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS) {
				_, _ = m.TransitionTask(ctx, dep.FromTaskID, loomv1.TaskStatus_TASK_STATUS_BLOCKED)
			}
		}
	}

	return nil
}

// RemoveDependency removes a dependency edge and checks if the dependent task
// should be unblocked (transitioned from BLOCKED back to OPEN).
func (m *Manager) RemoveDependency(ctx context.Context, fromTaskID, toTaskID string) error {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.remove_dependency")
	defer m.tracer.EndSpan(span)

	if err := m.store.RemoveDependency(ctx, fromTaskID, toTaskID); err != nil {
		return err
	}

	// Check if the dependent task should be unblocked.
	m.tryUnblock(ctx, fromTaskID)

	return nil
}

// GetReadyFront returns tasks with all dependencies satisfied.
func (m *Manager) GetReadyFront(ctx context.Context, boardID string, opts ReadyFrontOpts) ([]*Task, error) {
	return m.store.GetReadyFront(ctx, boardID, opts)
}

// GetBlockedTasks returns tasks waiting on unfinished dependencies.
func (m *Manager) GetBlockedTasks(ctx context.Context, boardID string) ([]*Task, error) {
	return m.store.GetBlockedTasks(ctx, boardID)
}

// =============================================================================
// Boards
// =============================================================================

// CreateBoard creates a new kanban board.
func (m *Manager) CreateBoard(ctx context.Context, board *TaskBoard) (*TaskBoard, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.create_board")
	defer m.tracer.EndSpan(span)

	created, err := m.store.CreateBoard(ctx, board)
	if err != nil {
		return nil, err
	}
	m.publishEvent(ctx, TopicBoardUpdated, nil, "")
	return created, nil
}

// GetBoard retrieves a board by ID.
func (m *Manager) GetBoard(ctx context.Context, id string) (*TaskBoard, error) {
	return m.store.GetBoard(ctx, id)
}

// ListBoards lists all boards.
func (m *Manager) ListBoards(ctx context.Context) ([]*TaskBoard, error) {
	return m.store.ListBoards(ctx)
}

// =============================================================================
// History
// =============================================================================

// GetHistory retrieves the audit trail for a task.
func (m *Manager) GetHistory(ctx context.Context, taskID string) ([]*TaskHistoryEntry, error) {
	return m.store.GetHistory(ctx, taskID)
}

// =============================================================================
// Compaction
// =============================================================================

// CompactClosedTasks summarizes old closed tasks to reduce context token usage.
// Tasks older than maxAge with compaction_level=0 get their description and notes
// replaced with a compact summary. The summary is generated by the provided
// summarize function (typically an LLM call — wired in Phase 4 Decomposer).
// Returns the number of tasks compacted.
func (m *Manager) CompactClosedTasks(ctx context.Context, boardID string, maxAge time.Duration, summarize func(t *Task) (string, error)) (int, error) {
	ctx, span := m.tracer.StartSpan(ctx, "task_manager.compact")
	defer m.tracer.EndSpan(span)

	tasks, _, err := m.store.ListTasks(ctx, ListTasksOpts{
		BoardID: boardID,
		Status:  loomv1.TaskStatus_TASK_STATUS_DONE,
		Limit:   100,
	})
	if err != nil {
		return 0, fmt.Errorf("list tasks for compaction: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	compacted := 0
	for _, t := range tasks {
		if t.CompactionLevel > 0 {
			continue // already compacted
		}
		if t.ClosedAt == nil || t.ClosedAt.After(cutoff) {
			continue // too recent
		}

		summary, err := summarize(t)
		if err != nil {
			m.logger.Warn("compaction summarize failed", zap.String("task_id", t.ID), zap.Error(err))
			continue
		}

		t.CompactedSummary = summary
		t.CompactionLevel = 1
		t.Description = "" // clear full content
		t.Notes = ""
		t.Approach = ""
		if _, err := m.store.UpdateTask(ctx, t, nil); err != nil {
			m.logger.Warn("compaction update failed", zap.String("task_id", t.ID), zap.Error(err))
			continue
		}
		compacted++
	}

	return compacted, nil
}

// =============================================================================
// WIP Limit Enforcement
// =============================================================================

// checkWIPLimit verifies that claiming this task won't exceed the board lane's
// work-in-progress limit. Returns an error if the limit would be exceeded.
func (m *Manager) checkWIPLimit(ctx context.Context, taskID string) error {
	t, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if t.BoardID == "" {
		return nil // no board, no WIP limit
	}

	board, err := m.store.GetBoard(ctx, t.BoardID)
	if err != nil {
		return nil // board not found, allow claim
	}

	// Find the IN_PROGRESS lane and check its WIP limit.
	for _, lane := range board.Lanes {
		if lane.Status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS && lane.WIPLimit > 0 {
			// Count current IN_PROGRESS tasks on this board.
			inProgress, _, err := m.store.ListTasks(ctx, ListTasksOpts{
				BoardID: t.BoardID,
				Status:  loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
				Limit:   lane.WIPLimit + 1, // only need to know if we exceed
			})
			if err != nil {
				return nil // on error, allow claim
			}
			if len(inProgress) >= lane.WIPLimit {
				return fmt.Errorf("WIP limit reached: board %q lane %q has %d/%d tasks in progress",
					board.Name, lane.Name, len(inProgress), lane.WIPLimit)
			}
			break
		}
	}
	return nil
}

// =============================================================================
// Cycle Detection (DFS)
// =============================================================================

// detectCycle checks if adding an edge from→to would create a cycle in the
// BLOCKS dependency graph. Uses DFS: starting from toTaskID, follow all
// outgoing BLOCKS dependencies. If we reach fromTaskID, a cycle exists.
//
// Edge semantics: from_task depends on to_task. GetDependencies(X) returns
// edges where X is from_task_id, meaning "X depends on Y" for each result.
// So following dep.ToTaskID walks the "depends on" chain.
func (m *Manager) detectCycle(ctx context.Context, fromTaskID, toTaskID string) error {
	if fromTaskID == toTaskID {
		return fmt.Errorf("cannot add self-dependency: %s", fromTaskID)
	}

	visited := make(map[string]bool)
	return m.dfs(ctx, toTaskID, fromTaskID, visited)
}

// dfs walks outgoing dependencies from current, looking for target.
func (m *Manager) dfs(ctx context.Context, current, target string, visited map[string]bool) error {
	if current == target {
		return fmt.Errorf("adding dependency would create a cycle (path reaches %s)", target)
	}
	if visited[current] {
		return nil
	}
	visited[current] = true

	deps, err := m.store.GetDependencies(ctx, current)
	if err != nil {
		return fmt.Errorf("cycle detection: %w", err)
	}
	for _, dep := range deps {
		if dep.Type == loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS {
			if err := m.dfs(ctx, dep.ToTaskID, target, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

// =============================================================================
// Auto-Status Propagation
// =============================================================================

// tryAutoCompleteParent checks if all children of the parent are done.
// If so, auto-closes the parent with a reason explaining the auto-completion.
func (m *Manager) tryAutoCompleteParent(ctx context.Context, parentID string) {
	children, _, err := m.store.ListTasks(ctx, ListTasksOpts{
		ParentID: parentID,
		Limit:    1000,
	})
	if err != nil || len(children) == 0 {
		return
	}

	allDone := true
	for _, child := range children {
		if !IsTerminal(child.Status) {
			allDone = false
			break
		}
	}

	if allDone {
		parent, err := m.store.GetTask(ctx, parentID)
		if err != nil || IsTerminal(parent.Status) {
			return
		}
		oldStatus := StatusName(parent.Status)
		reason := fmt.Sprintf("auto-completed: all %d child tasks are done", len(children))
		closed, err := m.store.CloseTask(ctx, parentID, reason)
		if err != nil {
			m.logger.Warn("auto-complete parent failed", zap.String("parent_id", parentID), zap.Error(err))
			return
		}
		m.recordHistory(ctx, parentID, "auto-closed", oldStatus, StatusName(loomv1.TaskStatus_TASK_STATUS_DONE), "", "")
		m.publishEvent(ctx, TopicTaskCompleted, closed, "")

		// Recurse: if the parent itself has a parent, check that too.
		if parent.ParentID != "" {
			m.tryAutoCompleteParent(ctx, parent.ParentID)
		}
	}
}

// tryUnblock checks if a BLOCKED task should be unblocked after a dependency removal.
// If all remaining BLOCKS dependencies are satisfied, transition to OPEN.
func (m *Manager) tryUnblock(ctx context.Context, taskID string) {
	t, err := m.store.GetTask(ctx, taskID)
	if err != nil || t.Status != loomv1.TaskStatus_TASK_STATUS_BLOCKED {
		return
	}

	deps, err := m.store.GetDependencies(ctx, taskID)
	if err != nil {
		return
	}

	// Check if any remaining BLOCKS dependency is unsatisfied.
	for _, dep := range deps {
		if dep.Type != loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS {
			continue
		}
		blocker, err := m.store.GetTask(ctx, dep.ToTaskID)
		if err != nil {
			return // can't determine, stay blocked
		}
		if !IsTerminal(blocker.Status) {
			return // still blocked
		}
	}

	// All blockers are done — unblock.
	_, _ = m.TransitionTask(ctx, taskID, loomv1.TaskStatus_TASK_STATUS_OPEN)
}

// =============================================================================
// Helpers
// =============================================================================

// rememberTaskCompletion creates a graph memory entry when a task is completed.
// Links the memory to any entities referenced by the task. Best-effort.
func (m *Manager) rememberTaskCompletion(ctx context.Context, t *Task) {
	if m.graphMemory == nil || t == nil {
		return
	}

	content := fmt.Sprintf("Completed task: %s\nObjective: %s\nResult: %s",
		t.Title, t.Objective, t.CloseReason)
	if t.Notes != "" {
		// Include last ~300 chars of notes for context.
		notes := t.Notes
		if len(notes) > 300 {
			notes = notes[len(notes)-300:]
		}
		content += "\nNotes: " + notes
	}

	summary := fmt.Sprintf("Task completed: %s — %s", t.Title, t.CloseReason)

	tags := append([]string{"task-completion"}, t.Tags...)
	tags = append(tags, CategoryName(t.Category))

	mem := &memory.Memory{
		AgentID:       t.OwnerAgentID,
		Content:       content,
		Summary:       summary,
		MemoryType:    "experience",
		Source:        "task",
		SourceID:      t.ID,
		MemoryAgentID: t.AssigneeAgentID,
		Tags:          tags,
		Salience:      0.6, // moderately salient — completed work is worth remembering
		EntityIDs:     t.EntityIDs,
	}

	_, err := m.graphMemory.Remember(ctx, mem)
	if err != nil {
		m.logger.Debug("failed to create task completion memory",
			zap.String("task_id", t.ID),
			zap.Error(err))
	}
}

// populateChildIDs queries child tasks and fills the ChildIDs field.
func (m *Manager) populateChildIDs(ctx context.Context, t *Task) {
	if t == nil {
		return
	}
	children, _, err := m.store.ListTasks(ctx, ListTasksOpts{
		ParentID: t.ID,
		Limit:    1000,
	})
	if err != nil {
		return
	}
	t.ChildIDs = make([]string, 0, len(children))
	for _, child := range children {
		t.ChildIDs = append(t.ChildIDs, child.ID)
	}
}

// recordHistory creates an audit trail entry. Best-effort — errors are logged, not returned.
func (m *Manager) recordHistory(ctx context.Context, taskID, action, oldStatus, newStatus, agentID, sessionID string) {
	err := m.store.RecordHistory(ctx, &TaskHistoryEntry{
		TaskID:    taskID,
		Action:    action,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		AgentID:   agentID,
		SessionID: sessionID,
	})
	if err != nil {
		m.logger.Warn("failed to record task history",
			zap.String("task_id", taskID),
			zap.String("action", action),
			zap.Error(err))
	}
}

// publishEvent publishes a task lifecycle event to the message bus. Best-effort.
func (m *Manager) publishEvent(ctx context.Context, topic string, t *Task, agentID string) {
	if m.bus == nil {
		return
	}

	metadata := map[string]string{
		"topic": topic,
	}
	if t != nil {
		metadata["task_id"] = t.ID
		metadata["status"] = StatusName(t.Status)
		metadata["board_id"] = t.BoardID
	}
	if agentID != "" {
		metadata["agent_id"] = agentID
	}

	var payload []byte
	if t != nil {
		payload, _ = json.Marshal(t)
	}

	msg := &loomv1.BusMessage{
		Id:        uuid.New().String(),
		Topic:     topic,
		FromAgent: agentID,
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{Value: payload},
		},
		Metadata:  metadata,
		Timestamp: time.Now().UnixMilli(),
	}

	delivered, dropped, err := m.bus.Publish(ctx, topic, msg)
	if err != nil {
		m.logger.Debug("task event publish failed", zap.String("topic", topic), zap.Error(err))
	} else if dropped > 0 {
		m.logger.Debug("task event dropped", zap.String("topic", topic), zap.Int("delivered", delivered), zap.Int("dropped", dropped))
	}
}

// IsTerminal returns true if the status is a terminal state (DONE, DEFERRED, CANCELLED).
func IsTerminal(status loomv1.TaskStatus) bool {
	switch status {
	case loomv1.TaskStatus_TASK_STATUS_DONE,
		loomv1.TaskStatus_TASK_STATUS_DEFERRED,
		loomv1.TaskStatus_TASK_STATUS_CANCELLED:
		return true
	default:
		return false
	}
}

// PriorityName returns the human-readable name of a task priority.
func PriorityName(priority loomv1.TaskPriority) string {
	switch priority {
	case loomv1.TaskPriority_TASK_PRIORITY_CRITICAL:
		return "P0"
	case loomv1.TaskPriority_TASK_PRIORITY_HIGH:
		return "P1"
	case loomv1.TaskPriority_TASK_PRIORITY_MEDIUM:
		return "P2"
	case loomv1.TaskPriority_TASK_PRIORITY_LOW:
		return "P3"
	case loomv1.TaskPriority_TASK_PRIORITY_BACKLOG:
		return "P4"
	default:
		return "P2"
	}
}

// CategoryName returns the human-readable name of a task category.
func CategoryName(category loomv1.TaskCategory) string {
	switch category {
	case loomv1.TaskCategory_TASK_CATEGORY_RESEARCH:
		return "research"
	case loomv1.TaskCategory_TASK_CATEGORY_ANALYSIS:
		return "analysis"
	case loomv1.TaskCategory_TASK_CATEGORY_IMPLEMENTATION:
		return "implementation"
	case loomv1.TaskCategory_TASK_CATEGORY_REVIEW:
		return "review"
	case loomv1.TaskCategory_TASK_CATEGORY_WRITING:
		return "writing"
	case loomv1.TaskCategory_TASK_CATEGORY_DECISION:
		return "decision"
	case loomv1.TaskCategory_TASK_CATEGORY_INVESTIGATION:
		return "investigation"
	case loomv1.TaskCategory_TASK_CATEGORY_PLANNING:
		return "planning"
	default:
		return "other"
	}
}

// StatusName returns the human-readable name of a task status.
func StatusName(status loomv1.TaskStatus) string {
	switch status {
	case loomv1.TaskStatus_TASK_STATUS_OPEN:
		return "OPEN"
	case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
		return "IN_PROGRESS"
	case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
		return "BLOCKED"
	case loomv1.TaskStatus_TASK_STATUS_DONE:
		return "DONE"
	case loomv1.TaskStatus_TASK_STATUS_DEFERRED:
		return "DEFERRED"
	case loomv1.TaskStatus_TASK_STATUS_CANCELLED:
		return "CANCELLED"
	default:
		return "UNSPECIFIED"
	}
}
