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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
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

// =============================================================================
// Conversation-loop integration tests (Layer 1 e2e)
//
// These exercise the FULL runConversationLoop -> runEndOfTurnHygiene path
// via the public Chat() entry point. The unit tests above call
// runEndOfTurnHygiene directly; the tests below prove that the loop's
// "no tool calls -> hygiene -> continue" wiring actually re-runs the LLM
// and that retries are bounded by max_retries.
// =============================================================================

// hygieneLoopLLM is a deterministic LLM stub for the loop-integration tests.
// It records every call, runs an optional side-effect before returning
// (used by the happy-path test to "fix" the board between turns), and
// always returns a text-only response with no tool calls so the agent
// hits the end-of-turn return path on each call.
type hygieneLoopLLM struct {
	mu       sync.Mutex
	calls    int
	onCall   func(callIdx int) // optional side effect run before returning; nil = no-op
	contents []string          // per-call content; missing index -> "ack"
}

func (m *hygieneLoopLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	hook := m.onCall
	contents := m.contents
	m.mu.Unlock()

	if hook != nil {
		hook(idx)
	}

	content := "ack"
	if idx < len(contents) {
		content = contents[idx]
	}
	return &llmtypes.LLMResponse{
		Content:   content,
		ToolCalls: []llmtypes.ToolCall{},
		Usage:     llmtypes.Usage{InputTokens: 50, OutputTokens: 25, CostUSD: 0.001},
	}, nil
}

func (m *hygieneLoopLLM) Name() string  { return "hygiene-loop-mock" }
func (m *hygieneLoopLLM) Model() string { return "mock-v1" }

