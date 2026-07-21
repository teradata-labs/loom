//go:build fts5

// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// D-6 (Skills v4 management) component acceptance test closing the gap
// flagged in review: the active-set-driven task emission half of
// cap-20-explicit-error-no-implicit-eviction-unload-only-removal-active-set-drives-tools-tasks
// was previously unasserted because every manage_skills_tool_test.go test
// wires taskEmitter as nil. This test wires a real skilltasks.Emitter over a
// real sqlite-backed task.Manager so ManageSkillsTool.executeLoad's
// emitActivationTasks branch (manage_skills_tool.go:228-230) actually runs,
// and confirms tasks were materialized for the activated skill.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// newTestTaskManager builds a real task.Manager backed by a temp sqlite
// file, mirroring pkg/skills/tasks/emitter_test.go's newTestManager helper.
func newTestTaskManager(t *testing.T) *task.Manager {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	store := sqlitestore.NewTaskStore(db, observability.NewNoOpTracer())
	return task.NewManager(store, nil, nil, nil)
}

func TestManageSkillsTool_Load_EmitsActivationTasksOverActiveSet(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name:  "task-emitting-skill",
		Title: "Task Emitting Skill",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{
				{Title: "Analyze", Objective: "find issues", Category: "analysis", Priority: "P1"},
				{Title: "Fix", Objective: "apply changes", Priority: "P1", DependsOn: []int32{0}},
			},
		},
	})
	orch := skills.NewOrchestrator(lib)

	manager := newTestTaskManager(t)
	emitter := skilltasks.NewEmitter(manager, nil)

	tool := NewManageSkillsTool(orch, emitter, nil, DefaultConfig(), nil, "test-agent", nil)

	sessionID := "sess-task-emission"
	ctx := ctxWithSession(sessionID)

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "task-emitting-skill"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, orch.GetActiveSkills(sessionID), 1, "the load must have activated the skill")

	tasks, err := manager.ListBySkillRun(context.Background(), "task-emitting-skill", sessionID)
	require.NoError(t, err)
	require.Len(t, tasks, 2, "expected one materialized task per authored template step")

	titles := make([]string, 0, len(tasks))
	for _, tk := range tasks {
		titles = append(titles, tk.Title)
	}
	assert.Contains(t, titles, "Analyze")
	assert.Contains(t, titles, "Fix")
}

func TestManageSkillsTool_Load_ReloadOfAlreadyActiveSkill_DoesNotReemitTasks(t *testing.T) {
	// emitActivationTasks only fires on a genuinely new activation
	// (!wasActive), not on a replace-in-place re-load of an already-active
	// skill (manage_skills_tool.go:228-230).
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name:  "reload-task-skill",
		Title: "Reload Task Skill",
		TaskTemplate: &skills.SkillTaskTemplate{
			Steps: []skills.SkillTaskStep{
				{Title: "Only Step", Objective: "do it", Priority: "P1"},
			},
		},
	})
	orch := skills.NewOrchestrator(lib)

	manager := newTestTaskManager(t)
	emitter := skilltasks.NewEmitter(manager, nil)
	tool := NewManageSkillsTool(orch, emitter, nil, DefaultConfig(), nil, "test-agent", nil)

	sessionID := "sess-reload-task-emission"
	ctx := ctxWithSession(sessionID)

	_, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "reload-task-skill"})
	require.NoError(t, err)

	tasksAfterFirstLoad, err := manager.ListBySkillRun(context.Background(), "reload-task-skill", sessionID)
	require.NoError(t, err)
	require.Len(t, tasksAfterFirstLoad, 1)

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "reload-task-skill"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, true, resultLoadMeta(t, result)["already_active"])

	tasksAfterReload, err := manager.ListBySkillRun(context.Background(), "reload-task-skill", sessionID)
	require.NoError(t, err)
	assert.Len(t, tasksAfterReload, 1, "re-loading an already-active skill must not emit duplicate tasks")
}
