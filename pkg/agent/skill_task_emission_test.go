// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
)

// These tests cover the wiring that turns a manage_skills(load) into task rows:
// ManageSkillsTool.load → Agent.emitSkillTasksAsync → Emitter.EmitForActivation.
// The emitter's own decision tree is covered in pkg/skills/tasks; what is under
// test here is that the call happens at all, with the right AgentTasksEnabled
// and BoardID, only for a new activation, without blocking the tool's return,
// and without failing the load when it fails.
//
// Every test joins the detached emit through Agent.waitForSkillTaskEmits (a
// sync.WaitGroup) — no test waits out a duration.

// --- fixtures ---------------------------------------------------------------

const (
	emitSkillName  = "emit-skill"
	plainSkillName = "plain-skill"
)

// emissionSkillFixtures is the on-disk library these tests load.
//   - emit-skill authors a two-step task_template with a depends_on edge.
//   - plain-skill authors none, so it can only reach the decomposer fallback.
var emissionSkillFixtures = map[string]string{
	emitSkillName + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: emit-skill
  title: Emit Skill
  description: Authors a task template.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Emit skill instructions body.
task_template:
  steps:
    - title: Analyze
      objective: Find the issues
      category: analysis
      priority: P1
    - title: Fix
      objective: Apply the changes
      priority: P1
      depends_on: [0]
`,
	plainSkillName + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: plain-skill
  title: Plain Skill
  description: Authors no task template.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Plain skill instructions body.
`,
}

func writeEmissionSkillFixtures(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range emissionSkillFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// --- doubles ----------------------------------------------------------------

// countingLLM records how many provider calls it received. Used to prove the
// template-less path never reaches the decomposer.
type countingLLM struct {
	mu    sync.Mutex
	calls int
}

func (c *countingLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &llmtypes.LLMResponse{Content: "unused"}, nil
}

func (c *countingLLM) Name() string  { return "counting" }
func (c *countingLLM) Model() string { return "counting-v1" }

func (c *countingLLM) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// gatedTaskStore wraps a real TaskStore and intercepts CreateTask only.
// Embedding the interface keeps every other method on the real store, so the
// idempotency lookup, history write and board ensure all behave normally.
//
//   - release non-nil: CreateTask blocks until it is closed. A synchronous
//     emit could not return from the tool call while it is held.
//   - err non-nil: CreateTask fails, which fails the whole emit.
type gatedTaskStore struct {
	task.TaskStore
	release chan struct{}
	err     error

	mu      sync.Mutex
	creates int
}

func (g *gatedTaskStore) CreateTask(ctx context.Context, tk *task.Task) (*task.Task, error) {
	g.mu.Lock()
	g.creates++
	g.mu.Unlock()
	if g.release != nil {
		<-g.release
	}
	if g.err != nil {
		return nil, g.err
	}
	return g.TaskStore.CreateTask(ctx, tk)
}

func (g *gatedTaskStore) createCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.creates
}

// --- rig --------------------------------------------------------------------

type emissionRig struct {
	agent       *Agent
	orch        *skills.Orchestrator
	taskManager *task.Manager
	llm         *countingLLM
}

type emissionRigOpts struct {
	// tasksEnabled is SkillsConfig.TasksEnabled: nil leaves the master switch
	// unset (default-true), &false turns emission off agent-wide.
	tasksEnabled *bool
	// skillTaskBoardID is SkillsConfig.SkillTaskBoardID (first choice).
	skillTaskBoardID string
	// defaultBoardID is TaskBoardConfig.DefaultBoardId (fallback).
	defaultBoardID string
	// boardEnabled is TaskBoardConfig.Enabled, which gates task_board tool
	// surfacing and must not gate emission.
	boardEnabled bool
	// wrapStore, when set, wraps the real task store before the manager sees it.
	wrapStore func(task.TaskStore) task.TaskStore
}

func newEmissionRig(t *testing.T, o emissionRigOpts) *emissionRig {
	t.Helper()

	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "tasks.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	var store task.TaskStore = sqlitestore.NewTaskStore(db, observability.NewNoOpTracer())
	if o.wrapStore != nil {
		store = o.wrapStore(store)
	}
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)

	lib := skills.NewLibrary(skills.WithSearchPaths(writeEmissionSkillFixtures(t)))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.SkillsConfig = &skills.SkillsConfig{
		Enabled:          true,
		TasksEnabled:     o.tasksEnabled,
		SkillTaskBoardID: o.skillTaskBoardID,
	}

	llm := &countingLLM{}
	a := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithSkillOrchestrator(orch),
		// Decomposer is nil: a template-less skill must not reach an LLM.
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{
			Enabled:        o.boardEnabled,
			DefaultBoardId: o.defaultBoardID,
		}),
	)
	// The emit goroutines outlive the tool call by design; join them before the
	// test's DB handle closes so a late write cannot hit a closed database.
	t.Cleanup(a.waitForSkillTaskEmits)

	return &emissionRig{agent: a, orch: orch, taskManager: mgr, llm: llm}
}

