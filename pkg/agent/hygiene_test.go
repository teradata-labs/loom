// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/hygiene"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
)

// hygieneTestRig builds a minimal Agent wired with everything the
// end-of-turn hygiene path depends on: a real (SQLite-backed) task
// manager, a real skills orchestrator, an auditor+enforcer, and a
// session. Avoids the full conversation loop and full LLM mock — the
// goal is to exercise runEndOfTurnHygiene under realistic state.
type hygieneTestRig struct {
	agent       *Agent
	taskManager *task.Manager
	session     *Session
}

func newHygieneTestRig(t *testing.T) *hygieneTestRig {
	t.Helper()

	// Real SQLite store so SkillIdempotencyKey + state transitions exercise
	// the same code path the production agent uses.
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "test.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	store := sqlite.NewTaskStore(db, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)

	orch := skills.NewOrchestrator(skills.NewLibrary())

	a := &Agent{
		id:                "test-agent",
		tools:             shuttle.NewRegistry(),
		memory:            NewMemory(),
		skillOrchestrator: orch,
		taskManager:       mgr,
		hygieneAuditor: hygiene.NewAuditor(mgr, orch,
			hygiene.WithTracer(observability.NewNoOpTracer()),
		),
		hygieneEnforcer: hygiene.NewEnforcer(mgr,
			hygiene.WithEnforcerTracer(observability.NewNoOpTracer()),
			hygiene.WithAgentID("test-agent"),
		),
		config: &Config{Name: "test-agent"},
	}

	sess := &Session{ID: "sess-1", AgentID: "test-agent", Messages: []types.Message{}}

	return &hygieneTestRig{
		agent:       a,
		taskManager: mgr,
		session:     sess,
	}
}

// seedTask inserts a task carrying the (skill, session) idempotency key
// at the requested status. ClaimedAt is set when the status is
// IN_PROGRESS or the caller passes wantClaimed=true (to model an OPEN
// task that was claimed-and-released, which is NOT a violation).
func (r *hygieneTestRig) seedTask(t *testing.T, skillName, title string, status loomv1.TaskStatus, wantClaimed bool) *task.Task {
	t.Helper()
	var claimedAt *time.Time
	if status == loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS || wantClaimed {
		now := time.Now().UTC()
		claimedAt = &now
	}
	created, _, err := r.taskManager.CreateTaskIdempotent(context.Background(), &task.Task{
		Title:               title,
		Status:              status,
		SkillIdempotencyKey: "skill:" + skillName + "|sess:" + r.session.ID + "|step:" + title,
		ClaimedAt:           claimedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	return created
}

func TestEndOfTurnHygiene_NoActiveSkills_NoOp(t *testing.T) {
	rig := newHygieneTestRig(t)

	retries := 0
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	assert.False(t, retry, "no active skills -> no retry")
	assert.Nil(t, outcome, "no active skills -> no outcome (clean report short-circuits)")
	assert.Equal(t, 0, retries)
	assert.Empty(t, rig.session.Messages, "no injection when board is clean")
}

func TestEndOfTurnHygiene_CleanBoard_NoOp(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)

	// Seed a DONE task — not a violation.
	rig.seedTask(t, "sql", "done-step", loomv1.TaskStatus_TASK_STATUS_DONE, false)

	retries := 0
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	assert.False(t, retry)
	assert.Nil(t, outcome, "clean board returns nil outcome")
	assert.Empty(t, rig.session.Messages)
}

func TestEndOfTurnHygiene_RequireFix_InjectsAndRetriesOnce(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)

	// Seed each violation kind so the report covers the full surface.
	rig.seedTask(t, "sql", "open-unstarted", loomv1.TaskStatus_TASK_STATUS_OPEN, false)
	rig.seedTask(t, "sql", "in-progress-orphan", loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, false)
	rig.seedTask(t, "sql", "blocked-no-hitl", loomv1.TaskStatus_TASK_STATUS_BLOCKED, false)

	// Default policy is REQUIRE_FIX; default max_retries is 2.
	retries := 0
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	require.True(t, retry, "REQUIRE_FIX with violations must request a retry")
	require.NotNil(t, outcome)
	assert.True(t, outcome.ShouldRetry)
	assert.Equal(t, 3, outcome.ViolationsFound)
	assert.Equal(t, 1, retries, "retries counter must be incremented exactly once per retry")

	// The synthetic fixup message must be appended to the session so the
	// LLM sees it on the next turn.
	require.Len(t, rig.session.Messages, 1)
	msg := rig.session.Messages[0]
	assert.Equal(t, "user", msg.Role)
	assert.Contains(t, msg.Content, "Task-board hygiene check found violations")
	assert.Contains(t, msg.Content, "open-unstarted")
	assert.Contains(t, msg.Content, "in-progress-orphan")
	assert.Contains(t, msg.Content, "blocked-no-hitl")
}

