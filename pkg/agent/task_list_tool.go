// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"fmt"

	"strings"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// taskListRenderBudget bounds the transient-tail task block. 2400 bytes
// (≈600 tokens): room for a few in_progress criteria lines plus a dozen
// pending titles.
const taskListRenderBudget = 2400

const taskListDescription = `Your working checklist for multi-deliverable tasks.

create — at planning time, write one item per deliverable. Put the
deliverable's ACCEPTANCE CRITERIA from the task text into criteria, verbatim
where exactness matters (column semantics, thresholds, formats). Criteria
are write-once: they cannot be edited later, only cancelled with a reason
and re-created — re-read the task text before re-creating.

update — move items: in_progress when you start one, done when its
criteria hold in the built result, cancelled (reason required) when the
task text makes it unnecessary. Keep exactly one item in_progress at a
time. done and cancelled are final. Batch transitions by passing updates
as an array — completing the current item and starting the next is one
call.

For data deliverables fill output_contract — the columns, types, units,
and null semantics the reader of the output will rely on; verify the built
result against it in the final verification item.

The current list is shown to you on every step. The work is complete only
when no item is pending or in_progress.`

// TaskListTool manages the session's in-memory checklist (types.TaskList).
// The list lives on the session object — never in Messages, never persisted —
// so context relief cannot touch it and it dies with the session.
type TaskListTool struct {
	agent *Agent
}

// NewTaskListTool creates the session-checklist tool.
func NewTaskListTool(a *Agent) *TaskListTool { return &TaskListTool{agent: a} }

// Name returns the tool name.
func (t *TaskListTool) Name() string { return "task_list" }

// Backend returns "" — the tool is backend-independent.
func (t *TaskListTool) Backend() string { return "" }

// Description returns the normative tool description.
func (t *TaskListTool) Description() string { return taskListDescription }

// InputSchema returns the JSON schema for the tool input.
func (t *TaskListTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {Type: "string", Description: "create | update"},
			"items": {
				Type:        "array",
				Description: "(create) one entry per deliverable",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"title":           {Type: "string", Description: "the deliverable"},
						"criteria":        {Type: "string", Description: "its acceptance criteria, from the task text"},
						"output_contract": {Type: "string", Description: "for data deliverables: the columns, types, units, and null semantics the reader of this output will rely on"},
					},
					Required: []string{"title"},
				},
			},
			"updates": {
				Type:        "array",
				Description: "(update) transitions applied in order — batch completing the current item and starting the next into one call",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"id":     {Type: "integer", Description: "task id"},
						"status": {Type: "string", Description: "in_progress | done | cancelled"},
						"reason": {Type: "string", Description: "(cancelled) why the task text makes this unnecessary"},
					},
					Required: []string{"id", "status"},
				},
			},
			"id":     {Type: "integer", Description: "(update) task id — single-transition form; prefer updates"},
			"status": {Type: "string", Description: "(update) in_progress | done | cancelled"},
			"reason": {Type: "string", Description: "(update, cancelled) why the task text makes this unnecessary"},
		},
		Required: []string{"action"},
	}
}

func taskListFail(code, msg string) *shuttle.Result {
	return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: code, Message: msg}}
}

// Execute serves create and update against the session's checklist.
func (t *TaskListTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	sessionID := session.SessionIDFromContext(ctx)
	sess, ok := t.agent.memory.GetSession(sessionID)
	if !ok || sess == nil {
		return taskListFail("no_session", "no active session"), nil
	}

	action, _ := input["action"].(string)
	switch action {
	case "create":
		raw, _ := input["items"].([]interface{})
		if len(raw) == 0 {
			return taskListFail("invalid_input", "create requires items: [{title, criteria}]"), nil
		}
		items := make([]types.TaskItem, 0, len(raw))
		for _, r := range raw {
			m, _ := r.(map[string]interface{})
			title, _ := m["title"].(string)
			if title == "" {
				// Models routinely send "description" for the item name on
				// their first call; accept it rather than burn a reject.
				title, _ = m["description"].(string)
			}
			criteria, _ := m["criteria"].(string)
			contract, _ := m["output_contract"].(string)
			items = append(items, types.TaskItem{Title: title, Criteria: criteria, Contract: contract})
		}
		if sess.Tasks == nil {
			sess.Tasks = types.NewTaskList()
		}
		ids, err := sess.Tasks.Create(items)
		if err != nil {
			return taskListFail("invalid_input", err.Error()), nil
		}
		return &shuttle.Result{Success: true,
			Data: fmt.Sprintf("created %d task(s), ids %v — the list is shown to you on every step", len(ids), ids)}, nil

	case "update":
		if sess.Tasks == nil {
			return taskListFail("no_tasks", "no task list exists — create first"), nil
		}
		// Batch form: updates applied in declared order, each reported on its
		// own line; one failure does not stop the rest.
		if raw, ok := input["updates"].([]interface{}); ok && len(raw) > 0 {
			lines := make([]string, 0, len(raw))
			failed := 0
			for _, r := range raw {
				m, _ := r.(map[string]interface{})
				idF, ok := m["id"].(float64) // JSON numbers arrive as float64
				if !ok {
					lines = append(lines, "FAILED: update missing integer id")
					failed++
					continue
				}
				status, _ := m["status"].(string)
				reason, _ := m["reason"].(string)
				if err := sess.Tasks.Update(int(idF), status, reason); err != nil {
					lines = append(lines, "FAILED #"+fmt.Sprint(int(idF))+": "+err.Error())
					failed++
					continue
				}
				lines = append(lines, fmt.Sprintf("#%d → %s", int(idF), status))
			}
			return &shuttle.Result{Success: failed < len(raw),
				Data: strings.Join(lines, "\n")}, nil
		}
		idF, ok := input["id"].(float64) // JSON numbers arrive as float64
		if !ok {
			return taskListFail("invalid_input", "update requires updates: [{id, status}] or an integer id"), nil
		}
		status, _ := input["status"].(string)
		reason, _ := input["reason"].(string)
		if err := sess.Tasks.Update(int(idF), status, reason); err != nil {
			return taskListFail("invalid_input", err.Error()), nil
		}
		return &shuttle.Result{Success: true, Data: fmt.Sprintf("#%d → %s", int(idF), status)}, nil

	default:
		return taskListFail("invalid_input", "action must be create or update"), nil
	}
}