// loadSkill invokes the registered manage_skills tool with the load action and
// returns its own Result.
func (r *emissionRig) loadSkill(t *testing.T, sessionID, name string) *shuttle.Result {
	t.Helper()
	tool := findRegisteredTool(r.agent, "manage_skills")
	require.NotNil(t, tool, "manage_skills is registered")
	res, err := tool.Execute(
		session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"action": "load", "name": name},
	)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// emittedTasks returns the tasks recorded for one (skill, session) run.
func (r *emissionRig) emittedTasks(t *testing.T, skillName, sessionID string) []*task.Task {
	t.Helper()
	out, err := r.taskManager.ListBySkillRun(context.Background(), skillName, sessionID)
	require.NoError(t, err)
	return out
}

// --- tests ------------------------------------------------------------------

func TestSkillTaskEmission_OnManageSkillsLoad(t *testing.T) {
	disabled := false

	tests := []struct {
		name       string
		skill      string
		opts       emissionRigOpts
		wantTasks  int
		wantTitles []string
		wantBoard  string
	}{
		{
			name:       "authored template materializes its steps",
			skill:      emitSkillName,
			opts:       emissionRigOpts{boardEnabled: true},
			wantTasks:  2,
			wantTitles: []string{"Analyze", "Fix"},
			wantBoard:  "",
		},
		{
			name:  "board id comes from the skills config first",
			skill: emitSkillName,
			opts: emissionRigOpts{
				boardEnabled:     true,
				skillTaskBoardID: "skill-board",
				defaultBoardID:   "default-board",
			},
			wantTasks: 2,
			wantBoard: "skill-board",
		},
		{
			name:  "board id falls back to the task board default",
			skill: emitSkillName,
			opts: emissionRigOpts{
				// Enabled=false gates task_board tool surfacing only; emission
				// needs a manager and an activation, not the tool.
				boardEnabled:   false,
				defaultBoardID: "default-board",
			},
			wantTasks: 2,
			wantBoard: "default-board",
		},
		{
			name:      "master switch off emits nothing",
			skill:     emitSkillName,
			opts:      emissionRigOpts{boardEnabled: true, tasksEnabled: &disabled},
			wantTasks: 0,
		},
		{
			name:      "no template and no decomposer emits nothing",
			skill:     plainSkillName,
			opts:      emissionRigOpts{boardEnabled: true},
			wantTasks: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newEmissionRig(t, tc.opts)
			const sessionID = "sess-1"

			res := rig.loadSkill(t, sessionID, tc.skill)
			require.True(t, res.Success, "the load itself must succeed")

			rig.agent.waitForSkillTaskEmits()

			got := rig.emittedTasks(t, tc.skill, sessionID)
			require.Len(t, got, tc.wantTasks)

			byTitle := map[string]*task.Task{}
			for _, tk := range got {
				byTitle[tk.Title] = tk
				assert.Equal(t, tc.wantBoard, tk.BoardID)
				assert.Equal(t, sessionID, tk.Metadata["skill_session"])
				assert.Equal(t, tc.skill, tk.Metadata["skill_name"])
				assert.Equal(t, "template", tk.Metadata["skill_emit_via"])
			}
			for _, want := range tc.wantTitles {
				assert.Contains(t, byTitle, want)
			}

			// Emission never consults a model: the template path is a pure
			// write, and the decomposer fallback is unreachable with a nil
			// decomposer (Emitter.emitDecomposed returns before touching req.LLM).
			assert.Zero(t, rig.llm.callCount(), "emission must not call the LLM")

			// The skill is active regardless of what emission did.
			assert.True(t, activeSkillNames(rig.orch, sessionID)[tc.skill])
		})
	}
}

// A repeat load of an already-active skill must not re-run emission. Emission is
// idempotent via SkillIdempotencyKey, so a repeat would be correct but would
// spend a board's worth of transactions rediscovering existing rows. The store's
// CreateTask counter is the observable: it must not move on the repeat.
func TestSkillTaskEmission_RepeatLoadDoesNotReEmit(t *testing.T) {
	var gate *gatedTaskStore
	rig := newEmissionRig(t, emissionRigOpts{
		boardEnabled: true,
		wrapStore: func(inner task.TaskStore) task.TaskStore {
			gate = &gatedTaskStore{TaskStore: inner}
			return gate
		},
	})
	const sessionID = "sess-repeat"

	require.True(t, rig.loadSkill(t, sessionID, emitSkillName).Success)
	rig.agent.waitForSkillTaskEmits()

	first := rig.emittedTasks(t, emitSkillName, sessionID)
	require.Len(t, first, 2)
	afterFirst := gate.createCount()
	require.Equal(t, 2, afterFirst)

	require.True(t, rig.loadSkill(t, sessionID, emitSkillName).Success)
	rig.agent.waitForSkillTaskEmits()

	second := rig.emittedTasks(t, emitSkillName, sessionID)
	require.Len(t, second, 2)
	for i := range first {
		assert.Equal(t, first[i].ID, second[i].ID, "no new rows on a repeat load")
	}
	assert.Equal(t, afterFirst, gate.createCount(),
		"a repeat load must not reach the store at all")
}