func (m *hygieneLoopLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// newAgentWithHygieneRig wires up a real Agent via NewAgent with a real
// SQLite-backed task manager, real skills orchestrator, the hygiene
// auditor/enforcer (constructed by NewAgent itself), and the given LLM
// stub. Returns the agent and the task manager so tests can seed/inspect
// task state.
func newAgentWithHygieneRig(t *testing.T, mockLLM LLMProvider) (*Agent, *task.Manager) {
	t.Helper()

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

	cfg := DefaultConfig()
	cfg.Name = "hygiene-loop-agent"
	// Disable the LLM-driven pattern classifier so the mock LLM is never
	// invoked outside our test path.
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	// Keep the loop tight: 1 max tool execution and 10 max turns is plenty
	// for hygiene retries (default cap is 2 retries -> 3 LLM calls).
	cfg.MaxTurns = 10
	cfg.MaxToolExecutions = 10

	ag := NewAgent(
		&mockBackend{},
		mockLLM,
		WithConfig(cfg),
		WithSkillOrchestrator(orch),
		WithTaskBoard(mgr, nil, nil),
	)
	return ag, mgr
}

// seedOpenUnstartedTask inserts a single OPEN-unstarted task tied to the
// given skill + session pair. The task is the dirty state the hygiene
// auditor must detect.
func seedOpenUnstartedTask(t *testing.T, mgr *task.Manager, skillName, sessionID, title string) *task.Task {
	t.Helper()
	created, _, err := mgr.CreateTaskIdempotent(context.Background(), &task.Task{
		Title:               title,
		Status:              loomv1.TaskStatus_TASK_STATUS_OPEN,
		SkillIdempotencyKey: "skill:" + skillName + "|sess:" + sessionID + "|step:" + title,
	})
	require.NoError(t, err)
	return created
}

// TestHygiene_FullLoop_RequireFix_FallthroughToAutoFix proves the loop's
// `continue` wiring drives multiple LLM calls and that REQUIRE_FIX falls
// through to AUTO_FIX after max_retries. The LLM stub never "fixes" the
// task, so the loop should: (1) call LLM, (2) hygiene injects, (3) call
// LLM again (retry 1), (4) hygiene injects, (5) call LLM again (retry 2),
// (6) hygiene caps out, falls through to AUTO_FIX, deferred the task,
// (7) return.
func TestHygiene_FullLoop_RequireFix_FallthroughToAutoFix(t *testing.T) {
	mockLLM := &hygieneLoopLLM{
		contents: []string{"first response", "second response", "third response"},
	}
	ag, mgr := newAgentWithHygieneRig(t, mockLLM)

	sessionID := "loop-sess-fallthrough"
	ag.skillOrchestrator.ActivateSkill(sessionID, &skills.Skill{Name: "sql"}, "test", "", 1.0)
	task1 := seedOpenUnstartedTask(t, mgr, "sql", sessionID, "design-index")

	resp, err := ag.Chat(context.Background(), sessionID, "hello")
	require.NoError(t, err)
	require.NotNil(t, resp)

	// With max_retries=2 (default), the loop should call the LLM exactly
	// 3 times: initial + 2 REQUIRE_FIX retries. After the third call the
	// auditor falls through to AUTO_FIX so the loop terminates.
	assert.Equal(t, 3, mockLLM.callCount(),
		"loop must call LLM initial + max_retries times before fallthrough")

	// Response metadata must surface hygiene state from the last pass.
	assert.Equal(t, 1, resp.Metadata["hygiene_violations_found"],
		"the final hygiene pass detected one OPEN-unstarted violation")
	assert.NotEmpty(t, resp.Metadata["hygiene_fallthrough"],
		"REQUIRE_FIX must record a fallthrough reason when it caps out")
	assert.Equal(t, 1, resp.Metadata["hygiene_resolved"],
		"AUTO_FIX must transition exactly one task (the OPEN-unstarted seed)")

	// The seeded task must end up DEFERRED in the persisted store.
	after, err := mgr.GetTask(context.Background(), task1.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DEFERRED, after.Status,
		"fallthrough AUTO_FIX must move OPEN-unstarted -> DEFERRED in the store")
	assert.Contains(t, after.Notes, "[hygiene]",
		"AUTO_FIX must annotate the task with a hygiene note")
}

// TestHygiene_FullLoop_RequireFix_AgentFixesOnRetry simulates the
// happy-path contract: the LLM "notices" the injected fixup message and
// resolves the violation on its second turn. The agent's side effect is
// modeled by the LLM stub transitioning the seeded task to DONE between
// its first and second responses — what a real LLM would accomplish by
// emitting a task_board tool call.
func TestHygiene_FullLoop_RequireFix_AgentFixesOnRetry(t *testing.T) {
	sessionID := "loop-sess-happy"
	var fixedTaskID string

	mockLLM := &hygieneLoopLLM{
		contents: []string{"working on it", "all done"},
	}
	ag, mgr := newAgentWithHygieneRig(t, mockLLM)
	ag.skillOrchestrator.ActivateSkill(sessionID, &skills.Skill{Name: "sql"}, "test", "", 1.0)
	task1 := seedOpenUnstartedTask(t, mgr, "sql", sessionID, "tune-query")
	fixedTaskID = task1.ID

	// Side effect: just before the LLM's second response (call index 1),
	// transition the task to DONE — the test stand-in for the agent
	// closing the task in response to the injected fixup message.
	mockLLM.mu.Lock()
	mockLLM.onCall = func(callIdx int) {
		if callIdx == 1 {
			_, _ = mgr.TransitionTask(context.Background(), fixedTaskID, loomv1.TaskStatus_TASK_STATUS_DONE)
		}
	}
	mockLLM.mu.Unlock()

	resp, err := ag.Chat(context.Background(), sessionID, "hello")
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Exactly two LLM calls: initial + one REQUIRE_FIX retry. The second
	// pass sees a clean board and exits without further retry.
	assert.Equal(t, 2, mockLLM.callCount(),
		"agent that resolves on retry must produce exactly one extra LLM call")

	// The final hygiene pass returns nil outcome (clean board) so
	// hygiene_* metadata is absent from the response. This is by design —
	// metadata is only stamped when the auditor actually ran AND found
	// violations. The proof of the retry is the call count above and the
	// task's terminal state below.
	assert.NotContains(t, resp.Metadata, "hygiene_fallthrough",
		"clean retry must not record a fallthrough reason")

	// Task is in DONE (the agent's fix), NOT DEFERRED (AUTO_FIX). This
	// distinguishes the happy path from the fallthrough test above.
	after, err := mgr.GetTask(context.Background(), task1.ID)
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, after.Status,
		"happy path: the agent's own fix wins; AUTO_FIX must not run")
	assert.NotContains(t, after.Notes, "[hygiene]",
		"happy path: no hygiene note because AUTO_FIX never fired")
}
