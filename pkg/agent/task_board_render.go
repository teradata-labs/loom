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
//
// Not yet wired into the prompt path; consumed by the #329 follow-up that
// injects the per-step transient tail block.
const SessionChecklistBudget = 2400

// sessionChecklistListLimit bounds the board query behind one render.
const sessionChecklistListLimit = 200

// sessionChecklistLiveStatuses are the statuses a checklist renders: work
// that is running, waiting, or still to do. DONE and CANCELLED tasks are
// filtered out server-side so a long-lived store cannot crowd live items out
// of the query window.
var sessionChecklistLiveStatuses = []loomv1.TaskStatus{
	loomv1.TaskStatus_TASK_STATUS_OPEN,
	loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
	loomv1.TaskStatus_TASK_STATUS_BLOCKED,
	loomv1.TaskStatus_TASK_STATUS_DEFERRED,
}

// RenderSessionChecklist renders the session's working checklist from
// task-board state as a compact, budgeted block for per-step context
// injection. The query is scoped server-side to the session's live tasks
// (claimed by it or created in it; DONE/CANCELLED excluded) and windowed
// newest-first, so grown multi-session stores can neither leak foreign tasks
// into the block nor push the session's recent tasks out of the window.
// In-progress items show their acceptance criteria (what must hold before
// closing); blocked and pending items show titles. Returns "" when the
// session has no live checklist items. budget <= 0 uses
// SessionChecklistBudget.
//
// Not yet wired into the prompt path; consumed by the #329 follow-up that
// injects the per-step transient tail block.
func RenderSessionChecklist(ctx context.Context, mgr *task.Manager, sessionID string, budget int) string {
	if mgr == nil || sessionID == "" {
		return ""
	}
	if budget <= 0 {
		budget = SessionChecklistBudget
	}

	tasks, total, err := mgr.ListTasks(ctx, task.ListTasksOpts{
		SessionID:   sessionID,
		Statuses:    sessionChecklistLiveStatuses,
		NewestFirst: true,
		Limit:       sessionChecklistListLimit,
	})
	if err != nil {
		return ""
	}

	var inProgress, blocked, pending []*task.Task
	for _, tk := range tasks {
		switch tk.Status {
		case loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS:
			inProgress = append(inProgress, tk)
		case loomv1.TaskStatus_TASK_STATUS_BLOCKED:
			blocked = append(blocked, tk)
		case loomv1.TaskStatus_TASK_STATUS_OPEN, loomv1.TaskStatus_TASK_STATUS_DEFERRED:
			pending = append(pending, tk)
		}
	}
	if len(inProgress) == 0 && len(blocked) == 0 && len(pending) == 0 {
		return ""
	}

	w := &budgetedChecklistWriter{budget: budget}
	w.write("## Session task checklist\n")
	for _, tk := range inProgress {
		w.write("IN PROGRESS: " + escapeChecklistText(tk.Title) + "\n")
		if tk.AcceptanceCriteria != "" {
			w.write("  criteria: " + escapeChecklistText(tk.AcceptanceCriteria) + "\n")
		}
	}
	for _, tk := range blocked {
		w.write("BLOCKED: " + escapeChecklistText(tk.Title) + "\n")
	}
	if len(pending) > 0 {
		titles := make([]string, 0, len(pending))
		for _, tk := range pending {
			titles = append(titles, escapeChecklistText(tk.Title))
		}
		w.write("PENDING: " + strings.Join(titles, "; ") + "\n")
	}
	if total > len(tasks) {
		w.write(fmt.Sprintf("(%d more live tasks not shown)\n", total-len(tasks)))
	}
	w.write("The work is complete only when nothing is in progress, blocked, or pending.\n")
	return w.String()
}

// checklistTextEscaper rewrites line breaks as the two-character sequence
// `\n` so a task title or criteria string containing newlines cannot
// fabricate additional checklist lines in the rendered block.
var checklistTextEscaper = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`)

func escapeChecklistText(s string) string {
	return checklistTextEscaper.Replace(s)
}

// budgetedChecklistWriter appends strings until the byte budget would be
// exceeded. On the first overflow it appends a truncation marker (when the
// marker itself fits) and then drops every subsequent write, so nothing is
// ever emitted after the marker. Every write — the header included — counts
// against the budget.
type budgetedChecklistWriter struct {
	b         strings.Builder
	budget    int
	truncated bool
}

func (w *budgetedChecklistWriter) write(s string) {
	if w.truncated {
		return
	}
	if w.b.Len()+len(s) <= w.budget {
		w.b.WriteString(s)
		return
	}
	const marker = "(… truncated …)\n"
	if w.b.Len()+len(marker) <= w.budget {
		w.b.WriteString(marker)
	}
	w.truncated = true
}

func (w *budgetedChecklistWriter) String() string {
	return w.b.String()
}