// The guard keys on the skill NAME, not on whether the session has any active
// skill. Loading a second, different skill is a new activation and must emit,
// even though the active set was already non-empty — which is exactly the case
// an active-set length check would get wrong.
func TestSkillTaskEmission_SecondDistinctSkillStillEmits(t *testing.T) {
	rig := newEmissionRig(t, emissionRigOpts{boardEnabled: true})
	const sessionID = "sess-two-skills"

	// plain-skill has no template, so it activates without emitting anything.
	require.True(t, rig.loadSkill(t, sessionID, plainSkillName).Success)
	rig.agent.waitForSkillTaskEmits()
	require.Len(t, rig.orch.GetActiveSkills(sessionID), 1)
	require.Empty(t, rig.emittedTasks(t, plainSkillName, sessionID))

	require.True(t, rig.loadSkill(t, sessionID, emitSkillName).Success)
	rig.agent.waitForSkillTaskEmits()
	assert.Len(t, rig.emittedTasks(t, emitSkillName, sessionID), 2)
}

// A failing emit is logged, not returned: the model already has the skill body,
// so a board that did not fill must not turn into a failed skill load.
func TestSkillTaskEmission_FailureDoesNotFailTheLoad(t *testing.T) {
	rig := newEmissionRig(t, emissionRigOpts{
		boardEnabled: true,
		wrapStore: func(inner task.TaskStore) task.TaskStore {
			return &gatedTaskStore{TaskStore: inner, err: errors.New("task store unavailable")}
		},
	})
	const sessionID = "sess-fail"

	res := rig.loadSkill(t, sessionID, emitSkillName)
	require.True(t, res.Success, "a failed emit must not fail the skill load")
	assert.Nil(t, res.Error)
	assert.Equal(t, "Skill loaded: "+emitSkillName, res.Data)
	assert.NotEmpty(t, res.Metadata["text_body"], "the skill body still reaches the model")

	rig.agent.waitForSkillTaskEmits()

	assert.Empty(t, rig.emittedTasks(t, emitSkillName, sessionID))
	assert.True(t, activeSkillNames(rig.orch, sessionID)[emitSkillName],
		"the skill is active even though its tasks did not materialize")
}

// The emit must not be on the tool's critical path. The gate below holds every
// CreateTask open, so a synchronous emit could not return from Execute at all —
// this test would hang rather than fail an assertion. Reaching the assertions is
// itself the proof; no duration is waited on.
func TestSkillTaskEmission_DoesNotBlockToolReturn(t *testing.T) {
	release := make(chan struct{})
	rig := newEmissionRig(t, emissionRigOpts{
		boardEnabled: true,
		wrapStore: func(inner task.TaskStore) task.TaskStore {
			return &gatedTaskStore{TaskStore: inner, release: release}
		},
	})
	const sessionID = "sess-nonblocking"

	// Registered after the rig so it runs BEFORE the rig's emit join (cleanups
	// are LIFO): an assertion that fails while the gate is held must still let
	// the emit goroutine finish rather than hang the package.
	releaseGate := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseGate)

	res := rig.loadSkill(t, sessionID, emitSkillName)
	require.True(t, res.Success)
	// Still holding the gate: the emit cannot have written anything yet.
	assert.Empty(t, rig.emittedTasks(t, emitSkillName, sessionID))

	releaseGate()
	rig.agent.waitForSkillTaskEmits()

	assert.Len(t, rig.emittedTasks(t, emitSkillName, sessionID), 2,
		"the emit completes on its own goroutine after the tool has returned")
}

// Without a session id the idempotency key "skill:<name>|sess:|step:<n>" would
// be shared by every session that lacks one, so the second such activation
// would adopt the first's tasks. Emission is skipped instead.
func TestSkillTaskEmission_SkippedWithoutSessionID(t *testing.T) {
	rig := newEmissionRig(t, emissionRigOpts{boardEnabled: true})

	require.True(t, rig.loadSkill(t, "", emitSkillName).Success)
	rig.agent.waitForSkillTaskEmits()

	probe, err := rig.taskManager.GetTaskByIdempotencyKey(
		context.Background(), "skill:"+emitSkillName+"|sess:|step:0")
	require.NoError(t, err)
	assert.Nil(t, probe, "no task may be keyed to the empty session")
}
