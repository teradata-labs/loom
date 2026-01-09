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
package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/internal/tui/components/chat/todos"
	"github.com/teradata-labs/loom/internal/tui/styles"
)

func hasIncompleteTodos(todos []session.Todo) bool {
	for _, todo := range todos {
		if todo.Status != session.TodoStatusCompleted {
			return true
		}
	}
	return false
}

const (
	pillHeightWithBorder  = 3
	maxTaskDisplayLength  = 40
	maxQueueDisplayLength = 60
)

func queuePill(queue int, focused, pillsPanelFocused bool, t *styles.Theme) string {
	if queue <= 0 {
		return ""
	}
	triangles := styles.ForegroundGrad("▶▶▶▶▶▶▶▶▶", false, t.RedDark, t.Accent)
	if queue < 10 {
		triangles = triangles[:queue]
	}

	content := fmt.Sprintf("%s %d Queued", strings.Join(triangles, ""), queue)

	style := t.S().Base.PaddingLeft(1).PaddingRight(1)
	if !pillsPanelFocused || focused {
		style = style.BorderStyle(lipgloss.RoundedBorder()).BorderForeground(t.BgOverlay)
	} else {
		style = style.BorderStyle(lipgloss.HiddenBorder())
	}
	return style.Render(content)
}

func todoPill(todos []session.Todo, spinnerView string, focused, pillsPanelFocused bool, t *styles.Theme) string {
	if !hasIncompleteTodos(todos) {
		return ""
	}

	completed := 0
	var currentTodo *session.Todo
	for i := range todos {
		switch todos[i].Status {
		case session.TodoStatusCompleted:
			completed++
		case session.TodoStatusInProgress:
			if currentTodo == nil {
				currentTodo = &todos[i]
			}
		}
	}

	total := len(todos)

	label := "To-Do"
	progress := t.S().Base.Foreground(t.FgMuted).Render(fmt.Sprintf("%d/%d", completed, total))

	var content string
	if pillsPanelFocused {
		content = fmt.Sprintf("%s %s", label, progress)
	} else if currentTodo != nil {
		taskText := currentTodo.Content
		if currentTodo.ActiveForm != "" {
			taskText = currentTodo.ActiveForm
		}
		if len(taskText) > maxTaskDisplayLength {
			taskText = taskText[:maxTaskDisplayLength-1] + "…"
		}
		task := t.S().Base.Foreground(t.FgSubtle).Render(taskText)
		content = fmt.Sprintf("%s %s %s  %s", spinnerView, label, progress, task)
	} else {
		content = fmt.Sprintf("%s %s", label, progress)
	}

	style := t.S().Base.PaddingLeft(1).PaddingRight(1)
	if !pillsPanelFocused || focused {
		style = style.BorderStyle(lipgloss.RoundedBorder()).BorderForeground(t.BgOverlay)
	} else {
		style = style.BorderStyle(lipgloss.HiddenBorder())
	}
	return style.Render(content)
}

func todoList(sessionTodos []session.Todo, spinnerView string, t *styles.Theme, width int) string {
	return todos.FormatTodosList(sessionTodos, spinnerView, t, width)
}

func queueList(queueItems []string, t *styles.Theme) string {
	if len(queueItems) == 0 {
		return ""
	}

	var lines []string
	for _, item := range queueItems {
		text := item
		if len(text) > maxQueueDisplayLength {
			text = text[:maxQueueDisplayLength-1] + "…"
		}
		prefix := t.S().Base.Foreground(t.FgMuted).Render("  •") + " "
		lines = append(lines, prefix+t.S().Base.Foreground(t.FgMuted).Render(text))
	}

	return strings.Join(lines, "\n")
}
