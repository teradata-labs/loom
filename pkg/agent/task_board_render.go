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
// This file renders a session's working checklist FROM task-board state —
// the board-state-backed alternative to a separate session-checklist tool:
// one source of truth, rendered per step rather than duplicated (see the
// PR #329 discussion). A caller that injects per-step context (a transient
// tail block) calls RenderSessionChecklist each step; the block is
// regenerated from the board, so context relief can never corrupt it and
// there is no second state store to drift.
package agent

import (
	"context"
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/task"
)

// SessionChecklistBudget bounds the rendered checklist block. 2400 bytes
// (≈600 tokens): room for a few in-progress criteria lines plus a dozen
// pending titles.
const SessionChecklistBudget = 2400

// sessionChecklistListLimit bounds the board query behind one render.
const sessionChecklistListLimit = 200

// RenderSessionChecklist renders the session's working checklist from
// task-board state as a compact, budgeted block for per-step context
// injection. Included are tasks created by or claimed by the session:
// in-progress items show their acceptance criteria (what must hold before
// closing); blocked and pending items show titles; terminal items are
// counted. Returns "" when the session has no live checklist items.
// budget <= 0 uses SessionChecklistBudget.
func RenderSessionChecklist(ctx context.Context, mgr *task.Manager, sessionID string, budget int) string {
	if mgr == nil || sessionID == "" {
		return ""
	}
	if budget <= 0 {
		budget = SessionChecklistBudget
	}

	tasks, _, err := mgr.ListTasks(ctx, task.ListTasksOpts{Limit: sessionChecklistListLimit})
	if err != nil {
		return ""
	}

	var inProgress, blocked, pending []*task.Task
	var done, cancelled int
	for _, tk := range tasks {
		if !taskBelongsToSession(tk, sessionID) {
			continue
		}
		switch tk.Status {
		case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
			inProgress = append(inProgress, tk)
		case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
			blocked = append(blocked, tk)
		case loomv1.TaskStatus_TASK_STATUS_OPEN, loomv1.TaskStatus_TASK_STATUS_DEFERRED:
			pending = append(pending, tk)
		case loomv1.TaskStatus_TASK_STATUS_DONE:
			done++
		case loomv1.TaskStatus_TASK_STATUS_CANCELLED:
			cancelled++
		}
	}
	if len(inProgress) == 0 && len(blocked) == 0 && len(pending) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Session task checklist\n")
	for _, tk := range inProgress {
		writeWithinBudget(&b, budget, "IN PROGRESS: "+tk.Title+"\n")
		if tk.AcceptanceCriteria != "" {
			writeWithinBudget(&b, budget, "  criteria: "+tk.AcceptanceCriteria+"\n")
		}
	}
	for _, tk := range blocked {
		writeWithinBudget(&b, budget, "BLOCKED: "+tk.Title+"\n")
	}
	if len(pending) > 0 {
		titles := make([]string, 0, len(pending))
		for _, tk := range pending {
			titles = append(titles, tk.Title)
		}
		writeWithinBudget(&b, budget, "PENDING: "+strings.Join(titles, "; ")+"\n")
	}
	if done+cancelled > 0 {
		writeWithinBudget(&b, budget, fmt.Sprintf("(%d done, %d cancelled)\n", done, cancelled))
	}
	writeWithinBudget(&b, budget, "The work is complete only when nothing is in progress, blocked, or pending.\n")
	return b.String()
}

// taskBelongsToSession reports whether a task is part of the session's
// working set: claimed by it, or created in it (attribution metadata).
func taskBelongsToSession(tk *task.Task, sessionID string) bool {
	if tk.ClaimedBySession == sessionID {
		return true
	}
	return tk.Metadata != nil && tk.Metadata[task.CreatedBySessionMetadataKey] == sessionID
}

// writeWithinBudget appends s unless doing so would exceed the byte budget;
// on the first overflow it appends a truncation marker instead (once).
func writeWithinBudget(b *strings.Builder, budget int, s string) {
	const marker = "(… truncated …)\n"
	if b.Len()+len(s) <= budget {
		b.WriteString(s)
		return
	}
	if b.Len()+len(marker) <= budget && !strings.HasSuffix(b.String(), marker) {
		b.WriteString(marker)
	}
}