func TestEndOfTurnHygiene_RequireFix_FallthroughAtCap(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)

	// One OPEN-unstarted violation; AUTO_FIX must transition it to DEFERRED.
	created := rig.seedTask(t, "sql", "open-unstarted", loomv1.TaskStatus_TASK_STATUS_OPEN, false)

	// Pre-seed retries at the cap so the next pass falls through to AUTO_FIX.
	retries := hygiene.DefaultMaxRetries
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)

	assert.False(t, retry, "at-cap REQUIRE_FIX must fall through, not retry again")
	require.NotNil(t, outcome)
	assert.NotEmpty(t, outcome.FallthroughReason, "fallthrough reason must be recorded")
	assert.Equal(t, 1, outcome.Resolved, "one OPEN task must be auto-deferred")
	assert.Empty(t, rig.session.Messages, "fallthrough does not inject a message")

	// Verify the task was actually transitioned to DEFERRED in the store.
	got, err := rig.taskManager.GetTask(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DEFERRED, got.Status,
		"auto-fix must transition OPEN-unstarted -> DEFERRED in the persisted task store")
	assert.Contains(t, got.Notes, "[hygiene]", "auto-fix must annotate the task with a hygiene note")
}

func TestEndOfTurnHygiene_AutoFixPolicy_MutatesBoardImmediately(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)

	open := rig.seedTask(t, "sql", "open", loomv1.TaskStatus_TASK_STATUS_OPEN, false)
	inProg := rig.seedTask(t, "sql", "in-prog", loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, false)

	// Configure AUTO_FIX policy on the agent's SkillsConfig.
	rig.agent.config.SkillsConfig = &skills.SkillsConfig{
		Hygiene: &loomv1.HygieneConfig{
			Policy: loomv1.HygienePolicy_HYGIENE_POLICY_AUTO_FIX,
		},
	}

	retries := 0
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	assert.False(t, retry, "AUTO_FIX must not request a retry")
	require.NotNil(t, outcome)
	assert.Equal(t, 2, outcome.Resolved)

	openAfter, err := rig.taskManager.GetTask(context.Background(), open.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DEFERRED, openAfter.Status,
		"AUTO_FIX must defer OPEN-unstarted tasks")

	ipAfter, err := rig.taskManager.GetTask(context.Background(), inProg.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_OPEN, ipAfter.Status,
		"AUTO_FIX must release IN_PROGRESS orphans back to OPEN")
}

func TestEndOfTurnHygiene_DisabledConfig_NoOp(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)
	rig.seedTask(t, "sql", "open", loomv1.TaskStatus_TASK_STATUS_OPEN, false)

	// Explicitly disable hygiene.
	off := false
	rig.agent.config.SkillsConfig = &skills.SkillsConfig{
		Hygiene: &loomv1.HygieneConfig{Enabled: &off},
	}

	retries := 0
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	assert.False(t, retry)
	assert.Nil(t, outcome, "disabled hygiene must short-circuit before audit")
	assert.Empty(t, rig.session.Messages)
}

// TestEndOfTurnHygiene_FullLoop simulates the full retry contract: first
// pass produces a violation and injects a fixup; the caller "resolves"
// the violation (mimicking what the LLM would do on the next turn); the
// second pass returns clean.
func TestEndOfTurnHygiene_FullLoop(t *testing.T) {
	rig := newHygieneTestRig(t)
	rig.agent.skillOrchestrator.ActivateSkill(rig.session.ID, &skills.Skill{Name: "sql"}, "test", "", 1.0)
	created := rig.seedTask(t, "sql", "step", loomv1.TaskStatus_TASK_STATUS_OPEN, false)

	retries := 0
	retry, _ := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	require.True(t, retry, "first pass must request a retry")
	require.Equal(t, 1, retries)
	require.Len(t, rig.session.Messages, 1)

	// Simulate the LLM resolving the violation in the next turn by
	// transitioning the task to DONE.
	_, err := rig.taskManager.TransitionTask(context.Background(), created.ID, loomv1.TaskStatus_TASK_STATUS_DONE)
	require.NoError(t, err)

	// Second pass: board is clean, no further retry.
	retry, outcome := rig.agent.runEndOfTurnHygiene(context.Background(), rig.session, &retries)
	assert.False(t, retry, "second pass must not retry — board is clean")
	assert.Nil(t, outcome, "clean second pass returns nil outcome")
	assert.Equal(t, 1, retries, "retries counter must not advance when the second pass is clean")
	assert.Len(t, rig.session.Messages, 1, "no further message injection on clean retry")

	// Sanity check: the synthetic message instructed the LLM correctly.
	assert.True(t, strings.Contains(rig.session.Messages[0].Content, "Do not silently end the turn"),
		"injected message must include the explicit anti-silent-end-of-turn instruction")
}
